package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/timkrebs/ryft/internal/metrics"
)

// GoogleJWKSURL is where Google publishes the keys that sign its OIDC tokens (the
// tokens Pub/Sub push and Cloud Scheduler attach).
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleIssuers are the iss values of a Google OIDC token: Google writes the URL, and
// its own verifiers also accept the bare host.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// PushVerifier checks the token of a push or tick request (/push/*, /internal/*): nil
// accepts it, an *Error says why not (401: the token is not acceptable; 503: it cannot
// be checked right now). GoogleOIDC and InsecurePush implement it.
type PushVerifier interface {
	VerifyPush(r *http.Request) error
}

// GoogleOIDCOptions configure GoogleOIDC: the token's audience and the one service
// account whose tokens count.
type GoogleOIDCOptions struct {
	Audience string // PUSH_AUDIENCE: the service's run.app URL, exactly as the subscription sets it
	Email    string // PUSH_SA_EMAIL: the push service account
	Subject  string // PUSH_SA_ID: that account's unique ID, the token's sub
	JWKSURL  string // default GoogleJWKSURL (tests point it elsewhere)

	// RefreshInterval re-reads the JWKS (default 1 h; Google rotates its keys over days and
	// announces a new one before it signs with it). UnknownKIDEvery limits the extra
	// fetches a token with an unseen key id triggers (default once a minute). NoKeysEvery
	// limits the fetches a token triggers while no keys are loaded at all (default every
	// 10 s): a cold instance whose first fetch failed must not wait for the hourly
	// refresh. Leeway tolerates clock skew on exp and iat (default 30 s).
	RefreshInterval, UnknownKIDEvery, NoKeysEvery, Leeway time.Duration
	// Client fetches the JWKS; default has a 10 s timeout.
	Client *http.Client
}

// noKeysFetchMax bounds the fetch a token makes while no keys are loaded, within the
// request's own deadline: one push or tick waits at most this long for it.
const noKeysFetchMax = 5 * time.Second

// GoogleOIDC verifies the Google-signed OIDC token on a push or a tick (D218): RS256
// only, exp required, iss Google's, aud exactly the configured audience (never the
// Host header), email the push service account's with email_verified, and sub its
// unique ID. It fails closed: anything it cannot read is a 401, and a token it cannot
// check (no keys loaded and none fetched now, or an unknown key id while the last fetch
// failed) a 503.
type GoogleOIDC struct {
	audience, email, subject string
	jwksURL                  string
	kf                       keyfunc.Keyfunc
	parser                   *jwt.Parser
	fetches                  *fetchHealth

	// fetch reads the same key set as kf with a limiter that always lets it refresh;
	// noKeys is the limit, at most one fetch per NoKeysEvery while no keys are loaded.
	fetch  jwkset.Storage
	noKeys *rate.Limiter
}

// NewGoogleOIDC starts the JWKS refresher, which runs until ctx ends, on the same
// keyfunc machinery as the Supabase verifier. A JWKS that cannot be fetched yet does
// not fail construction: Ready reports it, and a token fetches it again (at most every
// NoKeysEvery) before VerifyPush answers 503.
func NewGoogleOIDC(ctx context.Context, o GoogleOIDCOptions) (*GoogleOIDC, error) {
	if o.Audience == "" || o.Email == "" || o.Subject == "" {
		return nil, errors.New("auth: the push audience, service account email and unique ID are required")
	}
	if o.JWKSURL == "" {
		o.JWKSURL = GoogleJWKSURL
	}
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = time.Hour
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
	// What keyfunc.NewDefaultOverrideCtx builds (the Supabase verifier's), put together
	// here so that a second reader can share the one remote key set: kf's, whose limiter
	// is the unknown key id's, and fetch, for a verifier that has no keys at all.
	remote, err := jwkset.NewStorageFromHTTP(o.JWKSURL, jwkset.HTTPClientStorageOptions{
		Client:                    client,
		Ctx:                       ctx,
		HTTPTimeout:               10 * time.Second,
		NoErrorReturnFirstHTTPReq: true,
		RefreshInterval:           o.RefreshInterval,
		RefreshErrorHandler: func(ctx context.Context, err error) {
			slog.Default().ErrorContext(ctx, "Failed to refresh HTTP JWK Set from remote HTTP resource.", "error", err, "url", o.JWKSURL)
			fetches.failed(err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: Google JWKS client: %w", err)
	}
	storage, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{o.JWKSURL: remote},
		RefreshUnknownKID: rate.NewLimiter(rate.Every(o.UnknownKIDEvery), 1),
		RateLimitWaitMax:  time.Second, // as the Supabase verifier: a push never waits long behind the limiter
	})
	if err != nil {
		return nil, fmt.Errorf("auth: Google JWKS client: %w", err)
	}
	fetch, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{o.JWKSURL: remote},
		RefreshUnknownKID: rate.NewLimiter(rate.Inf, 1), // the limit is noKeys, checked before
		RateLimitWaitMax:  noKeysFetchMax,               // jwkset fetches on this context: it bounds the fetch
	})
	if err != nil {
		return nil, fmt.Errorf("auth: Google JWKS client: %w", err)
	}
	kf, err := keyfunc.New(keyfunc.Options{Ctx: ctx, Storage: storage})
	if err != nil {
		return nil, fmt.Errorf("auth: Google JWKS client: %w", err)
	}
	return &GoogleOIDC{
		audience: o.Audience, email: o.Email, subject: o.Subject, jwksURL: o.JWKSURL,
		kf: kf, fetches: fetches, fetch: fetch, noKeys: rate.NewLimiter(rate.Every(o.NoKeysEvery), 1),
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"RS256"}), // Google signs with RS256: no other alg, no HS256, no "none"
			jwt.WithAudience(o.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(o.Leeway),
		),
	}, nil
}

// googleClaims are the OIDC token's claims we check.
type googleClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"` // a JSON boolean; any other shape fails to parse (fail closed)
}

// VerifyPush checks the request's bearer token and counts a refusal in
// ryft_auth_failures_total under its reason. It reads only the Authorization header,
// never the Host: the audience is the configured one.
func (g *GoogleOIDC) VerifyPush(r *http.Request) error {
	err := g.verifyRequest(r)
	if err != nil {
		var ae *Error
		if !errors.As(err, &ae) {
			ae = Unauthorized("invalid", err)
			err = ae
		}
		metrics.AuthFailures.WithLabelValues(ae.Reason).Inc()
	}
	return err
}

func (g *GoogleOIDC) verifyRequest(r *http.Request) error {
	token, ok := bearer(r)
	if !ok {
		return Unauthorized("missing_token", nil)
	}
	if len(token) > maxTokenLen {
		return Unauthorized("malformed", nil)
	}
	return g.Verify(r.Context(), token)
}

// Verify checks one token: nil, or an *Error (401 or 503). Its errors name the check
// that failed, never a claim's value (a token's email can be anyone's).
func (g *GoogleOIDC) Verify(ctx context.Context, token string) error {
	if !g.loadKeys(ctx) {
		return Unavailable("jwks_unavailable", errors.New("no Google signing keys loaded"))
	}
	var c googleClaims
	// Detached from the caller's cancellation, as in Supabase.Verify: an unknown key id's
	// fetch must not end with a push that hangs up (D184). jwkset's 1 s and the client's
	// 10 s still bound it.
	if _, err := g.parser.ParseWithClaims(token, &c, g.kf.KeyfuncCtx(context.WithoutCancel(ctx))); err != nil {
		return classifyJWT(err, g.fetches)
	}
	switch {
	case !slices.Contains(googleIssuers, c.Issuer):
		return Unauthorized("issuer", errors.New("not Google's issuer"))
	case len(c.Audience) != 1 || c.Audience[0] != g.audience:
		return Unauthorized("audience", errors.New("the audience is not exactly this service's"))
	case c.Email != g.email:
		return Unauthorized("subject", errors.New("email is not the push service account's"))
	case !c.EmailVerified:
		return Unauthorized("subject", errors.New("email_verified is not true"))
	case c.Subject != g.subject:
		return Unauthorized("subject", errors.New("sub is not the push service account's unique ID"))
	}
	return nil
}

// Ready reports whether Google's signing keys are loaded. It never fetches.
func (g *GoogleOIDC) Ready(ctx context.Context) error {
	if !hasKeys(ctx, g.kf) {
		return fmt.Errorf("no JWKS keys loaded from %s", g.jwksURL)
	}
	return nil
}

func hasKeys(ctx context.Context, kf keyfunc.Keyfunc) bool {
	keys, err := kf.Storage().KeyReadAll(ctx)
	return err == nil && len(keys) > 0
}

// loadKeys reports whether Google's keys are loaded and, when none are, fetches them
// first: at most once per NoKeysEvery for the whole verifier (the other requests meanwhile
// get their 503 at once), on the request's ctx and at most noKeysFetchMax. Without it a
// cold instance whose first fetch failed would answer 503 to every push and tick until
// the hourly refresh, and Pub/Sub would dead-letter them. A failed fetch is logged and
// recorded by the refresh error handler, as any other.
func (g *GoogleOIDC) loadKeys(ctx context.Context) bool {
	if hasKeys(ctx, g.kf) {
		return true
	}
	if ctx.Err() != nil || !g.noKeys.Allow() {
		return false
	}
	// A key id no set has misses, so jwkset fetches the set (fetch's limiter lets it) into
	// the storage kf reads. The miss itself is expected: what counts is whether keys came.
	// Detached from the caller's cancel, so a client that hangs up cannot cancel the one
	// fetch the limiter allows, but held to the request's deadline (at most noKeysFetchMax).
	limit := time.Now().Add(noKeysFetchMax)
	if d, ok := ctx.Deadline(); ok && d.Before(limit) {
		limit = d
	}
	fctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), limit)
	defer cancel()
	_, _ = g.fetch.KeyRead(fctx, "")
	return hasKeys(fctx, g.kf)
}

// classifyJWT maps a parse failure to 401 (the token is bad) or 503 (it could not be
// checked), with the reasons of the Supabase verifier: a key lookup fails as 401 only
// while the JWKS is healthy. That includes an unknown key id after the minute's refresh
// is spent: anyone can send one, so while the last fetch worked it is a 401 unknown_key,
// not a 503 the public api and ingest would count against their availability. Pub/Sub
// retries a 401 as it does a 503, so a real rotation inside the minute is delivered on a
// later attempt.
func classifyJWT(err error, fetches *fetchHealth) error {
	switch {
	case errors.Is(err, keyfunc.ErrKeyfunc):
		switch {
		case strings.Contains(err.Error(), "refresh rate limiter") && fetches.lastFailed():
			return Unavailable("jwks_refresh_limited", err)
		case strings.Contains(err.Error(), "refresh rate limiter"):
			return Unauthorized("unknown_key", err)
		case errors.Is(err, jwkset.ErrKeyNotFound) && fetches.lastFailed():
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

// insecurePush accepts every request: ALLOW_INSECURE_PUSH=true.
type insecurePush struct{}

func (insecurePush) VerifyPush(*http.Request) error { return nil }

// InsecurePush is a PushVerifier that accepts every push and tick without a token, for
// local development with the emulator (ALLOW_INSECURE_PUSH=true, which config refuses
// on Cloud Run). It logs a warning when built, so a service that runs without the check
// says so once at start-up.
func InsecurePush(log *slog.Logger) PushVerifier {
	log.Warn("push and tick tokens are NOT checked (ALLOW_INSECURE_PUSH=true): local development only")
	return insecurePush{}
}
