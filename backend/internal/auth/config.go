package auth

import (
	"context"
	"fmt"

	"github.com/timkrebs/ryft/internal/config"
)

// FromConfig builds the verifier AUTH_MODE asks for. The Supabase verifier's
// JWKS refresher runs until ctx ends. With the smoke sign-in configured (D286) it is wrapped
// so that the smoke service account's Google ID token also verifies, as the smoke user.
func FromConfig(ctx context.Context, c config.Auth) (Verifier, error) {
	switch c.Mode {
	case config.AuthSupabase:
		sb, err := NewSupabase(ctx, SupabaseOptions{Issuer: c.Issuer, JWKSURL: c.JWKSURL})
		if err != nil {
			return nil, err
		}
		if !c.SmokeOn() {
			return sb, nil
		}
		g, err := NewGoogleOIDC(ctx, GoogleOIDCOptions{Audience: c.SmokeAudience, Email: c.SmokeSAEmail, Subject: c.SmokeSAID})
		if err != nil {
			return nil, err
		}
		return newSmoke(sb, g, c.SmokeUserID), nil
	case config.AuthAPIKey:
		return NewAPIKey(c.APIKey, c.APIKeyUserID), nil
	default:
		return nil, fmt.Errorf("auth: unknown mode %q", c.Mode)
	}
}
