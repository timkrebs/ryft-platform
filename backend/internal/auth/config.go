package auth

import (
	"context"
	"fmt"

	"github.com/timkrebs/ryft/internal/config"
)

// FromConfig builds the verifier AUTH_MODE asks for. The Supabase verifier's
// JWKS refresher runs until ctx ends.
func FromConfig(ctx context.Context, c config.Auth) (Verifier, error) {
	switch c.Mode {
	case config.AuthSupabase:
		return NewSupabase(ctx, SupabaseOptions{Issuer: c.Issuer, JWKSURL: c.JWKSURL})
	case config.AuthAPIKey:
		return NewAPIKey(c.APIKey, c.APIKeyUserID), nil
	default:
		return nil, fmt.Errorf("auth: unknown mode %q", c.Mode)
	}
}
