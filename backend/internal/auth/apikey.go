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

func NewAPIKey(key string, userID uuid.UUID) *APIKey {
	return &APIKey{key: []byte(key), userID: userID}
}

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

func (a *APIKey) Ready(context.Context) error { return nil }
