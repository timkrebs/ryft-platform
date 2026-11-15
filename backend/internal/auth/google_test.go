package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"

	"github.com/timkrebs/ryft/internal/metrics"
)

// googleJWKS serves a mutable RSA key set the way Google's certs endpoint does, and can
// be switched to failing or to hanging until the client gives up. hits counts requests.
type googleJWKS struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	keys    map[string]*rsa.PrivateKey
	failing bool
	hanging bool
}

func newGoogleJWKS(t *testing.T) *googleJWKS {
	t.Helper()
	s := &googleJWKS{keys: map[string]*rsa.PrivateKey{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		hanging := s.hanging
		s.mu.Unlock()
		if hanging {
			<-r.Context().Done()
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failing {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		var set struct {
			Keys []map[string]string `json:"keys"`
		}
		for kid, k := range s.keys {
			set.Keys = append(set.Keys, rsaJWK(kid, &k.PublicKey))
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(s.Close)
	return s
}

// newRSAKey is a key made for this run: no key material is in the repository.
func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (s *googleJWKS) add(kid string, k *rsa.PrivateKey) {
	s.mu.Lock()
	s.keys[kid] = k
	s.mu.Unlock()
}

func (s *googleJWKS) set(failing, hanging bool) {
	s.mu.Lock()
	s.failing, s.hanging = failing, hanging
	s.mu.Unlock()
}

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]string {
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": enc(pub.N.Bytes()), "e": enc(big.NewInt(int64(pub.E)).Bytes())}
}

// pushIdentity is what a service is configured with: its audience and the push service
// account. Built at runtime, so nothing in the test looks like a real value.
type pushIdentity struct{ audience, email, subject string }

func newPushIdentity() pushIdentity {
	return pushIdentity{
		audience: "https://scoring-" + strconv.Itoa(100000+mrand.IntN(900000)) + ".europe-west1.run.app",
		email:    "ryft-push@ryft-test-" + strconv.Itoa(mrand.IntN(1000)) + ".iam.gserviceaccount.com",
		subject:  strconv.FormatUint(1e17+mrand.Uint64N(1e18), 10),
	}
}

// claims are the claims of a token Pub/Sub attaches to a push for id.
func (id pushIdentity) claims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": id.audience, "azp": id.subject, "sub": id.subject,
		"email": id.email, "email_verified": true, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

func signRS(t *testing.T, method jwt.SigningMethod, key *rsa.PrivateKey, kid string, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newGoogleVerifier(t *testing.T, srv *googleJWKS, id pushIdentity) *GoogleOIDC {
	t.Helper()
	return newGoogleVerifierWith(t, GoogleOIDCOptions{Audience: id.audience, Email: id.email, Subject: id.subject, JWKSURL: srv.URL})
}

func newGoogleVerifierWith(t *testing.T, o GoogleOIDCOptions) *GoogleOIDC {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g, err := NewGoogleOIDC(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func pushRequest(host, authorization string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://"+host+"/push/ingested", strings.NewReader("{}"))
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

// anyReason marks a refusal whose reason belongs to the JWT library (a missing claim or
// header): only the 401 is pinned.
const anyReason = "*"

// reasonOf is the *Error's status and reason, or 0 and "" for nil.
func reasonOf(t *testing.T, err error) (int, string) {
	t.Helper()
	if err == nil {
		return 0, ""
	}
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("not an *auth.Error: %T %v", err, err)
	}
	return ae.Status, ae.Reason
}

func TestGoogleOIDC(t *testing.T) {
	srv := newGoogleJWKS(t)
	key := newRSAKey(t)
	srv.add("google-1", key)
	stranger := newRSAKey(t) // a key Google never published
	id := newPushIdentity()
	g := newGoogleVerifier(t, srv, id)

	good := func(edit func(jwt.MapClaims)) string {
		c := id.claims()
		if edit != nil {
			edit(c)
		}
		return signRS(t, jwt.SigningMethodRS256, key, "google-1", c)
	}
	// HS256 keyed with the public key: the classic confusion attack on RS256 verifiers.
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, id.claims())
	hs.Header["kid"] = "google-1"
	hsToken, err := hs.SignedString(pubDER)
	if err != nil {
		t.Fatal(err)
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, id.claims())
	none.Header["kid"] = "google-1"
	noneToken, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(id.audience, "https://")

	for _, c := range []struct {
		name, auth string
		status     int
		reason     string
	}{
		{"good token", "Bearer " + good(nil), 0, ""},
		{"the bare issuer Google's verifiers accept", "Bearer " + good(func(c jwt.MapClaims) { c["iss"] = "accounts.google.com" }), 0, ""},
		{"lower-case scheme", "bearer " + good(nil), 0, ""},
		{"no Authorization", "", 401, "missing_token"},
		{"not a bearer", "Basic " + base64.StdEncoding.EncodeToString([]byte("a:b")), 401, "missing_token"},
		{"too long", "Bearer " + strings.Repeat("a", maxTokenLen+1), 401, "malformed"},
		{"not a JWT", "Bearer abc", 401, "malformed"},
		{"alg none", "Bearer " + noneToken, 401, "signature"},
		{"HS256 with the public key", "Bearer " + hsToken, 401, "signature"},
		{"RS512", "Bearer " + signRS(t, jwt.SigningMethodRS512, key, "google-1", id.claims()), 401, "signature"},
		{"signed by another key under Google's kid", "Bearer " + signRS(t, jwt.SigningMethodRS256, stranger, "google-1", id.claims()), 401, "signature"},
		{"expired", "Bearer " + good(func(c jwt.MapClaims) {
			c["iat"], c["exp"] = time.Now().Add(-3*time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix()
		}), 401, "expired"},
		{"no exp", "Bearer " + good(func(c jwt.MapClaims) { delete(c, "exp") }), 401, "invalid"},
		{"issued in the future", "Bearer " + good(func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Hour).Unix() }), 401, "invalid"},
		{"wrong issuer", "Bearer " + good(func(c jwt.MapClaims) { c["iss"] = "https://accounts.example.com" }), 401, "issuer"},
		{"no issuer", "Bearer " + good(func(c jwt.MapClaims) { delete(c, "iss") }), 401, "issuer"},
		{"wrong audience", "Bearer " + good(func(c jwt.MapClaims) { c["aud"] = "https://notify-1.europe-west1.run.app" }), 401, "audience"},
		{"audience with a path", "Bearer " + good(func(c jwt.MapClaims) { c["aud"] = id.audience + "/push/ingested" }), 401, "audience"},
		{"two audiences, ours among them", "Bearer " + good(func(c jwt.MapClaims) { c["aud"] = []string{id.audience, "https://other.example"} }), 401, "audience"},
		{"no audience", "Bearer " + good(func(c jwt.MapClaims) { delete(c, "aud") }), 401, anyReason},
		{"wrong email", "Bearer " + good(func(c jwt.MapClaims) { c["email"] = "someone-else@ryft-test.iam.gserviceaccount.com" }), 401, "subject"},
		{"email not verified", "Bearer " + good(func(c jwt.MapClaims) { c["email_verified"] = false }), 401, "subject"},
		{"email_verified missing", "Bearer " + good(func(c jwt.MapClaims) { delete(c, "email_verified") }), 401, "subject"},
		{"email_verified as a string", "Bearer " + good(func(c jwt.MapClaims) { c["email_verified"] = "true" }), 401, "malformed"},
		{"wrong sub", "Bearer " + good(func(c jwt.MapClaims) { c["sub"] = "1" + id.subject }), 401, "subject"},
		{"no sub", "Bearer " + good(func(c jwt.MapClaims) { delete(c, "sub") }), 401, "subject"},
		// keyfunc tries the loaded keys when a token names none: the signature still decides.
		{"no kid, Google's key", "Bearer " + signRS(t, jwt.SigningMethodRS256, key, "", id.claims()), 0, ""},
		{"no kid, another key", "Bearer " + signRS(t, jwt.SigningMethodRS256, stranger, "", id.claims()), 401, anyReason},
		// Last: it uses the minute's one refresh for an unseen key id.
		{"unknown kid", "Bearer " + signRS(t, jwt.SigningMethodRS256, stranger, "rotated", id.claims()), 401, "unknown_key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var before float64
			counted := c.reason != "" && c.reason != anyReason
			if counted {
				before = testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(c.reason))
			}
			status, reason := reasonOf(t, g.VerifyPush(pushRequest(host, c.auth)))
			if status != c.status || (reason != c.reason && c.reason != anyReason) {
				t.Fatalf("got %d %q, want %d %q", status, reason, c.status, c.reason)
			}
			if counted {
				if n := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(c.reason)) - before; n != 1 {
					t.Fatalf("ryft_auth_failures_total{reason=%q} rose by %v, want 1", c.reason, n)
				}
			}
		})
	}
}

// The audience is configuration: a token minted for whatever host the request names is
// refused, so a Host header cannot choose what is accepted.
func TestGoogleOIDCIgnoresTheHost(t *testing.T) {
	srv := newGoogleJWKS(t)
	key := newRSAKey(t)
	srv.add("google-1", key)
	id := newPushIdentity()
	g := newGoogleVerifier(t, srv, id)
	c := id.claims()
	c["aud"] = "https://attacker-chosen.example"
	r := pushRequest("attacker-chosen.example", "Bearer "+signRS(t, jwt.SigningMethodRS256, key, "google-1", c))
	r.Header.Set("X-Forwarded-Host", "attacker-chosen.example")
	if status, reason := reasonOf(t, g.VerifyPush(r)); status != 401 || reason != "audience" {
		t.Fatalf("got %d %q", status, reason)
	}
	// And the right token is accepted whatever the Host says.
	r = pushRequest("attacker-chosen.example", "Bearer "+signRS(t, jwt.SigningMethodRS256, key, "google-1", id.claims()))
	if err := g.VerifyPush(r); err != nil {
		t.Fatal(err)
	}
}

// Without Google's keys a token cannot be checked: 503, never a 401 and never a pass.
func TestGoogleOIDCWithoutKeys(t *testing.T) {
	srv := newGoogleJWKS(t)
	srv.mu.Lock()
	srv.failing = true
	srv.mu.Unlock()
	key := newRSAKey(t)
	id := newPushIdentity()
	g := newGoogleVerifier(t, srv, id)
	if g.Ready(context.Background()) == nil {
		t.Fatal("ready without keys")
	}
	before := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("jwks_unavailable"))
	status, reason := reasonOf(t, g.VerifyPush(pushRequest("x", "Bearer "+signRS(t, jwt.SigningMethodRS256, key, "google-1", id.claims()))))
	if status != 503 || reason != "jwks_unavailable" {
		t.Fatalf("got %d %q", status, reason)
	}
	if n := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("jwks_unavailable")) - before; n != 1 {
		t.Fatalf("counted %v", n)
	}

	srv2 := newGoogleJWKS(t)
	srv2.add("google-1", key)
	if err := newGoogleVerifier(t, srv2, id).Ready(context.Background()); err != nil {
		t.Fatalf("keys loaded at construction: %v", err)
	}
}

// A cold instance whose first fetch failed must not wait for the hourly refresh: while
// no keys are loaded, a token fetches the set again, at most once per NoKeysEvery for the
// whole verifier (every other token meanwhile gets its 503 without a fetch). Once the
// JWKS answers, pushes verify again within that limit.
func TestGoogleOIDCFetchesWhileItHasNoKeys(t *testing.T) {
	const every = time.Second
	srv := newGoogleJWKS(t)
	srv.set(true, false)
	key := newRSAKey(t)
	id := newPushIdentity()
	g := newGoogleVerifierWith(t, GoogleOIDCOptions{Audience: id.audience, Email: id.email, Subject: id.subject,
		JWKSURL: srv.URL, NoKeysEvery: every})
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches at construction, want 1", n)
	}
	token := "Bearer " + signRS(t, jwt.SigningMethodRS256, key, "google-1", id.claims())
	verify := func() (int, string) { return reasonOf(t, g.VerifyPush(pushRequest("x", token))) }

	start := time.Now()
	if status, reason := verify(); status != 503 || reason != "jwks_unavailable" || srv.hits.Load() != 2 {
		t.Fatalf("the JWKS still fails: %d %q after %d fetches, want 503 jwks_unavailable after a fetch", status, reason, srv.hits.Load())
	}
	if status, _ := verify(); status != 503 || srv.hits.Load() != 2 {
		t.Fatalf("a second token inside the limit: %d after %d fetches, want 503 and no fetch", status, srv.hits.Load())
	}
	srv.add("google-1", key)
	srv.set(false, false)
	inside := time.Since(start) < every
	if status, _ := verify(); inside && (status != 503 || srv.hits.Load() != 2) {
		t.Fatalf("the JWKS is back, still inside the limit: %d after %d fetches, want 503 and no fetch", status, srv.hits.Load())
	}
	for deadline := start.Add(every + 5*time.Second); ; time.Sleep(20 * time.Millisecond) {
		status, reason := verify()
		if status == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d %q %s after the JWKS came back", status, reason, time.Since(start))
		}
	}
	if took := time.Since(start); took < every {
		t.Errorf("verified after %s: the second fetch came before the %s limit", took, every)
	}
	if n := srv.hits.Load(); n != 3 {
		t.Errorf("%d fetches, want 3: construction, the first token, one after the limit", n)
	}
	if err := g.Ready(context.Background()); err != nil {
		t.Fatalf("keys loaded: %v", err)
	}
	if g.noKeys.Limit() != rate.Every(every) {
		t.Fatalf("limit %v", g.noKeys.Limit())
	}
	if d := newGoogleVerifier(t, srv, id); d.noKeys.Limit() != rate.Every(10*time.Second) {
		t.Fatalf("default limit %v, want every 10 s", d.noKeys.Limit())
	}
}

// That fetch is bounded by the request: a JWKS that hangs costs a push at most its own
// deadline (and never more than noKeysFetchMax), not the client's 10 s.
func TestGoogleOIDCNoKeysFetchIsBoundedByTheRequest(t *testing.T) {
	srv := newGoogleJWKS(t)
	srv.set(true, false) // the construction's fetch fails fast
	key := newRSAKey(t)
	id := newPushIdentity()
	g := newGoogleVerifier(t, srv, id)
	srv.set(false, true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	status, reason := reasonOf(t, g.Verify(ctx, signRS(t, jwt.SigningMethodRS256, key, "google-1", id.claims())))
	if status != 503 || reason != "jwks_unavailable" {
		t.Fatalf("got %d %q", status, reason)
	}
	if took := time.Since(start); took > 2*time.Second || srv.hits.Load() != 2 {
		t.Fatalf("took %s with %d fetches: want the request's 100 ms and one fetch", took, srv.hits.Load())
	}
}

// An unknown key id once the minute's refresh is spent: anyone can send one, so while the
// last fetch worked it is a 401 unknown_key; only while the last fetch failed is it a 503
// (the token may be a rotation that cannot be fetched now).
func TestGoogleOIDCUnknownKeyID(t *testing.T) {
	key, stranger := newRSAKey(t), newRSAKey(t)
	id := newPushIdentity()
	unknown := func(kid string) string { return signRS(t, jwt.SigningMethodRS256, stranger, kid, id.claims()) }

	t.Run("the JWKS answers", func(t *testing.T) {
		srv := newGoogleJWKS(t)
		srv.add("google-1", key)
		g := newGoogleVerifier(t, srv, id)
		for i, kid := range []string{"forged-1", "forged-2", "forged-3"} { // the first uses the minute's refresh
			if status, reason := reasonOf(t, g.Verify(context.Background(), unknown(kid))); status != 401 || reason != "unknown_key" {
				t.Fatalf("token %d: %d %q, want 401 unknown_key", i+1, status, reason)
			}
		}
		if n := srv.hits.Load(); n != 2 {
			t.Fatalf("%d fetches, want 2: construction and the minute's one", n)
		}
	})
	t.Run("the last fetch failed", func(t *testing.T) {
		srv := newGoogleJWKS(t)
		srv.add("google-1", key)
		g := newGoogleVerifier(t, srv, id)
		srv.set(true, false)
		if status, reason := reasonOf(t, g.Verify(context.Background(), unknown("rotated-1"))); status != 503 || reason != "jwks_unavailable" {
			t.Fatalf("the refresh fails: %d %q, want 503 jwks_unavailable", status, reason)
		}
		if status, reason := reasonOf(t, g.Verify(context.Background(), unknown("rotated-2"))); status != 503 || reason != "jwks_refresh_limited" {
			t.Fatalf("no refresh allowed, the last failed: %d %q, want 503 jwks_refresh_limited", status, reason)
		}
		// The keys already loaded still verify.
		if err := g.Verify(context.Background(), signRS(t, jwt.SigningMethodRS256, key, "google-1", id.claims())); err != nil {
			t.Fatalf("a known key during the outage: %v", err)
		}
	})
}

func TestNewGoogleOIDCNeedsTheIdentity(t *testing.T) {
	id := newPushIdentity()
	for name, o := range map[string]GoogleOIDCOptions{
		"no audience": {Email: id.email, Subject: id.subject},
		"no email":    {Audience: id.audience, Subject: id.subject},
		"no subject":  {Audience: id.audience, Email: id.email},
	} {
		if _, err := NewGoogleOIDC(context.Background(), o); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestInsecurePush(t *testing.T) {
	var buf bytes.Buffer
	v := InsecurePush(slog.New(slog.NewJSONHandler(&buf, nil)))
	if !strings.Contains(buf.String(), `"level":"WARN"`) || !strings.Contains(buf.String(), "ALLOW_INSECURE_PUSH") {
		t.Fatalf("no warning at start: %s", buf.String())
	}
	if err := v.VerifyPush(pushRequest("x", "")); err != nil {
		t.Fatalf("insecure accepts a push without a token: %v", err)
	}
}
