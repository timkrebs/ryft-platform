// Package auth authenticates /v1 requests. In production the bearer is a
// Supabase access token (a JWT) verified against the project's JWKS; for local
// development a shared API key can stand in (AUTH_MODE=apikey). The verified
// Principal — and whatever OnAuth adds, such as the user row — travels in the
// request context.
//
// Status codes matter to the app: it refreshes its token once on a 401 and
// signs the user out if the retry fails too. So 401 means only "this token is
// not acceptable"; anything that stops us from checking a token (JWKS
// unreachable, database down) is a 503 the app simply retries.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Principal is the authenticated caller.
type Principal struct {
	UserID   uuid.UUID
	Email    string // empty when the token has none
	Provider string // sign-in provider, e.g. "apple"; "apikey" in apikey mode
	Name     string // user_metadata.full_name (the app stores the Apple name there)
}

type principalKey struct{}
type userKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the request's Principal; ok is false outside Middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithUser stores the caller's user row (see OnAuth).
func WithUser(ctx context.Context, u model.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the caller's user row stored by OnAuth.
func UserFrom(ctx context.Context) (model.User, bool) {
	u, ok := ctx.Value(userKey{}).(model.User)
	return u, ok
}

// Error is a failed authentication with the status to answer and a short,
// bounded reason for metrics and logs (never the token itself).
type Error struct {
	Status int
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

// Unauthorized rejects the token (401): the client should refresh it.
func Unauthorized(reason string, err error) *Error {
	return &Error{Status: http.StatusUnauthorized, Reason: reason, Err: err}
}

// Unavailable means the token could not be checked right now (503): retry.
func Unavailable(reason string, err error) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Reason: reason, Err: err}
}

// Verifier checks a bearer token.
type Verifier interface {
	// Verify returns the principal, or an *Error.
	Verify(ctx context.Context, token string) (Principal, error)
	// Ready reports whether tokens can be verified (for /readyz).
	Ready(ctx context.Context) error
}

// OnAuth runs after a token is verified, typically to load or create the user
// row; the returned context is what handlers see. An *Error picks the status,
// any other error is a 503.
type OnAuth func(ctx context.Context, p Principal) (context.Context, error)

// Options configure Middleware.
type Options struct {
	Log      *slog.Logger
	Verifier Verifier
	OnAuth   OnAuth // optional
}

// maxTokenLen bounds the work an attacker can make us do per request.
const maxTokenLen = 8 << 10

// Middleware authenticates every request it wraps.
func Middleware(o Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		token, ok := bearer(r)
		if !ok {
			fail(w, r, o.Log, Unauthorized("missing_token", nil))
			return
		}
		if len(token) > maxTokenLen {
			fail(w, r, o.Log, Unauthorized("malformed", nil))
			return
		}
		p, err := o.Verifier.Verify(ctx, token)
		if err != nil {
			fail(w, r, o.Log, err)
			return
		}
		if ri := httpx.Info(ctx); ri != nil {
			ri.UserID = p.UserID.String()
		}
		ctx = WithPrincipal(ctx, p)
		if o.OnAuth != nil {
			if ctx, err = o.OnAuth(ctx, p); err != nil {
				fail(w, r, o.Log, err)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearer extracts the token from "Authorization: Bearer <token>" (the scheme
// is case-insensitive per RFC 7235).
func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		ae = Unavailable("internal", err)
	}
	metrics.AuthFailures.WithLabelValues(ae.Reason).Inc()
	if ae.Status == http.StatusUnauthorized {
		log.InfoContext(r.Context(), "auth rejected", "reason", ae.Reason, "err", errString(ae.Err))
		w.Header().Set("WWW-Authenticate", `Bearer realm="ryft", error="invalid_token"`)
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	log.WarnContext(r.Context(), "auth unavailable", "reason", ae.Reason, "err", errString(ae.Err))
	w.Header().Set("Retry-After", "5")
	httpx.WriteError(w, ae.Status, "authentication temporarily unavailable")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// LoadUser adapts a "find or create the user row" function into an OnAuth
// that stores the row in the context (see UserFrom). Its errors are 503s
// unless it returns an *Error.
func LoadUser(load func(ctx context.Context, p Principal) (model.User, error)) OnAuth {
	return func(ctx context.Context, p Principal) (context.Context, error) {
		u, err := load(ctx, p)
		if err != nil {
			return ctx, err
		}
		return WithUser(ctx, u), nil
	}
}
