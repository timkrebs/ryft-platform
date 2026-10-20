package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// gateOpen are the routes an account without beta access may use: enough to
// redeem a code, consent, read what it is, report a problem (API.md → Errors).
// DELETE /v1/me has its own mux and is never gated.
var gateOpen = map[string]bool{
	"GET /v1/me": true, "GET /v1/config": true, "GET /v1/model": true, "POST /v1/me/invite": true,
	"POST /v1/me/consent": true, "DELETE /v1/me/consent": true, "POST /v1/feedback": true,
}

// betaGate refuses accounts without beta access (403 invite_required) while
// the gate is on. It runs after the user row is loaded.
func betaGate(on bool, next http.Handler) http.Handler {
	if !on {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !userOf(r).BetaAccess && !gateOpen[r.Method+" "+r.URL.Path] {
			metrics.BetaDenied.WithLabelValues(model.CodeInviteRequired, "api").Inc()
			httpx.WriteCodedError(w, http.StatusForbidden, model.CodeInviteRequired, "this beta needs an invite code")
			return
		}
		next.ServeHTTP(w, r)
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

func (h *handler) redeemInvite(w http.ResponseWriter, r *http.Request) {
	var p model.InvitePost
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	used, err := h.st.RedeemInvite(r.Context(), userOf(r).ID, p.Code)
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

// ---- admin: invite codes --------------------------------------------------------

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
	msg := ""
	switch {
	case maxUses < 1 || maxUses > 1000:
		msg = "max_uses must be 1-1000"
	case len(in.Note) > 200:
		msg = "note has at most 200 characters"
	case in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()):
		msg = "expires_at must be in the future"
	}
	if msg != "" {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, msg)
		return
	}
	inv, err := a.h.st.CreateInvite(r.Context(), strings.TrimSpace(in.Note), maxUses, in.ExpiresAt)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(r.Context(), "invite created", "note", inv.Note, "max_uses", inv.MaxUses, "admin_key", a.keyID)
	httpx.WriteJSON(w, 201, inv)
}

func (a admin) invites(w http.ResponseWriter, r *http.Request) {
	list, err := a.h.st.Invites(r.Context())
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	httpx.WriteJSON(w, 200, list)
}

func (a admin) deleteInvite(w http.ResponseWriter, r *http.Request) {
	err := a.h.st.DeleteInvite(r.Context(), r.PathValue("code"))
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
	a.h.log.InfoContext(r.Context(), "invite deleted", "admin_key", a.keyID)
	w.WriteHeader(http.StatusNoContent)
}
