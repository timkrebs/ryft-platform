package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

// What counts as a failed JWKS fetch decides between 401 unknown_key (sign in again) and 503 (retry) for an
// unknown key id, on every route that verifies a Supabase token (api, ingest). D184.

func failures(reason string) float64 {
	return testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(reason))
}

// userRoute is auth.Middleware in front of a 204, as the user routes use it.
func userRoute(v Verifier) http.Handler {
	return Middleware(Options{Log: discard, Verifier: v}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func call(h http.Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func otherKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A client that hangs up while the refresh its unknown key id started is running neither cuts that refresh
// short nor counts as a failed fetch: the rotated key is loaded, nothing is jwks_unavailable, and the
// next tokens are answered as with a healthy JWKS.
func TestSupabaseClientHangUpIsNotAFailedFetch(t *testing.T) {
	jw := newJWKSServer(t)
	jw.addKey(t, "k1")
	v := newVerifier(t, jw, SupabaseOptions{})
	api := httptest.NewServer(userRoute(v))
	t.Cleanup(api.Close)
	rotated := jw.addKey(t, "k2")                                    // Supabase rotated after our fetch
	jw.set(func(s *jwksServer) { s.delay = 150 * time.Millisecond }) // a healthy, not instant JWKS
	unavailable := failures("jwks_unavailable")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, rotated, "k2", claims()))
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatalf("the client was meant to hang up first, got %d", res.StatusCode)
	}
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := v.kf.Storage().KeyRead(context.Background(), "k2"); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the refresh ended with the client: the rotated key was never loaded (Verify must detach the key lookup)")
		}
	}
	if v.fetches.lastFailed() {
		t.Error("a client hang-up was recorded as a failed JWKS fetch")
	}
	if failures("jwks_unavailable") != unavailable {
		t.Error("a client hang-up counted as jwks_unavailable (AuthUnavailable)")
	}
	jw.set(func(s *jwksServer) { s.delay = 0 })
	if rec := call(userRoute(v), sign(t, rotated, "k2", claims())); rec.Code != http.StatusNoContent {
		t.Fatalf("a token of the rotated key after the hang-up: %d", rec.Code)
	}
	// The minute's refresh is used: an unknown key id now is the user routes' 503, as before.
	limited := failures("jwks_refresh_limited")
	rec := call(userRoute(v), sign(t, otherKey(t), "x9", claims()))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || failures("jwks_refresh_limited") != limited+1 {
		t.Fatalf("unknown kid inside the refresh limit: %d %v", rec.Code, rec.Header())
	}
}

// A refresh cut short because its own context was cancelled (the service stopping) is not a failed
// fetch; one that ran out of time, or any other error, is.
func TestFetchHealthIgnoresOnlyCancellation(t *testing.T) {
	for _, c := range []struct {
		err  error
		fail bool
	}{
		{fmt.Errorf("refresh: %w", &url.Error{Op: "Get", URL: "https://x", Err: context.Canceled}), false},
		{fmt.Errorf("refresh: %w", &url.Error{Op: "Get", URL: "https://x", Err: context.DeadlineExceeded}), true},
		{fmt.Errorf("refresh: invalid HTTP status code: 502"), true},
		{fmt.Errorf("failed to decode JWK Set response: %w", fmt.Errorf("invalid character '<'")), true},
	} {
		var h fetchHealth
		h.lastOK.Store(1)
		h.failed(c.err)
		if h.lastFailed() != c.fail {
			t.Errorf("%v: failed %v, want %v", c.err, h.lastFailed(), c.fail)
		}
	}
}

// A 200 whose body is not a key set is a failed refresh: an unknown key id is then a 503, not a sign-out.
func TestSupabaseUndecodableKeySetIsAFailedFetch(t *testing.T) {
	jw := newJWKSServer(t)
	jw.addKey(t, "k1")
	v := newVerifier(t, jw, SupabaseOptions{})
	jw.set(func(s *jwksServer) { s.garbage = true })
	unavailable := failures("jwks_unavailable")
	rec := call(userRoute(v), sign(t, otherKey(t), "rotated", claims()))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("an unknown kid while the JWKS answers 200 with garbage: %d %v", rec.Code, rec.Header())
	}
	if !v.fetches.lastFailed() || failures("jwks_unavailable") != unavailable+1 {
		t.Error("the undecodable key set is not a failed fetch")
	}
}

// A JWKS slower than the 1 s an unknown key id may wait is a failed fetch, and the detached lookup stays
// bounded by it.
func TestSupabaseSlowJWKSIsAFailedFetchAndBounded(t *testing.T) {
	jw := newJWKSServer(t)
	jw.addKey(t, "k1")
	v := newVerifier(t, jw, SupabaseOptions{})
	jw.set(func(s *jwksServer) { s.delay = 3 * time.Second })
	start := time.Now()
	rec := call(userRoute(v), sign(t, otherKey(t), "rotated", claims()))
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the detached key lookup took %v", took)
	}
	if rec.Code != http.StatusServiceUnavailable || !v.fetches.lastFailed() {
		t.Fatalf("a JWKS that does not answer within 1 s: %d, failed %v", rec.Code, v.fetches.lastFailed())
	}
}

// The user routes answer as before for a valid token, an expired one, an unknown key id (first a refresh,
// then the refresh limit) and a genuine outage.
func TestMiddlewareWithSupabaseIsUnchanged(t *testing.T) {
	jw := newJWKSServer(t)
	k1 := jw.addKey(t, "k1")
	v := newVerifier(t, jw, SupabaseOptions{})
	h := userRoute(v)
	expired := claims()
	expired["exp"], expired["iat"] = time.Now().Add(-time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix()
	forged := sign(t, otherKey(t), "forged", claims())
	for _, c := range []struct {
		name, token, reason string
		status              int
	}{
		{"valid", sign(t, k1, "k1", claims()), "", http.StatusNoContent},
		{"expired", sign(t, k1, "k1", expired), "expired", http.StatusUnauthorized},
		{"unknown kid, refreshed", forged, "unknown_key", http.StatusUnauthorized},
		{"unknown kid inside the refresh limit", forged, "jwks_refresh_limited", http.StatusServiceUnavailable},
	} {
		var before float64
		if c.reason != "" {
			before = failures(c.reason)
		}
		rec := call(h, c.token)
		if rec.Code != c.status {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.status)
		}
		if c.reason != "" && failures(c.reason) != before+1 {
			t.Errorf("%s: not counted as %s", c.name, c.reason)
		}
		switch c.status {
		case http.StatusUnauthorized:
			if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Errorf("%s: %v", c.name, rec.Header())
			}
		case http.StatusServiceUnavailable:
			if rec.Header().Get("Retry-After") != "5" || rec.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("%s: %v", c.name, rec.Header())
			}
		}
	}

	// A genuine outage: the refresh fails, which is recorded and answered 503 jwks_unavailable.
	out := newJWKSServer(t)
	out.addKey(t, "k1")
	ov := newVerifier(t, out, SupabaseOptions{})
	out.setFailing(true)
	unavailable := failures("jwks_unavailable")
	if rec := call(userRoute(ov), forged); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("unknown kid during an outage: %d %v", rec.Code, rec.Header())
	}
	if !ov.fetches.lastFailed() || failures("jwks_unavailable") != unavailable+1 {
		t.Error("a failing fetch is not recorded")
	}
	// No keys at all: 503 jwks_unavailable as well.
	none := newJWKSServer(t)
	none.setFailing(true)
	unavailable = failures("jwks_unavailable")
	if rec := call(userRoute(newVerifier(t, none, SupabaseOptions{})), sign(t, k1, "k1", claims())); rec.Code != http.StatusServiceUnavailable ||
		failures("jwks_unavailable") != unavailable+1 {
		t.Fatalf("no keys: %d", rec.Code)
	}
}
