package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// testPostHogKey is a project key of the right form, built at run time (no key-shaped literal
// in the repository for the secret scanners to report).
func testPostHogKey() string { return "phc_" + strings.Repeat("Zy9", 15) }

// GET /v1/config through the api's own routes and chain (newMux, Instrument at debug): analytics is
// null without a project key and {host, project_key} with one, beside the flag analytics.enabled,
// and no log line of the request carries the key (D231).
func TestRemoteConfigAnalytics(t *testing.T) {
	pool := testPool(t)
	key := testPostHogKey()
	for _, c := range []struct {
		name string
		cfg  config.Analytics
		want *model.AnalyticsConfig
	}{
		{"no key: null", config.Analytics{Host: config.DefaultPostHogHost}, nil},
		{"a key: the object", config.Analytics{Host: config.DefaultPostHogHost, ProjectKey: key},
			&model.AnalyticsConfig{Host: "https://eu.i.posthog.com", ProjectKey: key}},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			log := logx.NewWriter(logs, "api", slog.LevelDebug)
			prev := slog.Default()
			slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
			t.Cleanup(func() { slog.SetDefault(prev) })
			h := &handler{st: store.New(pool), log: log, beta: defaultBeta(), analytics: analyticsConfig(c.cfg)}
			const apiKey = "analytics-test-key-0123456789"
			mux := newMux(h, muxOptions{log: log, verifier: auth.NewAPIKey(apiKey, uuid.New()),
				ready: func(context.Context) error { return nil }, tick: http.NotFoundHandler(),
				gate: config.ModeOff, authMode: config.AuthAPIKey})
			srv := httpx.Instrument(log, httpx.Recover(httpx.SecurityHeaders(mux)))

			req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
			req.Header.Set("Authorization", "Bearer "+apiKey+":"+uuid.NewString())
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /v1/config: %d %s", rec.Code, rec.Body)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			got, present := raw["analytics"]
			if !present {
				t.Fatal("analytics is missing: it is always present, null or the object")
			}
			if c.want == nil && string(got) != "null" {
				t.Fatalf("analytics = %s, want null", got)
			}
			if c.want != nil {
				var a model.AnalyticsConfig
				if err := json.Unmarshal(got, &a); err != nil || a != *c.want {
					t.Fatalf("analytics = %s (%v), want %+v", got, err, *c.want)
				}
			}
			var cfg model.RemoteConfig
			if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
				t.Fatal(err)
			}
			if on, ok := cfg.Flags["analytics.enabled"]; !ok || on {
				t.Errorf("analytics.enabled = %v (present %v): it ships dark, off for an account not on the allow-list", on, ok)
			}
			// The request's one line is the access line, with the standard keys only, and no line
			// holds the key or any part of it that would identify it.
			lines, problems := logcheck.Check(logs.String(), logcheck.Standard("api"), logcheck.Lines{"http": logcheck.HTTP()},
				logcheck.Forbidden{Texts: []string{key, key[4:], key[4:20], "phc_"}})
			for _, p := range problems {
				t.Error(p)
			}
			if len(lines["http"]) != 1 {
				t.Fatalf("want the request's one access line, so the scan proves something:\n%s", logs.String())
			}
		})
	}
}

// A POSTHOG_HOST that is not https stops the api before it connects to anything (the database is
// a closed port), and the error quotes neither the host nor the key. A good configuration logs one
// "analytics" line that says a key is set, without it. The child gets only this environment.
func TestMainAnalyticsConfig(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	key := testPostHogKey()
	run := func(host string, wait time.Duration) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainAnalyticsConfig$")
		cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://api@127.0.0.1:1/ryft", "PORT=1", "METRICS_PORT=1",
			"AUTH_MODE=apikey", "API_KEY=" + strings.Repeat("0123456789abcdef", 2), "ALLOW_INSECURE_APIKEY_AUTH=true",
			"PRIVACY_POLICY_VERSION=2026-10-26", "POSTHOG_PROJECT_KEY=" + key, "POSTHOG_HOST=" + host,
			"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"} // compose's local Pub/Sub, never reached
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("http://eu.i.posthog.com", 10*time.Second)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, `"msg":"analytics config"`) ||
		!strings.Contains(out, "POSTHOG_HOST must be https") {
		t.Fatalf("want exit 1 with the analytics config error, got %v:\n%s", err, out)
	}
	for _, later := range []string{`"msg":"listening"`, `"msg":"db"`, `"msg":"events"`} {
		if strings.Contains(out, later) {
			t.Errorf("the api got as far as %s before refusing POSTHOG_HOST:\n%s", later, out)
		}
	}
	if strings.Contains(out, key[4:]) || strings.Contains(out, "http://eu.i.posthog.com") {
		t.Errorf("the refusal quotes the key or the host:\n%s", out)
	}

	// Accepted: the start goes on to wait for the database, which never answers; the child is
	// stopped after the config lines.
	out, _ = run(config.DefaultPostHogHost, 3*time.Second)
	said := false
	for _, line := range strings.Split(out, "\n") {
		said = said || (strings.Contains(line, `"msg":"analytics"`) && strings.Contains(line, `"configured":true`))
	}
	if !said {
		t.Fatalf("no analytics line saying a key is set:\n%s", out)
	}
	if strings.Contains(out, key[4:]) || strings.Contains(out, "phc_") {
		t.Errorf("the start-up logs carry the project key:\n%s", out)
	}
}
