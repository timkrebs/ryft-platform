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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The access line of a request whose path holds a date (a recovery check-in, a journal day, a day, a coaching
// feedback) or a workout's RPE never holds the date or the workout id, whoever answers the request (D300, RFT-75):
// the redaction runs outermost, before the bearer check, the limiter, the gates and the mux. No Postgres: the store
// is a closed pool.

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
		{"/v1/workouts/" + id + "/rpe/", "/v1/workouts/{id}/rpe"},
		{"/v1/workouts/" + id + "/rpe//", "/v1/workouts/{id}/rpe"}, // the mux's 301 to the cleaned path
		{"/v1//workouts/" + id + "/rpe", "/v1/workouts/{id}/rpe"},
		{"/v1//journal/2026-11-26", "/v1/journal/{day}"},
		{"/v1/x/../days/2026-11-26", "/v1/days/{day}"},
		{"/v1/./coaching/feedback/2026-11-26", "/v1/coaching/feedback/{date}"},
		{"/v1//recovery/checkins/2026-11-26", "/v1/recovery/checkins/{date}"},
		{"/v1//days", ""},
		{"/v1/workouts/rpe", ""},
		{"/v1/workouts//rpe", ""},
		{"/v1/workouts/" + id + "/x/rpe", ""},
		{"/v1/workouts/" + id, ""}, // the workout read is out of scope
		{"/v1/workouts", ""},
		{"/v1/days/2026-11-26", "/v1/days/{day}"},
		{"/v1/days/2026-11-26/", "/v1/days/{day}"},
		{"/v1/days/", "/v1/days/{day}"},
		{"/v1/days", ""},
		{"/v1/daysx/2026-11-26", ""},
		{"/v1/journal/2026-11-26", "/v1/journal/{day}"},
		{"/v1/journal/2026-11-26/extra", "/v1/journal/{day}"},
		{"/v1/journal", ""},
		{"/v1/coaching/feedback/2026-11-26", "/v1/coaching/feedback/{date}"},
		{"/v1/coaching/feedback/", "/v1/coaching/feedback/{date}"},
		{"/v1/coaching/feedback", ""},
		{"/v1/coaching/daily-briefing", ""},
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

// Every route whose path holds a date or the workout id logs the redacted path for an authorized request (it reaches
// its handler, whose store is a closed pool) and for the 401, the mux's 404/405 and the limiter's 429 (RFT-75).
func TestPrivatePathsLogNoDateOrWorkoutID(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	logs := &lockedBuffer{}
	log := logx.NewWriter(logs, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	beta := config.Beta{Gate: config.ModeOff, Consent: config.ModeOn, PrivacyPolicyVersion: nutritionPolicy}
	h := &handler{st: store.New(pool), log: log, beta: beta, attestMode: config.ModeOff, accounts: &accounts{log: log},
		cache: newInsightCache()}
	mux := newMux(h, muxOptions{log: log, verifier: nutritionVerifier{}, ready: func(context.Context) error { return nil },
		tick: http.NotFoundHandler(), gate: beta.Gate, attest: config.AppAttest{Mode: config.ModeOff}, authMode: config.AuthAPIKey,
		clients: httpx.NewClientKeys(1), loadUser: func(_ context.Context, p auth.Principal) (model.User, error) { return model.User{ID: p.UserID}, nil }})
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

	workout := uuid.NewString()
	for _, c := range []struct {
		name, method, raw, secret, want string
	}{
		{"check-in", http.MethodPut, "/v1/recovery/checkins/2031-04-17", "2031-04-17", "/v1/recovery/checkins/{date}"},
		{"session RPE", http.MethodPut, "/v1/workouts/" + workout + "/rpe", workout, "/v1/workouts/{id}/rpe"},
		{"journal read", http.MethodGet, "/v1/journal/2031-05-19", "2031-05-19", "/v1/journal/{day}"},
		{"journal write", http.MethodPut, "/v1/journal/2031-06-23", "2031-06-23", "/v1/journal/{day}"},
		{"day", http.MethodGet, "/v1/days/2031-07-29", "2031-07-29", "/v1/days/{day}"},
		{"coaching feedback", http.MethodPut, "/v1/coaching/feedback/2031-08-13", "2031-08-13", "/v1/coaching/feedback/{date}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(accessLines(t, logs.String()))
			user := uuid.New()

			authorized := do(user, c.method, c.raw)
			if authorized == http.StatusUnauthorized || authorized == http.StatusTooManyRequests || authorized == http.StatusMethodNotAllowed {
				t.Fatalf("authorized: %d", authorized)
			}
			if got := do(uuid.Nil, c.method, c.raw); got != http.StatusUnauthorized {
				t.Fatalf("unauthorized: %d", got)
			}
			// An unclean path: the mux redirects to the cleaned one before any route runs.
			redirect := do(uuid.Nil, http.MethodGet, strings.Replace(c.raw, "/v1/", "/v1//", 1))
			if redirect != http.StatusMovedPermanently && redirect != http.StatusTemporaryRedirect {
				t.Fatalf("unclean path: %d", redirect)
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
				// A handler behind its route logs its mux pattern, "PUT /v1/recovery/checkins/{date}".
				if l.Path != c.want && l.Path != c.method+" "+c.want {
					t.Errorf("access line status %d logs path %q, want %q", l.Status, l.Path, c.want)
				}
			}
			for _, status := range []int{authorized, http.StatusUnauthorized, redirect, wrong, http.StatusTooManyRequests} {
				if !seen[status] {
					t.Errorf("no access line with status %d", status)
				}
			}
			if strings.Contains(logs.String(), c.secret) {
				t.Errorf("the log holds %q", c.secret)
			}
		})
	}
}
