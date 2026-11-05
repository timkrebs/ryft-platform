package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// The admin sign-in tests mint real ES256 tokens against a real JWKS (an httptest server) and verify
// them with the same auth.Supabase the services use, so "forged", "expired" and "foreign issuer" are
// what the verifier decides, not what a fake says.

const (
	testAdminKey = "test-admin-key-0123456789abcdef0123456789"
	testIssuer   = "https://testref.supabase.co/auth/v1"
	testKID      = "k1"
)

// jwksStub serves the JWK set of one signing key and can be switched to failing.
type jwksStub struct {
	*httptest.Server
	key     *ecdsa.PrivateKey
	mu      sync.Mutex
	failing bool
}

func newJWKSStub(t *testing.T, failing bool) *jwksStub {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &jwksStub{key: key, failing: failing}
	ecdhPub, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw, enc := ecdhPub.Bytes(), base64.RawURLEncoding.EncodeToString
	jwk := map[string]string{"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": testKID, "x": enc(raw[1:33]), "y": enc(raw[33:])}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failing {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk}})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksStub) verifier(t *testing.T) *auth.Supabase {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := auth.NewSupabase(ctx, auth.SupabaseOptions{Issuer: testIssuer, JWKSURL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// claims are those of a signed-in app user: no operator claim of any kind.
func appClaims(sub uuid.UUID) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": testIssuer, "aud": "authenticated", "sub": sub.String(), "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"role": "authenticated", "is_anonymous": false, "email": "operator@example.test",
		"app_metadata":  map[string]any{"provider": "email"},
		"user_metadata": map[string]any{},
	}
}

// adminClaims are those of an operator who confirmed a TOTP five minutes ago.
func adminClaims(sub uuid.UUID) jwt.MapClaims {
	c := appClaims(sub)
	c["app_metadata"] = map[string]any{"provider": "email", "role": "admin"}
	c["aal"] = "aal2"
	c["amr"] = []map[string]any{{"method": "password", "timestamp": time.Now().Add(-10 * time.Minute).Unix()},
		{"method": "totp", "timestamp": time.Now().Add(-5 * time.Minute).Unix()}}
	return c
}

func (s *jwksStub) sign(t *testing.T, c jwt.MapClaims) string {
	t.Helper()
	return signWith(t, s.key, testKID, c)
}

func signWith(t *testing.T, key *ecdsa.PrivateKey, kid string, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = kid
	out, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeAccounts is the allow-list without a database; err makes the check fail.
type fakeAccounts struct {
	mu    sync.Mutex
	rows  map[uuid.UUID]bool
	err   error
	calls int
}

func newFakeAccounts(admitted ...uuid.UUID) *fakeAccounts {
	f := &fakeAccounts{rows: map[uuid.UUID]bool{}}
	for _, id := range admitted {
		f.rows[id] = true
	}
	return f
}

func (f *fakeAccounts) IsAdminAccount(_ context.Context, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rows[id], f.err
}

func (f *fakeAccounts) set(id uuid.UUID, in bool) {
	f.mu.Lock()
	f.rows[id] = in
	f.mu.Unlock()
}

// countingVerifier counts the tokens that reach the signature check.
type countingVerifier struct {
	next  auth.Verifier
	mu    sync.Mutex
	calls int
}

func (c *countingVerifier) Verify(ctx context.Context, token string) (auth.Principal, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.next.Verify(ctx, token)
}

func (c *countingVerifier) Ready(ctx context.Context) error { return c.next.Ready(ctx) }

func quietHandler() *handler {
	return &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cache: newInsightCache()}
}

// authChain is the bearer check alone (limit not included) in front of a handler that answers 204 and
// reports the actor it was given, for tests that need no database.
func authChain(h *handler, o adminOptions) (http.Handler, *[]adminActor) {
	var actors []adminActor
	var mu sync.Mutex
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		act, ok := adminActorFrom(r.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		actors = append(actors, act)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return newAdminAuth(h, o, func(*http.Request) string { return "" }).require(next), &actors
}

// wide is a limiter no test hits by accident.
func wide() *ratelimit.Limiter { return ratelimit.New(10000, 10000) }

type answer struct {
	status int
	header http.Header
	body   string
}

func (a answer) code() string {
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(a.body), &b)
	return b.Code
}

func do(srv http.Handler, method, path, bearer, body string, hdr ...string) answer {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return answer{rec.Code, rec.Header(), rec.Body.String()}
}

// count reads a counter.
func count(c prometheus.Counter) float64 { return testutil.ToFloat64(c) }

func adminReq(result string) float64 { return count(metrics.AdminRequests.WithLabelValues(result)) }

func adminAuthorized(actor string) prometheus.Counter {
	return metrics.AdminAuthorized.WithLabelValues(actor)
}

func rateLimited(class string) prometheus.Counter { return metrics.RateLimited.WithLabelValues(class) }

var errDown = errors.New("db down")
