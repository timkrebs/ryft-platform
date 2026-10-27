// Package gate is how a service acts on an account a gate would refuse (D87): the
// beta invite gate (BETA_GATE) and the consent to the current privacy policy
// (CONSENT_ENFORCE, D78). ingest's POST /v1/sync and the api's POST
// /v1/train/import share the consent rule (D129); the api's invite gate and
// GET /v1/me/sync use the same pieces.
package gate

import (
	"net/http"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Consented says whether the user has consented to the policy version in force. The
// user row, consent included, is read fresh for every request, so a withdrawal counts
// at once.
func Consented(b config.Beta, u model.User) bool {
	return u.Consent != nil && u.Consent.PolicyVersion == b.PrivacyPolicyVersion
}

// Apply runs one gate in mode for a request it would refuse when failed: off, or not
// failed, passes; warn passes with the code in X-RYFT-Gate; on answers 403 with the code
// and stops. Every refusal and warning is counted for service (ryft_gate_refusals_total).
func Apply(w http.ResponseWriter, r *http.Request, mode config.Mode, failed bool, code, msg, service string) bool {
	if mode == config.ModeOff || !failed {
		return true
	}
	metrics.GateRefusals.WithLabelValues(code, string(mode), service).Inc()
	if mode == config.ModeWarn {
		w.Header().Add(httpx.GateHeader, code)
		return true
	}
	httpx.WriteCodedError(w, r, http.StatusForbidden, code, msg)
	return false
}

// Consent is the consent gate of a request that uploads health data (a sync, an import):
// 403 consent_required under CONSENT_ENFORCE=on, X-RYFT-Gate under warn. Run it before the
// body is read, so a refused upload costs no parse.
func Consent(w http.ResponseWriter, r *http.Request, b config.Beta, u model.User, service string) bool {
	return Apply(w, r, b.Consent, !Consented(b, u), model.CodeConsentRequired,
		"consent to the privacy policy "+b.PrivacyPolicyVersion+" is required before uploading health data", service)
}

// Invite is the beta gate: 403 invite_required under BETA_GATE=on for an account without
// beta access, X-RYFT-Gate under warn.
func Invite(w http.ResponseWriter, r *http.Request, b config.Mode, u model.User, service string) bool {
	return Apply(w, r, b, !u.BetaAccess, model.CodeInviteRequired, "this beta needs an invite code", service)
}
