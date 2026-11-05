package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// D184: a token with a key id the api does not know, sent while the signing keys were fetched less than a
// minute ago (the refresh limit), is on /v1/admin/* the same 401 as garbage, byte for byte. Anyone can send
// one. The user routes keep auth.classify's 503 (the app must not sign out in a key rotation race).
func TestAdminUnknownKeyIDInsideTheRefreshLimitIs401(t *testing.T) {
	jwks := newJWKSStub(t, false)
	v := jwks.verifier(t)
	op := uuid.New()
	buf := &bytes.Buffer{}
	h := quietHandler()
	h.log = logx.NewWriter(buf, "api", slog.LevelDebug)
	srv, actors := authChain(h, adminOptions{Key: testAdminKey, Verifier: v, Accounts: newFakeAccounts(op)})
	forged := signWith(t, jwks.key, "forged", adminClaims(op))

	garbage := do(srv, http.MethodGet, "/v1/admin/flags", "garbage", "")
	if garbage.status != 401 {
		t.Fatalf("garbage: %d", garbage.status)
	}
	// The first one may refresh the keys (once a minute): still unknown, a 401 from classify itself.
	if a := do(srv, http.MethodGet, "/v1/admin/flags", forged, ""); a.status != 401 {
		t.Fatalf("first forged kid: %d %s", a.status, a.body)
	}
	unauth, errs := adminReq("unauthorized"), adminReq("error")
	limited := count(metrics.AuthFailures.WithLabelValues(refreshLimited))
	buf.Reset()

	// The second is inside the refresh limit: today's 503 jwks_refresh_limited, now the generic 401.
	a := do(srv, http.MethodGet, "/v1/admin/flags", forged, "")
	if a.status != garbage.status || a.body != garbage.body || !sameHeader(a.header, garbage.header) {
		t.Fatalf("forged kid inside the refresh limit: %d %v %q, want garbage's %d %v %q",
			a.status, a.header, a.body, garbage.status, garbage.header, garbage.body)
	}
	if adminReq("unauthorized") != unauth+1 || adminReq("error") != errs {
		t.Error("counted as an error, not as a refused bearer")
	}
	if count(metrics.AuthFailures.WithLabelValues(refreshLimited)) != limited {
		t.Error("an admin 401 is not an auth failure of the user routes (ryft_auth_failures_total stays as it was)")
	}
	if line := buf.String(); !strings.Contains(line, `"msg":"admin auth rejected"`) || !strings.Contains(line, `"reason":"unknown_key"`) {
		t.Errorf("the log names the reason unknown_key: %s", line)
	}
	if len(*actors) != 0 {
		t.Errorf("a forged kid reached the handler: %v", *actors)
	}

	// The classification is still the refresh limit (auth.classify unchanged) ...
	_, err := v.Verify(context.Background(), forged)
	var ae *auth.Error
	if !errors.As(err, &ae) || ae.Status != 503 || ae.Reason != refreshLimited {
		t.Fatalf("auth.classify changed: %v", err)
	}
	// ... and the same token on a user route still gets its 503 with Retry-After.
	user := auth.Middleware(auth.Options{Log: h.log, Verifier: v}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	before := count(metrics.AuthFailures.WithLabelValues(refreshLimited))
	if u := do(user, http.MethodGet, "/v1/today", forged, ""); u.status != 503 || u.header.Get("Retry-After") != "5" {
		t.Fatalf("user route: %d %v %s, want 503 Retry-After 5", u.status, u.header, u.body)
	}
	if count(metrics.AuthFailures.WithLabelValues(refreshLimited)) != before+1 {
		t.Error("the user route counts jwks_refresh_limited as before")
	}
	// The key is not affected.
	if k := do(srv, http.MethodGet, "/v1/admin/flags", testAdminKey, ""); k.status != 204 {
		t.Fatalf("the key: %d", k.status)
	}
}

// D184: while the JWKS cannot be fetched, an unknown key id stays a 503 with Retry-After on the admin routes,
// the first time (jwks_unavailable) and inside the refresh limit (jwks_refresh_limited during an outage).
func TestAdminUnknownKeyIDDuringAJWKSOutageIs503(t *testing.T) {
	jwks := newJWKSStub(t, false)
	v := jwks.verifier(t) // the keys are loaded
	op := uuid.New()
	srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: v, Accounts: newFakeAccounts(op)})
	jwks.mu.Lock()
	jwks.failing = true
	jwks.mu.Unlock()
	forged := signWith(t, jwks.key, "rotated", adminClaims(op))
	for i, want := range []string{"jwks_unavailable", refreshLimited} {
		unauth := adminReq("unauthorized")
		a := do(srv, http.MethodGet, "/v1/admin/flags", forged, "")
		if a.status != 503 || a.header.Get("Retry-After") != "5" || a.header.Get("WWW-Authenticate") != "" {
			t.Fatalf("request %d (%s): %d %v %s, want 503 Retry-After 5", i+1, want, a.status, a.header, a.body)
		}
		if adminReq("unauthorized") != unauth {
			t.Errorf("request %d: an outage is not a refused credential", i+1)
		}
	}
	if !v.FetchFailing() {
		t.Fatal("the stub's outage is not seen by the verifier")
	}
	// A token with the known key still verifies from the cached keys, and the key works.
	if a := do(srv, http.MethodGet, "/v1/admin/flags", jwks.sign(t, adminClaims(op)), ""); a.status != 204 {
		t.Fatalf("a valid operator token during the outage: %d", a.status)
	}
	if a := do(srv, http.MethodGet, "/v1/admin/flags", testAdminKey, ""); a.status != 204 {
		t.Fatalf("the key: %d", a.status)
	}
	if len(*actors) != 2 {
		t.Errorf("handler ran %d times", len(*actors))
	}
}

// fixedVerifier answers every token with err and reports a healthy JWKS fetch.
type fixedVerifier struct{ err error }

func (f fixedVerifier) Verify(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, f.err
}
func (fixedVerifier) Ready(context.Context) error { return nil }
func (fixedVerifier) FetchFailing() bool          { return false }

// Only the refresh limit becomes a 401: every other reason a token cannot be checked stays a 503, even while
// the last JWKS fetch worked (a key set without keys, a failed lookup).
func TestAdminOnlyTheRefreshLimitBecomes401(t *testing.T) {
	for _, err := range []error{
		auth.Unavailable("jwks_unavailable", errors.New("no signing keys loaded")),
		auth.Unavailable("internal", errDown),
		errDown, // not an *auth.Error
	} {
		srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: fixedVerifier{err}, Accounts: newFakeAccounts()})
		if a := do(srv, http.MethodGet, "/v1/admin/flags", "aaa.bbb.ccc", ""); a.status != 503 || a.header.Get("Retry-After") != "5" {
			t.Errorf("%v: %d %v, want 503 Retry-After 5", err, a.status, a.header)
		}
	}
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey,
		Verifier: fixedVerifier{auth.Unavailable(refreshLimited, nil)}, Accounts: newFakeAccounts()})
	if a := do(srv, http.MethodGet, "/v1/admin/flags", "aaa.bbb.ccc", ""); a.status != 401 {
		t.Errorf("the refresh limit with fresh keys: %d, want 401", a.status)
	}
}

// sameHeader compares two answers' headers exactly.
func sameHeader(a, b http.Header) bool { return maps.EqualFunc(a, b, slices.Equal[[]string]) }
