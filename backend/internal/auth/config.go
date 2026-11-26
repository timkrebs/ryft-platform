package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/timkrebs/ryft/internal/config"
)

// FromConfig builds the verifier AUTH_MODE asks for. The Supabase verifier's
// JWKS refresher runs until ctx ends. With the smoke sign-in configured (D286) it is wrapped
// so that the smoke service account's Google ID token also verifies, as the smoke user.
func FromConfig(ctx context.Context, c config.Auth) (Verifier, error) {
	return fromConfig(ctx, c, "")
}

// fromConfig is FromConfig with the smoke verifier's JWKS URL (tests; "" is Google's). The smoke
// Google verifier is built in the background (newSmokeAsync): FromConfig never waits for Google.
func fromConfig(ctx context.Context, c config.Auth, googleJWKS string) (Verifier, error) {
	switch c.Mode {
	case config.AuthSupabase:
		sb, err := NewSupabase(ctx, SupabaseOptions{Issuer: c.Issuer, JWKSURL: c.JWKSURL})
		if err != nil {
			return nil, err
		}
		if !c.SmokeOn() {
			return sb, nil
		}
		o := GoogleOIDCOptions{Audience: c.SmokeAudience, Email: c.SmokeSAEmail, Subject: c.SmokeSAID, JWKSURL: googleJWKS}
		if o.Audience == "" || o.Email == "" || o.Subject == "" {
			return nil, errors.New("auth: the smoke audience, service account email and unique ID are required") // fail at start, not in the background
		}
		return newSmokeAsync(ctx, sb, func(ctx context.Context) (*GoogleOIDC, error) { return NewGoogleOIDC(ctx, o) }, c.SmokeUserID), nil
	case config.AuthAPIKey:
		return NewAPIKey(c.APIKey, c.APIKeyUserID), nil
	default:
		return nil, fmt.Errorf("auth: unknown mode %q", c.Mode)
	}
}
