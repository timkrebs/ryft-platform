package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Admin calls the Supabase Auth admin API with the project's secret key. Only
// the api service holds that key (account deletion).
type Admin struct {
	base   string // Supabase Auth URL, the token issuer: https://<ref>.supabase.co/auth/v1
	key    string
	jwt    bool // legacy service_role key: also sent as the bearer
	client *http.Client
}

// NewAdmin checks the key's kind: a secret key (sb_secret_…) goes in the
// apikey header only; a legacy service_role JWT also as Authorization. Anything
// else (a publishable or anon key) is refused, since it could not delete users.
func NewAdmin(base, key string, client *http.Client) (*Admin, error) {
	key = strings.TrimSpace(key)
	a := &Admin{base: strings.TrimRight(base, "/"), key: key, client: client}
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	switch role, isJWT := jwtRole(key); {
	case strings.HasPrefix(key, "sb_secret_"):
	case isJWT && role == "service_role":
		a.jwt = true
	case isJWT:
		return nil, fmt.Errorf("SUPABASE_SERVICE_ROLE_KEY is a %q key; use the secret (service_role) key", role)
	default:
		return nil, fmt.Errorf("SUPABASE_SERVICE_ROLE_KEY must be a secret key (sb_secret_…) or the legacy service_role JWT")
	}
	return a, nil
}

// jwtRole returns the unverified role claim of a JWT-shaped key.
func jwtRole(key string) (string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var c struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return "", false
	}
	return c.Role, true
}

// DeleteUser deletes the auth user for good (with its identities and
// sessions, so the Apple sign-in starts from scratch). A user that does not
// exist counts as deleted, which makes retries safe.
func (a *Admin) DeleteUser(ctx context.Context, id uuid.UUID) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.base+"/admin/users/"+id.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", a.key)
	if a.jwt {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("supabase admin: delete user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("supabase admin: delete user: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
