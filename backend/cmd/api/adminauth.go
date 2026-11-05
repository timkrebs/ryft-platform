package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// /v1/admin/ takes two bearers: ADMIN_API_KEY (scripts, smoke), compared first and in constant time,
// or the Supabase access token of an allow-listed operator with a recent second factor (the
// operator's web page). The order, the answers and why are in docs/API.md → Admin routes.

const (
	// adminMFAMaxAge is how old the token's TOTP may be: a Supabase session never expires by itself,
	// so this bounds what a stolen refresh token is worth without the second factor.
	adminMFAMaxAge = 12 * time.Hour
	// adminMFASkew is how far in the future the TOTP time may lie (clock skew, as the verifier's leeway).
	adminMFASkew = 30 * time.Second
	// adminRole is the app_metadata.role of an operator.
	adminRole = "admin"
	// adminMaxToken bounds the work a bearer can cost, as auth.Middleware does.
	adminMaxToken = 8 << 10
)

// adminOptions is what adminRoutes needs besides the handler.
type adminOptions struct {
	Key      string        // ADMIN_API_KEY
	Verifier auth.Verifier // verifies a Supabase access token; nil: only the key is accepted
	Accounts adminAccounts // the allow-list; default the handler's store
	Origins  []string      // ADMIN_WEB_ORIGINS: the pages that may read the answers; empty: none
	Timeout  time.Duration // of the routes' work
	// Per-address limit, per pod: 10/s with bursts of 20. A test passes its own.
	Limiter *ratelimit.Limiter
	Now     func() time.Time // default time.Now
}

// adminActor is who an authorized admin call is made by: the key (named by its fingerprint) or an
// admin's Supabase account.
type adminActor struct {
	fingerprint string    // of the key; "" for a user
	user        uuid.UUID // of the operator's account; Nil for the key
}

func (a adminActor) isKey() bool { return a.user == uuid.Nil }

func (a adminActor) kind() string {
	if a.isKey() {
		return model.ActorKey
	}
	return model.ActorUser
}

// label names the actor in logs: "key:3fa1c2d9" or "user:1b2c3d4e" (the first 8 hex digits, as user_id's
// short form elsewhere).
func (a adminActor) label() string {
	if a.isKey() {
		return a.kind() + ":" + a.fingerprint
	}
	return a.kind() + ":" + a.user.String()[:8]
}

type adminActorKey struct{}

func adminActorFrom(ctx context.Context) (adminActor, bool) {
	a, ok := ctx.Value(adminActorKey{}).(adminActor)
	return a, ok
}

// adminLogAttrs are the attributes every admin log line carries: admin_actor, and admin_key on the key's
// lines (queries on it predate the token path).
func adminLogAttrs(ctx context.Context) []any {
	a, ok := adminActorFrom(ctx)
	switch {
	case !ok:
		return nil
	case a.isKey():
		return []any{"admin_actor", a.label(), "admin_key", a.fingerprint}
	}
	return []any{"admin_actor", a.label()}
}

// info logs one admin line with the actor's attributes.
func (a admin) info(r *http.Request, msg string, kv ...any) {
	a.h.log.InfoContext(r.Context(), msg, append(kv, adminLogAttrs(r.Context())...)...)
}

// adminAccounts answers whether a user may use an admin token (store.IsAdminAccount).
type adminAccounts interface {
	IsAdminAccount(ctx context.Context, user uuid.UUID) (bool, error)
}

type adminAuth struct {
	keySum      [sha256.Size]byte
	fingerprint string
	verifier    auth.Verifier
	accounts    adminAccounts
	log         *slog.Logger
	limiter     *ratelimit.Limiter
	now         func() time.Time
}

func newAdminAuth(h *handler, o adminOptions) adminAuth {
	a := adminAuth{keySum: sha256.Sum256([]byte(o.Key)), fingerprint: keyFingerprint(o.Key), verifier: o.Verifier,
		accounts: o.Accounts, log: h.log, limiter: o.Limiter, now: o.Now}
	if a.accounts == nil {
		a.accounts = h.st
	}
	// Create the series at 0 now: increase() over a counter that is born at 1 sees no increase, so the
	// first use of a pod's life would never fire AdminWebUsed (a use you did not make is the signal).
	for _, kind := range []string{model.ActorKey, model.ActorUser} {
		metrics.AdminAuthorized.WithLabelValues(kind)
	}
	for _, result := range []string{"ok", "invalid", "error", "unauthorized", "mfa_required", "forbidden"} {
		metrics.AdminRequests.WithLabelValues(result)
	}
	if a.limiter == nil {
		a.limiter = ratelimit.New(10, 20)
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

// limit is the per-address limit in front of the bearer check, so a flood of bad credentials costs no
// token verification and no database call. The address (httpx.ClientIP) is only a map key: it is never
// logged and never a metric label.
func (a adminAuth) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := a.limiter.Allow(httpx.ClientIP(r)); !ok {
			metrics.RateLimited.WithLabelValues("admin_ip").Inc()
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			httpx.WriteError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// require lets a request through with the key or an operator's token, and answers everything else:
// one identical 401 for every bearer that is not accepted (so the answer is no oracle for the key or
// for who is an operator), 403 mfa_required only for a real operator, 503 when a token cannot be
// checked now. The reason of a 401 is in the log and on the access line's code, never in the answer.
func (a adminAuth) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth.Bearer(r)
		if !ok {
			a.reject(w, r, "missing")
			return
		}
		// The key first, whatever the bearer looks like. Hashing both sides gives ConstantTimeCompare
		// equal lengths, so the key's length is not a side channel either.
		if sum := sha256.Sum256([]byte(token)); subtle.ConstantTimeCompare(sum[:], a.keySum[:]) == 1 {
			a.pass(w, r, next, adminActor{fingerprint: a.fingerprint})
			return
		}
		if a.verifier == nil || len(token) > adminMaxToken || strings.Count(token, ".") != 2 {
			a.reject(w, r, "not_a_token")
			return
		}
		p, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			var ae *auth.Error
			if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
				a.reject(w, r, ae.Reason)
				return
			}
			reason := "internal"
			if ae != nil {
				reason = ae.Reason
			}
			a.unavailable(w, r, reason)
			return
		}
		if p.AppRole != adminRole {
			a.reject(w, r, "not_admin")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		allowed, err := a.accounts.IsAdminAccount(ctx, p.UserID)
		cancel()
		if err != nil {
			a.unavailable(w, r, "account_check")
			return
		}
		if !allowed {
			a.reject(w, r, "not_allowed")
			return
		}
		// Only now, for a real operator: the second factor. An app user's token, or an operator's
		// without a row, never sees mfa_required.
		if reason := a.mfaProblem(p); reason != "" {
			metrics.AdminRequests.WithLabelValues("mfa_required").Inc()
			a.log.InfoContext(r.Context(), "admin second factor required", "reason", reason, "admin_actor", model.ActorUser+":"+p.UserID.String()[:8])
			httpx.WriteCodedError(w, r, http.StatusForbidden, model.CodeMFARequired, "a second factor confirmed in the last 12 hours is required")
			return
		}
		if ri := httpx.Info(r.Context()); ri != nil {
			ri.UserID = p.UserID.String()
		}
		a.pass(w, r, next, adminActor{user: p.UserID})
	})
}

// mfaProblem is why the token's second factor does not count: aal must be exactly aal2, and a totp entry
// of amr must be at most 12 hours old and not more than 30 seconds in the future.
func (a adminAuth) mfaProblem(p auth.Principal) string {
	now := a.now()
	switch {
	case p.AAL != "aal2" || p.MFAAt.IsZero():
		return "mfa_missing"
	case p.MFAAt.Before(now.Add(-adminMFAMaxAge)) || p.MFAAt.After(now.Add(adminMFASkew)):
		return "mfa_stale"
	}
	return ""
}

func (a adminAuth) pass(w http.ResponseWriter, r *http.Request, next http.Handler, act adminActor) {
	metrics.AdminAuthorized.WithLabelValues(act.kind()).Inc()
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminActorKey{}, act)))
}

func (a adminAuth) reject(w http.ResponseWriter, r *http.Request, reason string) {
	metrics.AdminRequests.WithLabelValues("unauthorized").Inc()
	httpx.SetCode(r, reason)
	a.log.InfoContext(r.Context(), "admin auth rejected", "reason", reason)
	w.Header().Set("WWW-Authenticate", `Bearer realm="ryft-admin"`)
	httpx.WriteError(w, http.StatusUnauthorized, "admin key or admin sign-in required")
}

// unavailable is the 503 for a token that cannot be checked now (the signing keys or the database):
// never a sign-out, and the key path does not depend on it.
func (a adminAuth) unavailable(w http.ResponseWriter, r *http.Request, reason string) {
	metrics.AdminRequests.WithLabelValues("error").Inc()
	metrics.AuthFailures.WithLabelValues(reason).Inc()
	httpx.SetCode(r, reason)
	a.log.WarnContext(r.Context(), "admin auth unavailable", "reason", reason)
	w.Header().Set("Retry-After", "5")
	httpx.WriteError(w, http.StatusServiceUnavailable, "authentication temporarily unavailable")
}

// keyOnly refuses a token on a route that needs the key (destructive or beyond what the page uses):
// 403 without a code. It runs inside audited, so the attempt is recorded, and an unknown actor is refused.
func (a admin) keyOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if act, ok := adminActorFrom(r.Context()); ok && act.isKey() {
			h(w, r)
			return
		}
		metrics.AdminRequests.WithLabelValues("forbidden").Inc()
		httpx.SetCode(r, "key_only")
		a.info(r, "admin key-only route refused")
		httpx.WriteError(w, http.StatusForbidden, "this route needs the admin key")
	}
}

// adminRoute is a route of /v1/admin with its class: key-only routes answer a token with 403.
type adminRoute struct {
	pattern string
	keyOnly bool
	handler http.HandlerFunc
}

// routes is every /v1/admin route and who may call it. A test pins this classification, so a route
// cannot be added without saying which bearers reach it.
func (a admin) routes() []adminRoute {
	return []adminRoute{
		{"GET /v1/admin/flags", false, a.flags},
		{"PUT /v1/admin/flags/{key}", true, a.putFlag},
		{"DELETE /v1/admin/flags/{key}", true, a.deleteFlag},
		{"POST /v1/admin/invites", false, a.createInvite},
		{"GET /v1/admin/invites", false, a.invites},
		{"DELETE /v1/admin/invites/{code}", false, a.deleteInvite},
		{"GET /v1/admin/feedback", false, a.feedback},
		{"POST /v1/admin/feedback/{id}/resolve", false, a.resolveFeedback},
		{"GET /v1/admin/users", false, a.users},
		{"GET /v1/admin/users/{id}", true, a.userDetail},
		{"POST /v1/admin/users/{id}/access", false, a.grantAccess},
		{"DELETE /v1/admin/users/{id}/access", false, a.revokeAccess},
		{"POST /v1/admin/users/{id}/merge-into/{to}", true, a.merge},
		{"POST /v1/admin/users/{id}/resync-hint", true, a.resyncHint},
		{"GET /v1/admin/audit", false, a.audit},
	}
}

// adminRoutes is /v1/admin/: CORS outermost (so a refusal is readable by the page, and a preflight never
// reaches anything below), the per-address limit, the bearer check, the audit around every authorized
// call, the timeout and the routes.
func adminRoutes(h *handler, o adminOptions) http.Handler {
	a, adm := admin{h: h}, http.NewServeMux()
	for _, r := range a.routes() {
		if r.keyOnly {
			adm.HandleFunc(r.pattern, a.keyOnly(r.handler))
		} else {
			adm.HandleFunc(r.pattern, r.handler)
		}
	}
	adm.HandleFunc("/v1/admin/", httpx.NotFound)
	au := newAdminAuth(h, o)
	inner := au.limit(au.require(a.audited(httpx.Timeout(o.Timeout, httpx.WithRoutes(adm)))))
	return httpx.CORS{Origins: o.Origins, Methods: []string{http.MethodGet, http.MethodPost, http.MethodDelete},
		Headers: []string{"Authorization", "Content-Type"}, Expose: []string{httpx.RequestIDHeader, "Retry-After"},
		MaxAge: 10 * time.Minute}.Wrap(inner)
}
