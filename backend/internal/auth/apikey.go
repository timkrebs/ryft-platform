package auth

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/google/uuid"
)

// APIKey is the local-development verifier (AUTH_MODE=apikey, refused unless
// ALLOW_INSECURE_APIKEY_AUTH=true). The bearer is the shared key, which acts
// as the configured user, or "<key>:<uuid>" to act as any user — so tests can
// exercise several accounts against one compose stack.
type APIKey struct {
	key    []byte
	userID uuid.UUID
}

// NewAPIKey returns the local-development verifier: key is the shared secret and userID the
// user a bare key acts as. config.LoadAuth refuses this mode without
// ALLOW_INSECURE_APIKEY_AUTH.
func NewAPIKey(key string, userID uuid.UUID) *APIKey {
	return &APIKey{key: []byte(key), userID: userID}
}

// Verify accepts "<key>" (the default user) or "<key>:<uuid>" (that user; D32), comparing
// the key in constant time. Anything else is a 401.
func (a *APIKey) Verify(_ context.Context, token string) (Principal, error) {
	key, user, hasUser := strings.Cut(token, ":")
	if subtle.ConstantTimeCompare([]byte(key), a.key) != 1 {
		return Principal{}, Unauthorized("api_key", nil)
	}
	id := a.userID
	if hasUser {
		var err error
		if id, err = uuid.Parse(user); err != nil || id == uuid.Nil {
			return Principal{}, Unauthorized("subject", err)
		}
	}
	return Principal{UserID: id, Provider: "apikey"}, nil
}

// Ready is always nil: a shared key has nothing to fetch.
func (a *APIKey) Ready(context.Context) error { return nil }
