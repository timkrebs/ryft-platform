package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
)

// The access line of a recovery request never holds the wake date or the workout id, whoever rejects the request
// (D300): the redaction runs outermost, before the bearer check, the limiter, the gates and the mux. No Postgres: the
// requests are rejected before any handler reads the store.

func TestPrivateLogPath(t *testing.T) {
	const id = "0b6f3a52-6a39-4d0b-8a0e-6f3a526a394d"
	for _, c := range []struct{ path, want string }{
		{"/v1/nutrition", "/v1/nutrition"},
		{"/v1/nutrition/meals/" + id, "/v1/nutrition"},
		{"/v1/nutritionx", ""},
		{"/v1/recovery/checkins/2026-11-26", "/v1/recovery/checkins/{date}"},
		{"/v1/recovery/checkins/", "/v1/recovery/checkins/{date}"},
		{"/v1/recovery/checkins/2026-11-26/extra", "/v1/recovery/checkins/{date}"},
		{"/v1/recovery/checkins", ""},
		{"/v1/recovery/status", ""},
		{"/v1/workouts/" + id + "/rpe", "/v1/workouts/{id}/rpe"},
		{"/v1/workouts/not-an-id/rpe", "/v1/workouts/{id}/rpe"},
		{"/v1/workouts/rpe", ""},
		{"/v1/workouts//rpe", ""},
		{"/v1/workouts/" + id + "/x/rpe", ""},
		{"/v1/workouts/" + id, ""}, // the workout read is out of scope
		{"/v1/workouts", ""},
		{"/v1/days/2026-11-26", ""},
	} {
		if got := privateLogPath(c.path); got != c.want {
			t.Errorf("privateLogPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// accessLine is one "http" log line of the api.
type accessLine struct {
	Path   string
	Status int
}

func accessLines(t *testing.T, logs string) []accessLine {
	t.Helper()
	var out []accessLine
	sc := bufio.NewScanner(strings.NewReader(logs))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, sc.Text())
		}
		if m["msg"] != "http" {
			continue
		}
		status, _ := m["status"].(float64)
		path, _ := m["path"].(string)
		out = append(out, accessLine{Path: path, Status: int(status)})
	}
	return out
}

func TestRecoveryRejectionsLogNoDateOrWorkoutID(t *testing.T) {
	logs := &lockedBuffer{}
	log := logx.NewWriter(logs, "api", slog.LevelDebug)
	beta := config.Beta{Gate: config.ModeOff, Consent: config.ModeOn, PrivacyPolicyVersion: nutritionPolicy}
	h := &handler{log: log, beta: beta, attestMode: config.ModeOff, accounts: &accounts{log: log}}
	mux := newMux(h, muxOptions{log: log, verifier: nutritionVerifier{}, ready: func(context.Context) error { return nil },
		tick: http.NotFoundHandler(), gate: beta.Gate, attest: config.AppAttest{Mode: config.ModeOff}, authMode: config.AuthAPIKey,
		clients: httpx.NewClientKeys(1),
		loadUser: func(_ context.Context, p auth.Principal) (model.User, error) { return model.User{ID: p.UserID}, nil }})
	srv := httpx.Instrument(log, nil, httpx.Recover(httpx.SecurityHeaders(mux)))
	do := func(user uuid.UUID, method, path string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if user != uuid.Nil {
			req.Header.Set("Authorization", "Bearer "+user.String())
		}
		resp := httptest.NewRecorder()
		srv.ServeHTTP(resp, req)
		return resp.Code
	}

	for _, c := range []struct {
		name, raw, want string
	}{
		{"check-in", "/v1/recovery/checkins/2031-04-17", "/v1/recovery/checkins/{date}"},
		{"session RPE", "/v1/workouts/" + uuid.NewString() + "/rpe", "/v1/workouts/{id}/rpe"},
	} {
		t.Run(c.name, func(t *testing.T) {
			secret := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.raw, "/v1/recovery/checkins/"), "/v1/workouts/"), "/rpe") // the date or the id
			before := len(accessLines(t, logs.String()))
			user := uuid.New()

			if got := do(uuid.Nil, http.MethodPut, c.raw); got != http.StatusUnauthorized {
				t.Fatalf("unauthorized: %d", got)
			}
			// A method the route does not have: the mux answers (404 behind the /v1/ catch-all, 405 without one).
			wrong := do(user, http.MethodPost, c.raw)
			if wrong != http.StatusNotFound && wrong != http.StatusMethodNotAllowed {
				t.Fatalf("wrong method: %d", wrong)
			}
			throttled := 0
			for range 200 { // the default class allows a burst of 60
				if do(user, http.MethodPost, c.raw) == http.StatusTooManyRequests {
					throttled++
				}
			}
			if throttled == 0 {
				t.Fatal("the limiter never answered 429")
			}

			lines := accessLines(t, logs.String())[before:]
			seen := map[int]bool{}
			for _, l := range lines {
				seen[l.Status] = true
				if l.Path != c.want {
					t.Errorf("access line status %d logs path %q, want %q", l.Status, l.Path, c.want)
				}
			}
			for _, status := range []int{http.StatusUnauthorized, wrong, http.StatusTooManyRequests} {
				if !seen[status] {
					t.Errorf("no access line with status %d", status)
				}
			}
			if strings.Contains(logs.String(), secret) {
				t.Errorf("the log holds %q", secret)
			}
		})
	}
}
