package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx"
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
	case strings.Contains(key, "change-me"):
		return nil, fmt.Errorf("SUPABASE_SERVICE_ROLE_KEY is still a placeholder (change-me)")
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

// AdminUserInfo is what the Supabase admin API says about a user: how they
// sign in. Emails as Supabase holds them; the caller masks them.
type AdminUserInfo struct {
	Email        string
	LastSignInAt *time.Time
	Identities   []AdminIdentity
}

// AdminIdentity is one way into the account (an Apple id, an email password).
type AdminIdentity struct {
	Provider     string
	Email        string
	LastSignInAt *time.Time
}

// GetUser reads the auth user: email, last sign-in and identities. ErrNoAuthUser
// when Supabase has none with that id.
func (a *Admin) GetUser(ctx context.Context, id uuid.UUID) (AdminUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/admin/users/"+id.String(), nil)
	if err != nil {
		return AdminUserInfo{}, err
	}
	req.Header.Set("apikey", a.key)
	if a.jwt {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return AdminUserInfo{}, fmt.Errorf("supabase admin: get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return AdminUserInfo{}, ErrNoAuthUser
	}
	if resp.StatusCode != http.StatusOK {
		return AdminUserInfo{}, adminError("get user", resp)
	}
	var u struct {
		Email        string     `json:"email"`
		LastSignInAt *time.Time `json:"last_sign_in_at"`
		Identities   []struct {
			Provider     string         `json:"provider"`
			IdentityData map[string]any `json:"identity_data"`
			LastSignInAt *time.Time     `json:"last_sign_in_at"`
		} `json:"identities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		// Not %w: a decode error quotes the value ("parsing time "tim@..."").
		return AdminUserInfo{}, errors.New("supabase admin: get user: undecodable response")
	}
	out := AdminUserInfo{Email: u.Email, LastSignInAt: u.LastSignInAt}
	for _, i := range u.Identities {
		email, _ := i.IdentityData["email"].(string)
		out.Identities = append(out.Identities, AdminIdentity{Provider: i.Provider, Email: email, LastSignInAt: i.LastSignInAt})
	}
	return out, nil
}

// ErrNoAuthUser: Supabase has no auth user with that id.
var ErrNoAuthUser = errors.New("supabase admin: no such user")

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
	return adminError("delete user", resp)
}

// adminError is a refused admin call as a log line may hold it (the api logs
// it, the admin view shows it): the status and GoTrue's error_code, never the
// body, whose msg is free text.
func adminError(op string, resp *http.Response) error {
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	code := body.ErrorCode
	if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz_0123456789") != "" {
		code = "no error_code"
	}
	// The status is the code and our own reason phrase: resp.Status echoes the
	// server's status line, whose text is as third-party as the body.
	return fmt.Errorf("supabase admin: %s: %s: %s", op, logx.StatusLine(resp.StatusCode), code)
}
