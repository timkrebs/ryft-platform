package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
)

// smokeVerifier adds the smoke sign-in (D286) to the Supabase verifier: a Google ID token of
// one service account is the smoke user, and nobody else. The route is chosen from the
// token's unverified iss, which only decides who verifies it: each verifier checks the whole
// token itself, so a forged iss reaches a verifier that rejects it, never one that accepts it.
type smokeVerifier struct {
	supabase Verifier
	google   atomic.Pointer[GoogleOIDC] // nil until built: a smoke token is a 503 meanwhile
	user     uuid.UUID
}

// newSmoke wraps supabase. google must be built for the one smoke service account and audience.
func newSmoke(supabase Verifier, google *GoogleOIDC, user uuid.UUID) Verifier {
	s := &smokeVerifier{supabase: supabase, user: user}
	s.google.Store(google)
	return s
}

// newSmokeAsync wraps supabase and builds the Google verifier in a goroutine: its constructor
// reads Google's JWKS once, and a stalled endpoint must not hold up the service's start (D286).
// Until build has returned, a smoke token gets 503 smoke_jwks_unavailable; Supabase tokens never
// wait. A build error is logged once and leaves the smoke sign-in unavailable.
func newSmokeAsync(ctx context.Context, supabase Verifier, build func(context.Context) (*GoogleOIDC, error), user uuid.UUID) Verifier {
	s := &smokeVerifier{supabase: supabase, user: user}
	go func() {
		g, err := build(ctx)
		if err != nil {
			slog.Default().ErrorContext(ctx, "smoke sign-in unavailable: the Google verifier could not be built", "err", err)
			return
		}
		s.google.Store(g)
	}()
	return s
}

// Verify sends a token whose payload names a Google issuer to the Google verifier and
// returns the smoke Principal: the user ID and the Smoke mark, nothing else (no email or provider, so
// EnsureUser keeps what is stored; no operator role, assurance level or second factor). Any
// other token, including one whose payload cannot be read, goes to Supabase unchanged.
func (s *smokeVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	if !googleIssued(token) {
		return s.supabase.Verify(ctx, token)
	}
	g := s.google.Load()
	if g == nil {
		return Principal{}, Unavailable("smoke_jwks_unavailable", errors.New("the Google verifier is not built yet"))
	}
	if err := g.Verify(ctx, token); err != nil {
		var ae *Error
		if errors.As(err, &ae) {
			return Principal{}, &Error{Status: ae.Status, Reason: "smoke_" + ae.Reason, Err: ae.Err}
		}
		return Principal{}, Unauthorized("smoke_invalid", err)
	}
	return Principal{UserID: s.user, Smoke: true}, nil
}

// Ready is the Supabase verifier's: Google's keys being late must not make the api unready
// for the app's users. A smoke request meanwhile gets the 503 of GoogleOIDC.Verify.
func (s *smokeVerifier) Ready(ctx context.Context) error { return s.supabase.Ready(ctx) }

// googleIssued reports whether the token's unverified payload has a Google iss. Anything it
// cannot read is "no": Supabase rejects it as it does today.
func googleIssued(token string) bool {
	if len(token) > maxTokenLen {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var p struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	return slices.Contains(googleIssuers, p.Iss)
}
