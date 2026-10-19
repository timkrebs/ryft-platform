// Package apple talks to Sign in with Apple's REST API. It exchanges the
// authorization code the app gets at sign-in for a refresh token, and revokes
// that token when the account is deleted (App Store Review Guideline 5.1.1(v)).
package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config is the developer-account side of Sign in with Apple.
type Config struct {
	TeamID string // Apple Developer → Membership
	KeyID  string // the Sign in with Apple key
	// ClientID is the audience of the authorization codes: the app's bundle ID
	// for native sign-in (dev.timkrebs.ryft), a Services ID only for web sign-in.
	ClientID   string
	PrivateKey string // the key's .p8 file: PEM, or that PEM base64-encoded
	BaseURL    string // default https://appleid.apple.com (tests point it elsewhere)
}

type Client struct {
	cfg  Config
	key  *ecdsa.PrivateKey
	base string
	http *http.Client
	now  func() time.Time
}

const appleBase = "https://appleid.apple.com"

func New(c Config, client *http.Client) (*Client, error) {
	if c.TeamID == "" || c.KeyID == "" || c.ClientID == "" {
		return nil, errors.New("apple: team ID, key ID and client ID are required")
	}
	key, err := ParseKey(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := appleBase
	if c.BaseURL != "" {
		base = strings.TrimRight(c.BaseURL, "/")
	}
	return &Client{cfg: c, key: key, base: base, http: client, now: time.Now}, nil
}

// ParseKey reads an Apple developer key (.p8): PEM, or the PEM base64-encoded
// (how it fits in an environment variable). Sign in with Apple and APNs keys
// have the same shape.
func ParseKey(s string) (*ecdsa.PrivateKey, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "-----BEGIN") {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, errors.New("apple: private key is neither PEM nor base64-encoded PEM")
		}
		s = string(raw)
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("apple: private key: no PEM block")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple: private key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple: private key is not an EC key (expected the .p8 from Apple)")
	}
	return ec, nil
}

// clientSecret is the short-lived ES256 JWT Apple takes as client_secret.
func (c *Client) clientSecret() (string, error) {
	now := c.now()
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Issuer:    c.cfg.TeamID,
		Subject:   c.cfg.ClientID,
		Audience:  jwt.ClaimStrings{appleBase},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	})
	t.Header["kid"] = c.cfg.KeyID
	return t.SignedString(c.key)
}

var (
	// ErrInvalidGrant: the code is invalid, expired (five minutes) or used.
	ErrInvalidGrant = errors.New("apple: authorization code invalid, expired or already used")
	// ErrInvalidClient: Apple rejected our team/key/client ID or key.
	ErrInvalidClient = errors.New("apple: client rejected (check APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_CLIENT_ID, APPLE_KEY_P8)")
)

// Tokens is what an exchange yields.
type Tokens struct {
	RefreshToken string
	Subject      string // the Apple user ID (id_token sub)
}

// Exchange trades a one-time authorization code for a refresh token.
func (c *Client) Exchange(ctx context.Context, code string) (Tokens, error) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := c.post(ctx, "/auth/token", url.Values{"code": {code}, "grant_type": {"authorization_code"}}, &body); err != nil {
		return Tokens{}, err
	}
	if body.RefreshToken == "" {
		return Tokens{}, errors.New("apple: token response without refresh_token")
	}
	return Tokens{RefreshToken: body.RefreshToken, Subject: subject(body.IDToken)}, nil
}

// subject reads the id_token's sub. Not verified on purpose: the token comes
// straight from Apple over TLS in reply to our own authenticated request.
func subject(idToken string) string {
	var c jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(idToken, &c); err != nil {
		return ""
	}
	return c.Subject
}

// Revoke invalidates a refresh token (and the user's grant for the app).
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	return c.post(ctx, "/auth/revoke", url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}}, nil)
}

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	secret, err := c.clientSecret()
	if err != nil {
		return err
	}
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apple %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	switch e.Error {
	case "invalid_grant":
		return ErrInvalidGrant
	case "invalid_client", "unauthorized_client":
		return ErrInvalidClient
	}
	return fmt.Errorf("apple %s: %s %s", path, resp.Status, e.Error)
}
