package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/store"
)

// deleteTimeout bounds one account deletion. A large account is a few million
// sample rows; the deletion runs to completion even if the client hangs up.
const deleteTimeout = 2 * time.Minute

// reconcileEvery is how often each api pod retries Supabase deletions that
// failed and erases accounts that reappeared (e.g. after a restore).
const reconcileEvery = 15 * time.Minute

// accounts deletes accounts: our rows (store) and the Supabase auth user
// (admin; nil in apikey mode or without SUPABASE_SERVICE_ROLE_KEY).
type accounts struct {
	st    *store.Store
	admin *auth.Admin
	log   *slog.Logger
}

func newAccounts(st *store.Store, log *slog.Logger, c config.Auth) (*accounts, error) {
	a := &accounts{st: st, log: log}
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

// reconcile erases accounts that have rows again despite their tombstone and
// retries pending Supabase deletions. Safe to run on every replica at once:
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
}

// deleteMe erases the caller's account: every row now, then the Supabase auth
// user. Repeating it is safe; the app signs out afterwards either way.
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
	authDeleted := h.accounts.deleteAuthUser(ctx, p.UserID)
	h.log.InfoContext(ctx, "account deleted", "auth_user_deleted", authDeleted)
	httpx.WriteJSON(w, http.StatusOK, AccountDeleted{Status: "deleted", AuthUserDeleted: authDeleted})
}
