package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// SupabaseOptions configure the Supabase verifier.
type SupabaseOptions struct {
	Issuer  string // https://<ref>.supabase.co/auth/v1
	JWKSURL string // <Issuer>/.well-known/jwks.json

	// RefreshInterval re-reads the JWKS. Supabase's edge caches it for 10
	// minutes and asks clients not to cache longer, so that is the default.
	RefreshInterval time.Duration
	// UnknownKIDEvery limits extra fetches triggered by tokens signed with a
	// key we have not seen (rotation). Default once a minute.
	UnknownKIDEvery time.Duration
	// Leeway tolerates clock skew on exp/iat. Default 30s.
	Leeway time.Duration
	// Client fetches the JWKS; default has a 10s timeout.
	Client *http.Client
}

// Supabase verifies Supabase Auth access tokens: ES256/RS256 signatures from
// the project's JWKS, issuer, audience "authenticated", expiry, role
// "authenticated", a non-anonymous user and a UUID subject.
type Supabase struct {
	jwksURL string
	kf      keyfunc.Keyfunc
	parser  *jwt.Parser
	fetches *fetchHealth
}

// NewSupabase starts the JWKS refresher; it runs until ctx ends. A JWKS that
// cannot be fetched yet does not fail construction — Ready reports it and
// Verify answers 503 until keys arrive.
func NewSupabase(ctx context.Context, o SupabaseOptions) (*Supabase, error) {
	if o.Issuer == "" || o.JWKSURL == "" {
		return nil, errors.New("auth: Supabase issuer and JWKS URL are required")
	}
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = 10 * time.Minute
	}
	if o.UnknownKIDEvery <= 0 {
		o.UnknownKIDEvery = time.Minute
	}
	if o.Leeway <= 0 {
		o.Leeway = 30 * time.Second
	}
	base := o.Client
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Second}
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	fetches := &fetchHealth{}
	client := &http.Client{Timeout: base.Timeout, Transport: &recordingTransport{next: transport, health: fetches}}

	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{o.JWKSURL}, keyfunc.Override{
		Client:            client,
		HTTPTimeout:       10 * time.Second,
		RefreshInterval:   o.RefreshInterval,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(o.UnknownKIDEvery), 1),
		// Never park a request behind the refresh limiter for long.
		RateLimitWaitMax: time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: JWKS client: %w", err)
	}
	return &Supabase{
		jwksURL: o.JWKSURL,
		kf:      kf,
		fetches: fetches,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"ES256", "RS256"}), // asymmetric only: no HS256, no "none"
			jwt.WithIssuer(o.Issuer),
			jwt.WithAudience("authenticated"),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(o.Leeway),
		),
	}, nil
}

// supabaseClaims are the access-token claims we read.
type supabaseClaims struct {
	jwt.RegisteredClaims
	Email       string `json:"email"`
	Role        string `json:"role"`
	IsAnonymous bool   `json:"is_anonymous"`
	AppMetadata struct {
		Provider string `json:"provider"`
	} `json:"app_metadata"`
	UserMetadata struct {
		FullName   string `json:"full_name"`
		Name       string `json:"name"`
		Issuer     string `json:"iss"`         // of the identity, e.g. https://appleid.apple.com
		ProviderID string `json:"provider_id"` // that identity's user ID
	} `json:"user_metadata"`
}

func (s *Supabase) Verify(ctx context.Context, token string) (Principal, error) {
	if !s.hasKeys(ctx) {
		return Principal{}, Unavailable("jwks_unavailable", errors.New("no signing keys loaded"))
	}
	var c supabaseClaims
	if _, err := s.parser.ParseWithClaims(token, &c, s.kf.KeyfuncCtx(ctx)); err != nil {
		return Principal{}, s.classify(err)
	}
	switch {
	case c.Role != "authenticated":
		return Principal{}, Unauthorized("role", fmt.Errorf("role %q", clip(c.Role, 32)))
	case c.IsAnonymous:
		return Principal{}, Unauthorized("anonymous", nil)
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil || id == uuid.Nil {
		return Principal{}, Unauthorized("subject", err)
	}
	name := c.UserMetadata.FullName
	if name == "" {
		name = c.UserMetadata.Name
	}
	var appleSub string
	if c.UserMetadata.Issuer == "https://appleid.apple.com" {
		appleSub = clip(c.UserMetadata.ProviderID, 256)
	}
	return Principal{
		UserID:       id,
		Email:        clip(c.Email, 320),
		Provider:     clip(c.AppMetadata.Provider, 64),
		Name:         clip(strings.TrimSpace(name), 200),
		AppleSubject: appleSub,
	}, nil
}

// classify maps a parse failure to 401 (the token is bad) or 503 (we could not
// check it). Key-lookup failures are 401 only while the JWKS is healthy.
func (s *Supabase) classify(err error) error {
	switch {
	case errors.Is(err, keyfunc.ErrKeyfunc):
		switch {
		case strings.Contains(err.Error(), "refresh rate limiter"):
			// Unknown kid and no refresh allowed right now (jwkset exposes no
			// typed error for this): retryable, never a sign-out.
			return Unavailable("jwks_refresh_limited", err)
		case errors.Is(err, jwkset.ErrKeyNotFound) && s.fetches.lastFailed():
			return Unavailable("jwks_unavailable", err)
		case errors.Is(err, jwkset.ErrKeyNotFound):
			return Unauthorized("unknown_key", err)
		default:
			return Unauthorized("key_mismatch", err)
		}
	case errors.Is(err, jwt.ErrTokenExpired):
		return Unauthorized("expired", err)
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return Unauthorized("issuer", err)
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return Unauthorized("audience", err)
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return Unauthorized("signature", err)
	case errors.Is(err, jwt.ErrTokenMalformed):
		return Unauthorized("malformed", err)
	default:
		return Unauthorized("invalid", err)
	}
}

func (s *Supabase) hasKeys(ctx context.Context) bool {
	keys, err := s.kf.Storage().KeyReadAll(ctx)
	return err == nil && len(keys) > 0
}

// Ready reports whether signing keys are loaded. It never fetches.
func (s *Supabase) Ready(ctx context.Context) error {
	if !s.hasKeys(ctx) {
		return fmt.Errorf("no JWKS keys loaded from %s", s.jwksURL)
	}
	return nil
}

// fetchHealth remembers whether the most recent JWKS fetch worked, so an
// unknown key during an outage is not mistaken for a forged token.
type fetchHealth struct {
	lastOK, lastFail atomic.Int64 // unix nanoseconds
}

func (h *fetchHealth) lastFailed() bool { return h.lastFail.Load() > h.lastOK.Load() }

type recordingTransport struct {
	next   http.RoundTripper
	health *fetchHealth
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	now := time.Now().UnixNano()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.health.lastFail.Store(now)
	} else {
		t.health.lastOK.Store(now)
	}
	return resp, err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
