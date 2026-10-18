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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testIssuer = "https://testref.supabase.co/auth/v1"

// jwksServer serves a mutable JWK Set and can be switched to failing.
type jwksServer struct {
	*httptest.Server
	mu      sync.Mutex
	keys    map[string]*ecdsa.PrivateKey
	failing bool
}

func newJWKSServer(t *testing.T) *jwksServer {
	s := &jwksServer{keys: map[string]*ecdsa.PrivateKey{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
