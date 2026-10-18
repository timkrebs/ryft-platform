package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/apple"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/sealed"
	"github.com/timkrebs/ryft/internal/store"
)

// deleteTimeout bounds one account deletion. A large account is a few million
// sample rows; the deletion runs to completion even if the client hangs up.
const deleteTimeout = 2 * time.Minute

// reconcileEvery is how often each api pod retries Supabase deletions that
// failed and erases accounts that reappeared (e.g. after a restore).
const reconcileEvery = 15 * time.Minute

// accounts deletes accounts: our rows (store), the Sign in with Apple grant
// (apple + key; nil when not configured) and the Supabase auth user (admin;
// nil in apikey mode or without SUPABASE_SERVICE_ROLE_KEY).
type accounts struct {
	st    *store.Store
	admin *auth.Admin
	apple *apple.Client
	key   *sealed.Key // encrypts stored Apple refresh tokens
	log   *slog.Logger
}

func newAccounts(st *store.Store, log *slog.Logger, c config.Auth) (*accounts, error) {
	a := &accounts{st: st, log: log}
	ac, ok, err := config.LoadApple()
	switch {
	case err != nil:
		return nil, err
	case ok:
		if a.apple, err = apple.New(apple.Config{TeamID: ac.TeamID, KeyID: ac.KeyID, ClientID: ac.ClientID, PrivateKey: ac.PrivateKey}, nil); err != nil {
			return nil, err
		}
		if a.key, err = sealed.ParseKey(ac.DataKey); err != nil {
			return nil, err
		}
	case c.Mode == config.AuthSupabase:
		log.Warn("APPLE_* not set: Sign in with Apple tokens are not revoked on account deletion")
	}
	if c.Mode != config.AuthSupabase {
		return a, nil
	}
	if c.AdminKey == "" {
		log.Warn("SUPABASE_SERVICE_ROLE_KEY not set: DELETE /v1/me erases the data but not the Supabase auth user")
		return a, nil
	}
	admin, err := auth.NewAdmin(c.Issuer, c.AdminKey, nil)
	if err != nil {
		return nil, err
	}
	a.admin = admin
	return a, nil
}

// deleteAuthUser deletes the Supabase auth user and records it; false if that
// is not possible now (the reconciler retries).
func (a *accounts) deleteAuthUser(ctx context.Context, id uuid.UUID) bool {
	if a.admin == nil {
		metrics.AccountDeletions.WithLabelValues("auth", "skipped").Inc()
		return false
	}
	if err := a.admin.DeleteUser(ctx, id); err != nil {
		metrics.AccountDeletions.WithLabelValues("auth", "error").Inc()
		a.log.WarnContext(ctx, "delete auth user", "deleted_user", id, "err", err)
		return false
	}
	metrics.AccountDeletions.WithLabelValues("auth", "ok").Inc()
	if err := a.st.MarkAuthDeleted(ctx, id); err != nil {
		// The auth user is gone; the next pass gets a 404 and records it then.
		a.log.WarnContext(ctx, "record auth deletion", "deleted_user", id, "err", err)
	}
	return true
}

// revokeApple revokes the deleted account's Sign in with Apple token, if one
// was stored: nil when there is none, false when it must be retried.
func (a *accounts) revokeApple(ctx context.Context, id uuid.UUID) *bool {
	enc, err := a.st.DeletedAppleToken(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	revoked := false
	switch {
	case err != nil:
		a.log.WarnContext(ctx, "load apple token", "deleted_user", id, "err", err)
	case a.apple == nil:
		metrics.AccountDeletions.WithLabelValues("apple", "skipped").Inc()
		a.log.WarnContext(ctx, "apple token stored but revocation not configured", "deleted_user", id)
	default:
		token, openErr := a.key.Open(enc, id[:])
		if openErr != nil {
			metrics.AccountDeletions.WithLabelValues("apple", "error").Inc()
			a.log.ErrorContext(ctx, "apple token does not decrypt (wrong DATA_ENCRYPTION_KEY?)", "deleted_user", id)
			break
		}
		if err := a.apple.Revoke(ctx, string(token)); err != nil {
			metrics.AccountDeletions.WithLabelValues("apple", "error").Inc()
			a.log.WarnContext(ctx, "revoke apple token", "deleted_user", id, "err", err)
			break
		}
		metrics.AccountDeletions.WithLabelValues("apple", "ok").Inc()
		if err := a.st.MarkAppleRevoked(ctx, id); err != nil {
			// Revoked at Apple; the next pass revokes again, which Apple accepts.
			a.log.WarnContext(ctx, "record apple revocation", "deleted_user", id, "err", err)
		}
		revoked = true
	}
	return &revoked
}

// reconcile erases accounts that have rows again despite their tombstone and
// retries pending Apple revocations and Supabase deletions. Safe to run on every replica at once:
// both steps are idempotent.
func (a *accounts) reconcile(ctx context.Context) {
	back, err := a.st.ResurrectedUsers(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "reconcile deletions", "err", err)
		return
	}
	for _, id := range back {
		if err := a.st.DeleteUser(ctx, id); err != nil {
			metrics.AccountDeletions.WithLabelValues("data", "error").Inc()
			a.log.WarnContext(ctx, "re-delete account", "deleted_user", id, "err", err)
			continue
		}
		metrics.AccountDeletions.WithLabelValues("data", "ok").Inc()
		a.log.InfoContext(ctx, "re-deleted account that reappeared", "deleted_user", id)
	}
	if a.apple != nil {
		apples, err := a.st.PendingAppleRevocations(ctx, 100)
		if err != nil {
			a.log.WarnContext(ctx, "pending apple revocations", "err", err)
		}
		for _, id := range apples {
			a.revokeApple(ctx, id)
		}
	}
	if a.admin == nil {
		return
	}
	pending, err := a.st.PendingAuthDeletions(ctx, 100)
	if err != nil {
		a.log.WarnContext(ctx, "pending auth deletions", "err", err)
		return
	}
	for _, id := range pending {
		a.deleteAuthUser(ctx, id)
	}
}

// reconcileEvery runs reconcile at start-up (right after a restore is when
// deleted accounts reappear) and then every interval.
func (a *accounts) reconcileEvery(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, every/2)
		a.reconcile(pass)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// AccountDeleted is the DELETE /v1/me response.
type AccountDeleted struct {
	Status          string `json:"status"`            // "deleted"
	AuthUserDeleted bool   `json:"auth_user_deleted"` // false: retried in the background
	// AppleTokenRevoked: null when no Sign in with Apple token was stored,
	// false when the revocation is retried in the background.
	AppleTokenRevoked *bool `json:"apple_token_revoked"`
}

// deleteMe erases the caller's account: every row now, then the Sign in with
// Apple grant, then the Supabase auth user. Repeating it is safe; the app
// signs out afterwards either way.
func (h *handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	// Finish even if the client gives up: half a deletion helps nobody.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), deleteTimeout)
	defer cancel()
	if err := h.st.DeleteUser(ctx, p.UserID); err != nil {
		metrics.AccountDeletions.WithLabelValues("data", "error").Inc()
		httpx.Fail(w, r, fmt.Errorf("delete account: %w", err))
		return
	}
	metrics.AccountDeletions.WithLabelValues("data", "ok").Inc()
	h.cache.forget(p.UserID)
	appleRevoked := h.accounts.revokeApple(ctx, p.UserID)
	authDeleted := h.accounts.deleteAuthUser(ctx, p.UserID)
	h.log.InfoContext(ctx, "account deleted", "auth_user_deleted", authDeleted, "apple_token_revoked", appleRevoked)
	httpx.WriteJSON(w, http.StatusOK, AccountDeleted{Status: "deleted", AuthUserDeleted: authDeleted, AppleTokenRevoked: appleRevoked})
}

// appleAuthorization is the POST /v1/me/apple-authorization body: the
// authorizationCode the app gets from Sign in with Apple, sent once after sign-in.
type appleAuthorization struct {
	Code string `json:"code"`
}

// appleAuthorizationTimeout bounds the exchange with Apple.
const appleAuthorizationTimeout = 15 * time.Second

// appleAuthorization exchanges the code for a refresh token and stores it
// encrypted, so that deleting the account can revoke the app's grant at Apple.
func (h *handler) appleAuthorization(w http.ResponseWriter, r *http.Request) {
	a := h.accounts
	if a.apple == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "apple token revocation is not configured")
		return
	}
	var req appleAuthorization
	if err := httpx.ReadJSON(w, r, &req, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 2048 {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "code is required")
		return
	}
	u := userOf(r)
	p, _ := auth.FromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), appleAuthorizationTimeout)
	defer cancel()
	tokens, err := a.apple.Exchange(ctx, code)
	switch {
	case errors.Is(err, apple.ErrInvalidGrant):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "authorization code invalid, expired or already used")
		return
	case errors.Is(err, apple.ErrInvalidClient):
		httpx.Fail(w, r, err) // our configuration: a 500, logged
		return
	case err != nil:
		h.log.WarnContext(ctx, "apple token exchange", "err", err)
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, http.StatusServiceUnavailable, "apple is not reachable, retry")
		return
	}
	if p.AppleSubject != "" && tokens.Subject != "" && tokens.Subject != p.AppleSubject {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "authorization code is for a different Apple ID")
		return
	}
	enc, err := a.key.Seal([]byte(tokens.RefreshToken), u.ID[:])
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := h.st.SaveAppleToken(ctx, u.ID, enc); err != nil {
		httpx.Fail(w, r, fmt.Errorf("save apple token: %w", err))
		return
	}
	h.log.InfoContext(ctx, "apple token stored")
	w.WriteHeader(http.StatusNoContent)
}
