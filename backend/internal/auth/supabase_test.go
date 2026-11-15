package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

const testIssuer = "https://testref.supabase.co/auth/v1"

// jwksServer serves a mutable JWK Set and can be switched to failing, to a 200
// whose body is not a key set (garbage), or to answering slowly (delay). hits
// counts requests.
type jwksServer struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	keys    map[string]*ecdsa.PrivateKey
	failing bool
	garbage bool
	delay   time.Duration
}

func newJWKSServer(t *testing.T) *jwksServer {
	s := &jwksServer{keys: map[string]*ecdsa.PrivateKey{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		delay := s.delay
		s.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case s.failing:
			http.Error(w, "down", http.StatusBadGateway)
			return
		case s.garbage:
			_, _ = w.Write([]byte("<html>not a key set</html>"))
			return
		}
		var set struct {
			Keys []map[string]string `json:"keys"`
		}
		for kid, k := range s.keys {
			set.Keys = append(set.Keys, jwkOf(t, kid, &k.PublicKey))
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) addKey(t *testing.T, kid string) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.keys[kid] = k
	s.mu.Unlock()
	return k
}

func (s *jwksServer) setFailing(v bool) {
	s.mu.Lock()
	s.failing = v
	s.mu.Unlock()
}

func (s *jwksServer) set(f func(*jwksServer)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

func jwkOf(t *testing.T, kid string, pub *ecdsa.PublicKey) map[string]string {
	ecdhPub, err := pub.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw := ecdhPub.Bytes() // 0x04 || X || Y
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]string{"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
		"x": enc(raw[1:33]), "y": enc(raw[33:])}
}

func newVerifier(t *testing.T, srv *jwksServer, o SupabaseOptions) *Supabase {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	o.Issuer, o.JWKSURL = testIssuer, srv.URL
	v, err := NewSupabase(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// claims returns valid Supabase access-token claims for a fresh user.
func claims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": testIssuer, "aud": "authenticated", "sub": uuid.NewString(),
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"role": "authenticated", "is_anonymous": false, "email": "tim@example.com",
		"app_metadata":  map[string]any{"provider": "apple"},
		"user_metadata": map[string]any{"full_name": "Tim K"},
	}
}

func sign(t *testing.T, key *ecdsa.PrivateKey, kid string, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("want *auth.Error, got %T %v", err, err)
	}
	return ae.Status, ae.Reason
}

func TestSupabaseAcceptsValidToken(t *testing.T) {
	srv := newJWKSServer(t)
	key := srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	if err := v.Ready(context.Background()); err != nil {
		t.Fatalf("keys should be loaded: %v", err)
	}
	c := claims()
	p, err := v.Verify(context.Background(), sign(t, key, "k1", c))
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID.String() != c["sub"] || p.Email != "tim@example.com" || p.Provider != "apple" || p.Name != "Tim K" {
		t.Fatalf("principal %+v", p)
	}
}

func TestSupabaseReadsTheAppleSubjectOnlyFromApple(t *testing.T) {
	srv := newJWKSServer(t)
	key := srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	c := claims()
	c["user_metadata"] = map[string]any{"iss": "https://appleid.apple.com", "provider_id": "001234.abc", "full_name": "Tim"}
	if p, err := v.Verify(context.Background(), sign(t, key, "k1", c)); err != nil || p.AppleSubject != "001234.abc" {
		t.Fatalf("apple: %+v %v", p, err)
	}
	c = claims()
	c["user_metadata"] = map[string]any{"iss": "https://accounts.google.com", "provider_id": "999"}
	if p, _ := v.Verify(context.Background(), sign(t, key, "k1", c)); p.AppleSubject != "" {
		t.Fatalf("another provider's ID is not an Apple subject: %q", p.AppleSubject)
	}
}

func TestSupabaseRejectsBadTokens(t *testing.T) {
	srv := newJWKSServer(t)
	key := srv.addKey(t, "k1")
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := newVerifier(t, srv, SupabaseOptions{})

	with := func(mut func(jwt.MapClaims)) jwt.MapClaims { c := claims(); mut(c); return c }
	pubBytes, _ := key.PublicKey.ECDH()

	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, claims()) // alg confusion: HMAC keyed with the public key
	hs.Header["kid"] = "k1"
	forged, _ := hs.SignedString(pubBytes.Bytes())
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, claims()).SignedString(jwt.UnsafeAllowNoneSignatureType)

	cases := map[string]struct {
		token  string
		reason string
	}{
		"expired":          {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })), "expired"},
		"wrong issuer":     {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["iss"] = "https://evil.example/auth/v1" })), "issuer"},
		"wrong audience":   {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["aud"] = "anon" })), "audience"},
		"anon role":        {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["role"] = "anon" })), "role"},
		"service role":     {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["role"] = "service_role" })), "role"},
		"anonymous user":   {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["is_anonymous"] = true })), "anonymous"},
		"subject not uuid": {sign(t, key, "k1", with(func(c jwt.MapClaims) { c["sub"] = "admin" })), "subject"},
		"no expiry":        {sign(t, key, "k1", with(func(c jwt.MapClaims) { delete(c, "exp") })), "invalid"},
		"foreign key":      {sign(t, stranger, "k1", claims()), "signature"},
		"HS256 forged":     {forged, "signature"},
		"alg none":         {none, "signature"},
		"garbage":          {"not.a.jwt", "malformed"},
	}
	for name, c := range cases {
		_, err := v.Verify(context.Background(), c.token)
		status, reason := statusOf(t, err)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status %d (%v), want 401", name, status, err)
			continue
		}
		if reason != c.reason {
			t.Errorf("%s: reason %q, want %q (%v)", name, reason, c.reason, err)
		}
	}
}

func TestSupabaseRefreshesOnUnknownKID(t *testing.T) {
	srv := newJWKSServer(t)
	srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	rotated := srv.addKey(t, "k2") // Supabase rotated its signing key after we fetched
	if _, err := v.Verify(context.Background(), sign(t, rotated, "k2", claims())); err != nil {
		t.Fatalf("a token from the rotated key must verify after a refresh: %v", err)
	}
}

func TestSupabaseUnknownKeyIsUnauthorized(t *testing.T) {
	srv := newJWKSServer(t)
	srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := v.Verify(context.Background(), sign(t, other, "nope", claims()))
	if status, reason := statusOf(t, err); status != 401 || reason != "unknown_key" {
		t.Fatalf("got %d %s", status, reason)
	}
}

func TestSupabaseUnavailableWithoutKeys(t *testing.T) {
	srv := newJWKSServer(t)
	srv.setFailing(true)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := newVerifier(t, srv, SupabaseOptions{})
	if v.Ready(context.Background()) == nil {
		t.Fatal("not ready without keys")
	}
	_, err := v.Verify(context.Background(), sign(t, key, "k1", claims()))
	if status, reason := statusOf(t, err); status != 503 || reason != "jwks_unavailable" {
		t.Fatalf("an outage must be 503 (retry), never 401 (sign-out): got %d %s", status, reason)
	}
}

// A cold instance whose first fetch failed must not wait for the 10-minute refresh: while
// no keys are loaded, a token fetches the set again, at most once per NoKeysEvery for the
// whole verifier (every other token meanwhile gets its 503 without a fetch). Once the JWKS
// answers, tokens verify again within that limit. As TestGoogleOIDCFetchesWhileItHasNoKeys.
func TestSupabaseFetchesWhileItHasNoKeys(t *testing.T) {
	const every = time.Second
	srv := newJWKSServer(t)
	srv.setFailing(true)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := newVerifier(t, srv, SupabaseOptions{NoKeysEvery: every})
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches at construction, want 1", n)
	}
	token := sign(t, key, "k1", claims())
	verify := func() (int, string) {
		_, err := v.Verify(context.Background(), token)
		if err == nil {
			return 0, ""
		}
		return statusOf(t, err)
	}

	start := time.Now()
	if status, reason := verify(); status != 503 || reason != "jwks_unavailable" || srv.hits.Load() != 2 {
		t.Fatalf("the JWKS still fails: %d %q after %d fetches, want 503 jwks_unavailable after a fetch", status, reason, srv.hits.Load())
	}
	if status, _ := verify(); status != 503 || srv.hits.Load() != 2 {
		t.Fatalf("a second token inside the limit: %d after %d fetches, want 503 and no fetch", status, srv.hits.Load())
	}
	srv.set(func(s *jwksServer) { s.keys["k1"] = key; s.failing = false })
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
	if err := v.Ready(context.Background()); err != nil {
		t.Fatalf("keys loaded: %v", err)
	}
	if v.noKeys.Limit() != rate.Every(every) {
		t.Fatalf("limit %v", v.noKeys.Limit())
	}
	if d := newVerifier(t, srv, SupabaseOptions{}); d.noKeys.Limit() != rate.Every(10*time.Second) {
		t.Fatalf("default limit %v, want every 10 s", d.noKeys.Limit())
	}
}

// That fetch is bounded by the request: a JWKS that hangs costs a request at most its own
// deadline (and never more than noKeysFetchMax), not the client's 10 s.
func TestSupabaseNoKeysFetchIsBoundedByTheRequest(t *testing.T) {
	srv := newJWKSServer(t)
	srv.setFailing(true) // the construction's fetch fails fast
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := newVerifier(t, srv, SupabaseOptions{})
	srv.set(func(s *jwksServer) { s.keys["k1"] = key; s.failing = false; s.delay = 10 * time.Second })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := v.Verify(ctx, sign(t, key, "k1", claims()))
	if status, reason := statusOf(t, err); status != 503 || reason != "jwks_unavailable" {
		t.Fatalf("got %d %q", status, reason)
	}
	if took := time.Since(start); took > 2*time.Second || srv.hits.Load() != 2 {
		t.Fatalf("took %s with %d fetches: want the request's 100 ms and one fetch", took, srv.hits.Load())
	}
}

// A client that hangs up during that fetch does not cut it (the limiter allowed only it):
// the keys arrive, and the next token verifies without waiting for the limit.
func TestSupabaseNoKeysFetchOutlivesAHangUp(t *testing.T) {
	srv := newJWKSServer(t)
	srv.setFailing(true)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := newVerifier(t, srv, SupabaseOptions{NoKeysEvery: time.Hour})
	srv.set(func(s *jwksServer) { s.keys["k1"] = key; s.failing = false; s.delay = 150 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := v.Verify(ctx, sign(t, key, "k1", claims())); err != nil || ctx.Err() == nil {
		t.Fatalf("the fetch must outlive the hang-up (cancelled %v): %v", ctx.Err() != nil, err)
	}
	if err := v.Ready(context.Background()); err != nil {
		t.Fatalf("the fetch ended with the client: %v", err)
	}
	srv.set(func(s *jwksServer) { s.delay = 0 })
	if _, err := v.Verify(context.Background(), sign(t, key, "k1", claims())); err != nil {
		t.Fatalf("the next token: %v", err)
	}
	if n := srv.hits.Load(); n != 2 {
		t.Fatalf("%d fetches, want 2: construction and the one the limit allowed", n)
	}
}

func TestSupabaseUnknownKeyDuringOutageIsUnavailable(t *testing.T) {
	srv := newJWKSServer(t)
	srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	srv.setFailing(true)
	rotated := srv.addKey(t, "k2")
	_, err := v.Verify(context.Background(), sign(t, rotated, "k2", claims()))
	if status, _ := statusOf(t, err); status != 503 {
		t.Fatalf("an unknown key while the JWKS is down must be 503, got %d (%v)", status, err)
	}
}

func TestSupabaseRefreshLimitIsUnavailable(t *testing.T) {
	srv := newJWKSServer(t)
	srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{UnknownKIDEvery: time.Hour})
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, first := v.Verify(context.Background(), sign(t, other, "x1", claims()))
	if status, _ := statusOf(t, first); status != 401 {
		t.Fatalf("first unknown kid: one refresh, still unknown → 401, got %d", status)
	}
	start := time.Now()
	_, second := v.Verify(context.Background(), sign(t, other, "x2", claims()))
	if status, reason := statusOf(t, second); status != 503 || reason != "jwks_refresh_limited" {
		t.Fatalf("second unknown kid within the limit: got %d %s", status, reason)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a rate-limited lookup must not park the request")
	}
}
