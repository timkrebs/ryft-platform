package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// smokeVerifier adds the smoke sign-in (D286) to the Supabase verifier: a Google ID token of
// one service account is the smoke user, and nobody else. The route is chosen from the
// token's unverified iss, which only decides who verifies it: each verifier checks the whole
// token itself, so a forged iss reaches a verifier that rejects it, never one that accepts it.
type smokeVerifier struct {
	supabase Verifier
	google   *GoogleOIDC
	user     uuid.UUID
}

// newSmoke wraps supabase. google must be built for the one smoke service account and audience.
func newSmoke(supabase Verifier, google *GoogleOIDC, user uuid.UUID) Verifier {
	return &smokeVerifier{supabase: supabase, google: google, user: user}
}

// Verify sends a token whose payload names a Google issuer to the Google verifier and
// returns the smoke Principal: the user ID and the Smoke mark, nothing else (no email or provider, so
// EnsureUser keeps what is stored; no operator role, assurance level or second factor). Any
// other token, including one whose payload cannot be read, goes to Supabase unchanged.
func (s *smokeVerifier) Verify(ctx context.Context, token string) (Principal, error) {
	if !googleIssued(token) {
		return s.supabase.Verify(ctx, token)
	}
	if err := s.google.Verify(ctx, token); err != nil {
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
