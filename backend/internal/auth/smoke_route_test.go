package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

// D286: the smoke principal reaches only the routes SmokeAllow names, and is refused before the
// rate limit and the user lookup. An app user's request never meets the allowlist.
func TestMiddlewareSmokeRouteAllowlist(t *testing.T) {
	allow := func(r *http.Request) bool { return r.Method == http.MethodGet && r.URL.Path == "/v1/me" }
	run := func(o Options, method, path string) (*httptest.ResponseRecorder, bool) {
		reached := false
		h := Middleware(o, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) }))
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec, reached
	}
	var limited, looked bool
	smoke := Options{
		Log:        discard,
		Verifier:   fakeVerifier{p: Principal{UserID: uuid.New(), Smoke: true}},
		SmokeAllow: allow,
		Limit: func(*http.Request, Principal) (string, bool, time.Duration) {
			limited = true
			return "default", true, 0
		},
		OnAuth: func(ctx context.Context, _ Principal) (context.Context, error) {
			looked = true
			return ctx, nil
		},
	}

	if rec, reached := run(smoke, http.MethodGet, "/v1/me"); rec.Code != http.StatusNoContent || !reached || !limited || !looked {
		t.Fatalf("an allowed route passes through the limit and the user lookup: %d reached=%v limited=%v looked=%v", rec.Code, reached, limited, looked)
	}

	before := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("smoke_route"))
	limited, looked = false, false
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/me"}, {http.MethodPost, "/v1/me"}, {http.MethodGet, "/v1/me/export"}, {http.MethodGet, "/v1/other"},
	} {
		rec, reached := run(smoke, c.method, c.path)
		if rec.Code != http.StatusForbidden || reached {
			t.Fatalf("%s %s: want 403 and no handler, got %d reached=%v", c.method, c.path, rec.Code, reached)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "forbidden" || body["code"] != "" {
			t.Fatalf("%s %s: body %q %v", c.method, c.path, rec.Body.String(), err)
		}
		if rec.Header().Get("WWW-Authenticate") != "" || rec.Header().Get("Retry-After") != "" {
			t.Fatalf("a 403 is no challenge and no retry: %v", rec.Header())
		}
	}
	if limited || looked {
		t.Fatalf("a refused route must cost no limit check or lookup: limited=%v looked=%v", limited, looked)
	}
	if got := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("smoke_route")) - before; got != 4 {
		t.Fatalf("smoke_route counted %v times, want 4", got)
	}

	// No allowlist at all: every route is refused.
	none := smoke
	none.SmokeAllow = nil
	if rec, reached := run(none, http.MethodGet, "/v1/me"); rec.Code != http.StatusForbidden || reached {
		t.Fatalf("a nil allowlist refuses the smoke: %d reached=%v", rec.Code, reached)
	}

	// An app user is not affected by the allowlist, with or without one.
	app := Options{Log: discard, Verifier: fakeVerifier{p: Principal{UserID: uuid.New()}}, SmokeAllow: allow}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if rec, reached := run(app, method, "/v1/me/export"); rec.Code != http.StatusNoContent || !reached {
			t.Fatalf("app user %s: %d reached=%v", method, rec.Code, reached)
		}
	}
	app.SmokeAllow = nil
	if rec, reached := run(app, http.MethodDelete, "/v1/me"); rec.Code != http.StatusNoContent || !reached {
		t.Fatalf("app user without an allowlist: %d reached=%v", rec.Code, reached)
	}
}
