package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// gateOpen are the routes an account without beta access may use: enough to
// redeem a code, consent, read what it is, report a problem (API.md → Errors).
// DELETE /v1/me has its own mux and is never gated.
var gateOpen = map[string]bool{
	"GET /v1/me": true, "GET /v1/me/sync": true, "GET /v1/config": true, "GET /v1/model": true, "POST /v1/me/invite": true,
	"POST /v1/me/consent": true, "DELETE /v1/me/consent": true, "POST /v1/feedback": true,
}

// betaGate acts on accounts without beta access outside the open routes: on
// refuses them (403 invite_required), warn serves them with X-RYFT-Gate (D87).
// It runs after the user row is loaded.
func betaGate(mode config.Mode, next http.Handler) http.Handler {
	if mode == config.ModeOff {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gateOpen[r.Method+" "+r.URL.Path] || gate.Invite(w, r, mode, userOf(r), "api") {
			next.ServeHTTP(w, r)
		}
	})
}

func (h *handler) postConsent(w http.ResponseWriter, r *http.Request) {
	var p model.ConsentPost
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if p.PolicyVersion != h.beta.PrivacyPolicyVersion {
		metrics.Consents.WithLabelValues("stale").Inc()
		httpx.WriteError(w, 422, "policy_version must be the current one ("+h.beta.PrivacyPolicyVersion+")")
		return
	}
	if p.AppVersion != nil && len(*p.AppVersion) > 64 {
		httpx.WriteError(w, 422, "app_version has at most 64 characters")
		return
	}
	if err := h.st.GrantConsent(r.Context(), userOf(r).ID, p.PolicyVersion, p.AppVersion); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	metrics.Consents.WithLabelValues("granted").Inc()
	h.log.InfoContext(r.Context(), "consent granted", "policy_version", p.PolicyVersion)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteConsent(w http.ResponseWriter, r *http.Request) {
	if err := h.st.RevokeConsent(r.Context(), userOf(r).ID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	metrics.Consents.WithLabelValues("revoked").Inc()
	h.log.InfoContext(r.Context(), "consent revoked")
	w.WriteHeader(http.StatusNoContent)
}

// inviteEmails answers which email Supabase holds for an account and has confirmed: what a code bound to an
// email is compared with. "" when the account has none or it is not confirmed; an error when Supabase
// cannot be asked.
type inviteEmails interface {
	VerifiedEmail(ctx context.Context, id uuid.UUID) (string, error)
}

// VerifiedEmail is the account's email from the Supabase admin API, only when Supabase has confirmed it
// (email_confirmed_at). Not the token's email claim (a copy, possibly older), not users.email (a copy of that
// claim) and not user_metadata (the user writes it). Without the admin client (apikey mode, or no
// SUPABASE_SERVICE_ROLE_KEY) it answers "": a bound code then never redeems.
func (a *accounts) VerifiedEmail(ctx context.Context, id uuid.UUID) (string, error) {
	if a == nil || a.admin == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := a.admin.GetUser(ctx, id)
	switch {
	case errors.Is(err, auth.ErrNoAuthUser):
		return "", nil
	case err != nil:
		return "", err
	case info.EmailConfirmedAt == nil:
		return "", nil
	}
	return info.Email, nil
}

// redeemInvite is POST /v1/me/invite (D159). One 422 for every refusal. An account that has access gets 204 first
// and nothing is looked up or spent. Supabase is asked only for a code that exists, is open and is bound, and
// outside any transaction (no row lock is held across the call); if it cannot answer, 503 (the tester sends
// the same request again).
func (h *handler) redeemInvite(w http.ResponseWriter, r *http.Request) {
	var p model.InvitePost
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	u := userOf(r)
	var verified string
	if !u.BetaAccess {
		_, bound, err := h.st.BoundInviteEmail(r.Context(), p.Code)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if bound {
			if verified, err = h.verifiedEmail(r.Context(), u.ID); err != nil {
				// Not the error: it comes from the Supabase client and is for the operator's logs only.
				h.log.WarnContext(r.Context(), "invite email lookup failed", "err", err)
				w.Header().Set("Retry-After", "5")
				httpx.WriteError(w, http.StatusServiceUnavailable, "the invite could not be checked right now")
				return
			}
		}
	}
	used, err := h.st.RedeemInvite(r.Context(), u.ID, p.Code, verified)
	switch {
	case errors.Is(err, store.ErrInvalidCode):
		metrics.InviteRedemptions.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, "invalid or used code")
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	case used:
		metrics.InviteRedemptions.WithLabelValues("ok").Inc()
		h.log.InfoContext(r.Context(), "invite redeemed")
	default:
		metrics.InviteRedemptions.WithLabelValues("already").Inc()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) verifiedEmail(ctx context.Context, id uuid.UUID) (string, error) {
	if h.emails == nil {
		return "", nil
	}
	return h.emails.VerifiedEmail(ctx, id)
}

// purgeInviteEmailsAfter is how long after a code can no longer be redeemed its bound email is kept
// (D161; docs/API.md → Invites for a person; the privacy text names it once U3 is decided).
const purgeInviteEmailsAfter = 30 * 24 * time.Hour

// purgeInviteEmailsEvery clears the emails of codes that have not been redeemable for 30 days, once now and
// then every interval, until ctx ends. Idempotent, so every pod runs it; a failed pass is logged and retried
// at the next tick. A pass that clears nothing is normal and silent.
func purgeInviteEmailsEvery(ctx context.Context, st *store.Store, log *slog.Logger, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		n, err := st.PurgeInviteEmails(ctx, purgeInviteEmailsAfter)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.WarnContext(ctx, "invite email purge failed", "err", err)
		case n > 0:
			log.InfoContext(ctx, "invite emails purged", "n", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- admin: invite codes --------------------------------------------------------

// inviteStatus is the closed status of a code, the first that applies: revoked, used, expired, open.
func inviteStatus(i model.BetaInvite, now time.Time) string {
	switch {
	case i.RevokedAt != nil:
		return model.InviteRevoked
	case i.Uses >= i.MaxUses:
		return model.InviteUsed
	case i.ExpiresAt != nil && !i.ExpiresAt.After(now):
		return model.InviteExpired
	}
	return model.InviteOpen
}

// inviteView fills what the store does not (D160): status, link, and the emails as the caller asked for them
// (masked unless emails; the create answer passes true for the address the caller just sent). The store's
// slice is not shared with the caller.
func (a admin) inviteView(i model.BetaInvite, emails bool, now time.Time) model.BetaInvite {
	i.Status = inviteStatus(i, now)
	i.Link = strings.TrimRight(cmp.Or(a.h.webURL, config.DefaultPublicWebURL), "/") + "/invite/" + i.Code
	if i.Email != nil {
		i.Email = shown(*i.Email, emails)
	}
	uses := make([]model.InviteUse, 0, len(i.UsesBy))
	for _, u := range i.UsesBy {
		if u.Email != nil {
			u.PrivateRelay = isRelay(*u.Email)
			u.Email = shown(*u.Email, emails)
		}
		uses = append(uses, u)
	}
	i.UsesBy = uses
	return i
}

// createdBy is what beta_invite_codes.created_by stores for the caller of a create: "key:3fa1c2d9" or
// "user:<uuid>".
func createdBy(r *http.Request) string {
	act, ok := adminActorFrom(r.Context())
	switch {
	case ok && !act.isKey():
		return model.ActorUser + ":" + act.user.String()
	case ok:
		return model.ActorKey + ":" + act.fingerprint
	}
	return model.ActorKey // unreachable behind adminAuth; the column's default form
}

func (a admin) createInvite(w http.ResponseWriter, r *http.Request) {
	var in model.BetaInviteInput
	if err := httpx.ReadJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteDecodeError(w, err)
		return
	}
	maxUses := 1
	if in.MaxUses != nil {
		maxUses = *in.MaxUses
	}
	var email *string
	if in.Email != nil {
		e, ok := store.ParseInviteEmail(*in.Email)
		if !ok {
			metrics.AdminRequests.WithLabelValues("invalid").Inc()
			httpx.WriteError(w, 422, "email is not a valid address")
			return
		}
		email = &e
	}
	msg := ""
	switch {
	case maxUses < 1 || maxUses > 1000:
		msg = "max_uses must be 1-1000"
	case len(in.Note) > 200:
		msg = "note has at most 200 characters"
	case in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()):
		msg = "expires_at must be in the future"
	case email != nil && maxUses != 1:
		msg = "a code for one email has max_uses 1"
	}
	if msg != "" {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, msg)
		return
	}
	inv, err := a.h.st.CreateInvite(r.Context(), store.NewInvite{Note: strings.TrimSpace(in.Note), MaxUses: maxUses,
		ExpiresAt: in.ExpiresAt, Email: email, CreatedBy: createdBy(r)})
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	// The code goes into the audit row (the database only, for the operator); never the address.
	auditDetail(r, "code", inv.Code)
	// Not the note or the address: they may name the person the code is for.
	a.info(r, "invite created", "max_uses", inv.MaxUses, "bound", email != nil)
	// The caller just sent the address: it is shown as stored, not masked.
	httpx.WriteJSON(w, 201, a.inviteView(inv, true, time.Now()))
}

// invites is GET /v1/admin/invites[?emails=1]: every code, with who redeemed it. Emails are masked unless
// asked for, whichever bearer asks; the call is audited with whether they were.
func (a admin) invites(w http.ResponseWriter, r *http.Request) {
	emails := r.URL.Query().Get("emails") == "1"
	list, err := a.h.st.Invites(r.Context())
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	now := time.Now()
	for k := range list {
		list[k] = a.inviteView(list[k], emails, now)
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	auditDetail(r, "emails", emails)
	a.info(r, "admin invites read", "emails", emails, "n", len(list))
	httpx.WriteJSON(w, 200, list)
}

// revokeInvite is DELETE /v1/admin/invites/{code}: the code stays listed and redeems nothing any more.
// 204 also when it was revoked already; 404 for a code that does not exist.
func (a admin) revokeInvite(w http.ResponseWriter, r *http.Request) {
	err := a.h.st.RevokeInvite(r.Context(), r.PathValue("code"))
	if errors.Is(err, store.ErrNotFound) {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 404, "no such code")
		return
	}
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	auditDetail(r, "code", store.ShowInvite(store.NormalizeInvite(r.PathValue("code"))))
	a.info(r, "invite revoked")
	w.WriteHeader(http.StatusNoContent)
}
