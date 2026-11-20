package gate

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/appattest"
	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// appAttestLine is the warn line's keys (decision 8); the standard keys come from logcheck.Standard,
// and the test below asserts user_id is not among them.
var appAttestLine = logcheck.Lines{"app attest": {
	"code":        logcheck.Enum(model.CodeAppAttestRequired),
	"reason":      logcheck.Enum("missing", "invalid", "expired", "other_account"),
	"mode":        logcheck.Enum("warn", "on"),
	"route_group": logcheck.Enum("sync", "train", "journal", "account", "config", "reads", "other"),
	"method":      logcheck.Enum("GET", "POST", "PUT", "PATCH", "DELETE"),
	"app_build":   logcheck.Pattern(regexp.MustCompile(`^$|^[0-9]+(\.[0-9]+){0,3}$`)),
	"app_device":  logcheck.Bool,
}}

var (
	aaNow   = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	aaUser  = uuid.MustParse("6f1c2b3a-4d5e-4f60-8a7b-9c0d1e2f3a4b")
	aaOther = uuid.MustParse("0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d")
	aaKeyID = [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
)

func aaRing(t *testing.T) *appattest.Ring {
	t.Helper()
	r, err := appattest.ParseRing(appattesttest.RingLine(3, 0xa))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// probe answers 200 "served" and counts its calls.
type probe struct{ calls int }

func (p *probe) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	p.calls++
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("served"))
}

// aaRequest is a request as auth.Middleware hands it on: the principal in the context, and logx
// fields that carry the user id (which the warn line must not pick up).
func aaRequest(method, path, token string, user uuid.UUID, headers ...string) (*http.Request, string) {
	r := httptest.NewRequest(method, path, nil)
	ctx, f := logx.NewContext(r.Context())
	f.RequestID, f.UserID, f.Route = uuid.NewString(), user.String(), "GET /v1/days"
	r = r.WithContext(auth.WithPrincipal(ctx, auth.Principal{UserID: user}))
	if token != "" {
		r.Header.Set(model.HeaderAppToken, token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r, f.RequestID
}

func refusals(reason, service string) float64 {
	return testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues(reason, service))
}

// Off is the next handler itself: no header is read, a garbage token changes nothing, nothing is
// logged or counted.
func TestAppAttestOffIsNext(t *testing.T) {
	next := &probe{}
	var logs bytes.Buffer
	h := AppAttest(AppAttestOptions{Config: config.AppAttest{Mode: config.ModeOff}, Service: "gtest",
		Log: logx.NewWriter(&logs, "api", slog.LevelDebug)}, next)
	if h != http.Handler(next) {
		t.Fatal("off wraps the handler: it must be next itself")
	}
	r, _ := aaRequest(http.MethodGet, "/v1/days", "garbage\x00token", aaUser)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || next.calls != 1 || logs.Len() != 0 || refusals("invalid", "gtest") != 0 {
		t.Fatalf("off: %d, calls %d, logs %q", rec.Code, next.calls, logs.String())
	}
}

// Warn serves every request unchanged, and for each reason writes one warn line and counts it; on
// refuses the same requests with 403 app_attest_required. A valid token passes both silently.
func TestAppAttestWarnAndOn(t *testing.T) {
	ring := aaRing(t)
	valid, _ := ring.Mint(aaUser, aaKeyID, aaNow)
	old, _ := ring.Mint(aaUser, aaKeyID, aaNow.Add(-2*appattest.TokenTTL))
	others, _ := ring.Mint(aaOther, aaKeyID, aaNow)
	tokens := map[string]string{"missing": "", "invalid": "v1.0000000a.not.a-token", "expired": old, "other_account": others}
	for _, mode := range []config.Mode{config.ModeWarn, config.ModeOn} {
		for reason, tok := range tokens {
			t.Run(string(mode)+" "+reason, func(t *testing.T) {
				next := &probe{}
				var logs bytes.Buffer
				svc := "gtest-" + string(mode)
				h := AppAttest(AppAttestOptions{Config: config.AppAttest{Mode: mode, Ring: ring}, Service: svc,
					Log: logx.NewWriter(&logs, "api", slog.LevelDebug), Now: func() time.Time { return aaNow }}, next)
				before := refusals(reason, svc)
				r, rid := aaRequest(http.MethodPut, "/v1/sessions/"+uuid.NewString(), tok, aaUser,
					model.HeaderAppBuild, "20261119.091500", deviceHeader, "6f1c2e3a-8b4d-4f5e-9a7b-1c2d3e4f5a6b")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if got := refusals(reason, svc) - before; got != 1 {
					t.Errorf("ryft_app_attest_refusals_total{%s,%s} rose by %v", reason, svc, got)
				}
				if mode == config.ModeWarn {
					if rec.Code != 200 || rec.Body.String() != "served" || next.calls != 1 || len(rec.Header()) != 0 {
						t.Fatalf("warn changed the answer: %d %q %v", rec.Code, rec.Body, rec.Header())
					}
				} else {
					var body map[string]string
					if rec.Code != 403 || next.calls != 0 || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
						body["code"] != model.CodeAppAttestRequired || body["error"] != msgAppAttestRequired {
						t.Fatalf("on: %d %s, calls %d", rec.Code, rec.Body, next.calls)
					}
				}
				line := oneWarnLine(t, logs.String(), tok)
				want := map[string]any{"msg": "app attest", "level": "WARN", "service": "api", "code": "app_attest_required",
					"reason": reason, "mode": string(mode), "route_group": "train", "method": "PUT",
					"app_build": "20261119.091500", "app_device": true, "request_id": rid}
				for k, v := range want {
					if line[k] != v {
						t.Errorf("%s = %v, want %v", k, line[k], v)
					}
				}
			})
		}
		t.Run(string(mode)+" valid", func(t *testing.T) {
			next := &probe{}
			var logs bytes.Buffer
			h := AppAttest(AppAttestOptions{Config: config.AppAttest{Mode: mode, Ring: ring}, Service: "gtest-valid",
				Log: logx.NewWriter(&logs, "api", slog.LevelDebug), Now: func() time.Time { return aaNow }}, next)
			r, _ := aaRequest(http.MethodGet, "/v1/days", valid, aaUser)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != 200 || next.calls != 1 || logs.Len() != 0 {
				t.Fatalf("a valid token: %d, calls %d, logs %q", rec.Code, next.calls, logs.String())
			}
		})
	}
}

// oneWarnLine checks the log is exactly one "app attest" line of the allowed keys, without user_id,
// the token, its kid, a key id or the device id's value, and returns it.
func oneWarnLine(t *testing.T, out, token string) map[string]any {
	t.Helper()
	forbid := []string{aaUser.String(), "user_id", "client_request_id", `"route"`, "0000000a", "6f1c2e3a-8b4d", "X-RYFT"}
	if token != "" {
		forbid = append(forbid, token, token[len(token)-20:])
	}
	lines, problems := logcheck.Check(out, logcheck.Standard("api"), appAttestLine, logcheck.Forbidden{Texts: forbid})
	for _, p := range problems {
		t.Error(p)
	}
	if len(lines["app attest"]) != 1 || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("want one app attest line:\n%s", out)
	}
	return lines["app attest"][0]
}

// The line goes out without the request's fields even though the same logger, given the request's
// context, adds user_id: the test above would pass vacuously otherwise.
func TestAppAttestLineIsNotTheRequestContext(t *testing.T) {
	var logs bytes.Buffer
	log := logx.NewWriter(&logs, "api", slog.LevelDebug)
	r, _ := aaRequest(http.MethodGet, "/v1/days", "", aaUser)
	log.InfoContext(r.Context(), "control")
	if !strings.Contains(logs.String(), aaUser.String()) {
		t.Fatalf("the request's context carries no user_id, so the warn line test proves nothing: %s", logs.String())
	}
}

// Exempt accounts and exempt routes pass under on without a token, uncounted and unlogged; the
// account's exemption is by the principal, not by a header.
func TestAppAttestExempt(t *testing.T) {
	ring := aaRing(t)
	var logs bytes.Buffer
	next := &probe{}
	h := AppAttest(AppAttestOptions{
		Config:  config.AppAttest{Mode: config.ModeOn, Ring: ring, ExemptUsers: []uuid.UUID{aaOther}},
		Service: "gtest-exempt", Log: logx.NewWriter(&logs, "api", slog.LevelDebug),
		Exempt: func(r *http.Request) bool { return r.Method == http.MethodPost && r.URL.Path == "/v1/feedback" },
	}, next)
	for _, c := range []struct {
		name         string
		method, path string
		user         uuid.UUID
		want         int
	}{
		{"exempt account", http.MethodGet, "/v1/days", aaOther, 200},
		{"exempt route", http.MethodPost, "/v1/feedback", aaUser, 200},
		{"the route with another method", http.MethodGet, "/v1/feedback", aaUser, 403},
		{"neither", http.MethodGet, "/v1/days", aaUser, 403},
	} {
		r, _ := aaRequest(c.method, c.path, "", c.user)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
	if next.calls != 2 || strings.Count(logs.String(), `"msg":"app attest"`) != 2 {
		t.Fatalf("calls %d, lines:\n%s", next.calls, logs.String())
	}
}

// Without a principal (a wiring mistake: outside auth.Middleware) no token is any account's.
func TestAppAttestWithoutPrincipalFailsClosed(t *testing.T) {
	ring := aaRing(t)
	tok, _ := ring.Mint(aaUser, aaKeyID, aaNow)
	h := AppAttest(AppAttestOptions{Config: config.AppAttest{Mode: config.ModeOn, Ring: ring}, Service: "gtest-np",
		Now: func() time.Time { return aaNow }}, &probe{})
	r := httptest.NewRequest(http.MethodGet, "/v1/days", nil)
	r.Header.Set(model.HeaderAppToken, tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("no principal: %d", rec.Code)
	}
}

func TestAppBuild(t *testing.T) {
	for v, want := range map[string]string{
		"": "", "20261119.091500": "20261119.091500", "1": "1", "1.2.3.4": "1.2.3.4", "1.2.3.4.5": "",
		"1.": "", ".1": "", "1..2": "", "v1": "", "1 2": "", "١٢": "", strings.Repeat("1", 24): strings.Repeat("1", 24),
		strings.Repeat("1", 25): "", "12345678901.12345678901.1": "", "61.7\n": "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/days", nil)
		r.Header.Set(model.HeaderAppBuild, v)
		if got := AppBuild(r); got != want {
			t.Errorf("AppBuild(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestRouteGroup(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/sync": "sync", "/v1/sessions/abc": "train", "/v1/plans": "train", "/v1/exercises/x/history": "train",
		"/v1/train/import": "train", "/v1/journal/2026-11-19": "journal", "/v1/behaviors/impact": "journal",
		"/v1/me": "account", "/v1/me/sync": "account", "/v1/devices/x/push": "account", "/v1/config": "config",
		"/v1/model": "config", "/v1/days/2026-11-19": "reads", "/v1/sleep/plan": "reads", "/v1/workouts": "reads",
		"/v1/trends": "reads", "/v1/insights": "reads", "/v1/recompute": "reads", "/v1/unknown": "other",
		"/v1/": "other", "/v2/days": "other", "/v1/syncs": "other",
	} {
		if got := RouteGroup(path); got != want {
			t.Errorf("RouteGroup(%q) = %q, want %q", path, got, want)
		}
	}
}
