package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// NoKeysEvery limits the fetches a token triggers while no keys are loaded at
	// all (default every 10 s): a cold instance whose first fetch failed must not
	// answer 503 until the next refresh, as the Google verifier (GoogleOIDCOptions).
	NoKeysEvery time.Duration
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

	// fetch reads the same key set as kf with a limiter that always lets it refresh;
	// noKeys is the limit, at most one fetch per NoKeysEvery while no keys are loaded.
	fetch  jwkset.Storage
	noKeys *rate.Limiter
}

// NewSupabase starts the JWKS refresher; it runs until ctx ends. A JWKS that
// cannot be fetched yet does not fail construction: Ready reports it, and a
// token fetches it again (at most every NoKeysEvery) before Verify answers 503.
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
	if o.NoKeysEvery <= 0 {
		o.NoKeysEvery = 10 * time.Second
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

	// What keyfunc.NewDefaultOverrideCtx builds, put together here (as for the Google
	// verifier) so that a second reader shares the one remote key set: kf's, whose
	// limiter is the unknown key id's, and fetch, for a verifier that has no keys at all.
	remote, err := jwkset.NewStorageFromHTTP(o.JWKSURL, jwkset.HTTPClientStorageOptions{
		Client:                    client,
		Ctx:                       ctx,
		HTTPTimeout:               10 * time.Second,
		NoErrorReturnFirstHTTPReq: true,
		RefreshInterval:           o.RefreshInterval,
		// Every refresh that does not end with a key set lands here: no response, another status, or a
		// body that is not a key set (the transport sees only the status). Keyfunc's own log line is kept.
		RefreshErrorHandler: func(ctx context.Context, err error) {
			slog.Default().ErrorContext(ctx, "Failed to refresh HTTP JWK Set from remote HTTP resource.", "error", err, "url", o.JWKSURL)
			fetches.failed(err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: JWKS client: %w", err)
	}
	storage, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{o.JWKSURL: remote},
		RefreshUnknownKID: rate.NewLimiter(rate.Every(o.UnknownKIDEvery), 1),
		// Never park a request behind the refresh limiter for long. It also bounds the refresh an unknown
		// key id triggers: jwkset waits for the limiter and fetches on the same 1 s context.
		RateLimitWaitMax: time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: JWKS client: %w", err)
	}
	fetch, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{o.JWKSURL: remote},
		RefreshUnknownKID: rate.NewLimiter(rate.Inf, 1), // the limit is noKeys, checked before
		RateLimitWaitMax:  noKeysFetchMax,               // jwkset fetches on this context: it bounds the fetch
	})
	if err != nil {
		return nil, fmt.Errorf("auth: JWKS client: %w", err)
	}
	kf, err := keyfunc.New(keyfunc.Options{Ctx: ctx, Storage: storage})
	if err != nil {
		return nil, fmt.Errorf("auth: JWKS client: %w", err)
	}
	return &Supabase{
		jwksURL: o.JWKSURL,
		kf:      kf,
		fetches: fetches,
		fetch:   fetch,
		noKeys:  rate.NewLimiter(rate.Every(o.NoKeysEvery), 1),
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
	// aal and amr are the session's assurance level and authentication methods. They stay raw and
	// are read leniently (claims.go): a typed field would make one unexpected shape a parse error,
	// and with it a 401 for every app user.
	AAL         json.RawMessage `json:"aal"`
	AMR         json.RawMessage `json:"amr"`
	AppMetadata struct {
		Provider string `json:"provider"`
		// Role is the operator role. app_metadata is writable only with the service key; the
		// top-level role and user_metadata (which the user can write) never count.
		Role json.RawMessage `json:"role"`
	} `json:"app_metadata"`
	UserMetadata struct {
		FullName   string `json:"full_name"`
		Name       string `json:"name"`
		Issuer     string `json:"iss"`         // of the identity, e.g. https://appleid.apple.com
		ProviderID string `json:"provider_id"` // that identity's user ID
	} `json:"user_metadata"`
}

// Verify checks token and returns its Principal. A bad token is a 401 (Unauthorized). One
// that cannot be checked right now, because no signing keys are loaded or an unknown key
// cannot be fetched, is a 503 (Unavailable), so an outage never signs a user out (D29).
func (s *Supabase) Verify(ctx context.Context, token string) (Principal, error) {
	if !s.loadKeys(ctx) {
		return Principal{}, Unavailable("jwks_unavailable", errors.New("no signing keys loaded"))
	}
	var c supabaseClaims
	// The key lookup may fetch the JWKS (an unknown key id). That fetch must not end with the client: a
	// caller that hangs up would cut it short, use up the minute's refresh and leave a rotated key
	// unloaded. Detached from the caller's cancellation and deadline, it stays bounded by jwkset's 1 s
	// (RateLimitWaitMax) and the HTTP client's 10 s; the context's values stay (D184).
	if _, err := s.parser.ParseWithClaims(token, &c, s.kf.KeyfuncCtx(context.WithoutCancel(ctx))); err != nil {
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
		AppRole:      parseAppRole(c.AppMetadata.Role),
		AAL:          parseAAL(c.AAL),
		MFAAt:        parseTOTPAt(c.AMR),
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

// loadKeys reports whether signing keys are loaded and, when none are, fetches them
// first: at most once per NoKeysEvery for the whole verifier (the other requests
// meanwhile get their 503 at once), at most noKeysFetchMax and never past the request's
// deadline, as GoogleOIDC.loadKeys. Without it an instance whose first fetch failed (a
// cold start during a Supabase blip) would answer 503 to every app request until the
// 10-minute refresh. A failed fetch is logged and recorded by the refresh error handler.
func (s *Supabase) loadKeys(ctx context.Context) bool {
	if hasKeys(ctx, s.kf) {
		return true
	}
	if ctx.Err() != nil || !s.noKeys.Allow() {
		return false
	}
	// A key id no set has misses, so jwkset fetches the set (fetch's limiter lets it) into
	// the storage kf reads; what counts is whether keys came. Detached from the caller's
	// cancel, so a client that hangs up cannot cancel the one fetch the limiter allows,
	// but held to the request's deadline (at most noKeysFetchMax).
	limit := time.Now().Add(noKeysFetchMax)
	if d, ok := ctx.Deadline(); ok && d.Before(limit) {
		limit = d
	}
	fctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), limit)
	defer cancel()
	_, _ = s.fetch.KeyRead(fctx, "")
	return hasKeys(fctx, s.kf)
}

// Ready reports whether signing keys are loaded. It never fetches.
func (s *Supabase) Ready(ctx context.Context) error {
	if !hasKeys(ctx, s.kf) {
		return fmt.Errorf("no JWKS keys loaded from %s", s.jwksURL)
	}
	return nil
}

// fetchHealth remembers whether the most recent JWKS fetch worked, so an
// unknown key during an outage is not mistaken for a forged token. The
// transport records a response with the expected status (ok); jwkset's
// refresh error handler records a refresh that did not end with a key set
// (failed), a 200 whose body is not one included: that is ok, then failed,
// in that order, so it counts as failed.
type fetchHealth struct {
	lastOK, lastFail atomic.Int64 // unix nanoseconds
}

func (h *fetchHealth) lastFailed() bool { return h.lastFail.Load() > h.lastOK.Load() }

func (h *fetchHealth) ok() { h.lastOK.Store(time.Now().UnixNano()) }

// failed records a failed refresh, except one cut short because its own
// context was cancelled (the service stopping; a request's hang-up no longer
// reaches it, see Verify), which says nothing about the JWKS. A refresh that
// ran out of its time (1 s for an unknown key id, 10 s otherwise) did fail.
func (h *fetchHealth) failed(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	h.lastFail.Store(time.Now().UnixNano())
}

type recordingTransport struct {
	next   http.RoundTripper
	health *fetchHealth
}

// RoundTrip records a response with the expected status. It records no
// failure: the refresh error handler does, seeing the whole refresh (and
// leaving out a cancelled one; D184).
func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusOK {
		t.health.ok()
	}
	return resp, err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
