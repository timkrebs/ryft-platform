package main

import (
	"bytes"
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
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// nulServer serves the api's writes and a search as main does (Instrument,
// Recover, then the auth fake's user and WithRoutes), every line to buf.
func nulServer(t *testing.T, st *store.Store, user model.User) (http.Handler, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	log := logx.NewWriter(buf, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := &handler{st: st, log: log, cache: newInsightCache()}
	api := http.NewServeMux()
	api.HandleFunc("PATCH /v1/me", h.patchMe)
	api.HandleFunc("PATCH /v1/me/notifications", h.patchNotifications)
	api.HandleFunc("POST /v1/me/invite", h.redeemInvite)
	api.HandleFunc("POST /v1/feedback", h.postFeedback)
	api.HandleFunc("POST /v1/devices/{id}/push", h.registerPush)
	api.HandleFunc("GET /v1/days/{day}", h.day)
	api.HandleFunc("GET /v1/exercises", h.exercises)
	api.HandleFunc("POST /v1/exercises", h.createExercise)
	api.HandleFunc("PUT /v1/plans/{id}", h.putPlan)
	api.HandleFunc("PUT /v1/sessions/{id}", h.putSession)
	api.HandleFunc("POST /v1/behaviors", h.createBehavior)
	routes := httpx.WithRoutes(api)
	return httpx.Instrument(log, httpx.Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := httpx.Info(r.Context()); f != nil {
			f.UserID = user.ID.String()
		}
		routes.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
	}))), buf
}

func callNUL(srv http.Handler, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

// errorBody is the error shape with exactly error and request_id (no code).
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["request_id"] == "" || len(body) != 2 {
		t.Fatalf("not the error shape: %s", rec.Body)
	}
	return body["error"]
}

// A NUL in a string of any api write — a value or a key, nested in a session,
// a plan or a report's diagnostics, or a field the handler would normalise
// away — is a 422 before the database is asked (the store is a closed pool,
// which would make it a 500); a NUL or non-UTF-8 byte in the path or query is
// a 400. The escaped backslash and ordinary text pass to the store; raw 0x00
// is not JSON (400). No line logs the text.
func TestWritesRefuseNULBeforeTheDatabase(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	srv, buf := nulServer(t, store.New(pool), model.User{ID: uuid.New(), TZ: "UTC"})
	id := uuid.NewString()
	nul := `strings must not contain the NUL character (\u0000)`
	url := "the path and query must be UTF-8 text without NUL characters"
	for _, c := range []struct {
		method, target, body string
		want                 int
		msg                  string
	}{
		{"POST", "/v1/feedback", `{"message": "bad night\u0000", "category": "data"}`, 422, nul},
		{"POST", "/v1/feedback", `{"message": "bad night", "diagnostics": {"hrv_ms": 61.7, "notes": {"woke at 3\u0000": true}}}`, 422, nul},
		{"POST", "/v1/feedback", `{"message": "bad night", "request_ids": ["01a1-feedback-ref", "\u0000"]}`, 422, nul},
		{"PUT", "/v1/sessions/" + id, `{"title": "Push", "exercises": [{"notes": "left knee", "sets": []}, {"notes": "woke at 3\u0000"}]}`, 422, nul},
		{"PUT", "/v1/plans/" + id, `{"name": "Mine", "days": [{"name": "Legs\u0000", "exercises": []}]}`, 422, nul},
		{"POST", "/v1/exercises", `{"name": "Curl\u0000", "kind": "strength"}`, 422, nul},
		{"POST", "/v1/behaviors", `{"name": "Sauna\u0000", "kind": "bool"}`, 422, nul},
		{"POST", "/v1/devices/" + id + "/push", `{"token": "ab", "env": "production", "locale": "de\u0000"}`, 422, nul},
		{"PATCH", "/v1/me", `{"tz": "Europe/Berlin\u0000"}`, 422, nul},
		{"PATCH", "/v1/me/notifications", `{"categories": {"MORNING_SCORE\u0000": true}}`, 422, nul},
		{"POST", "/v1/me/invite", `{"code": "RYFT-7K3M\u0000"}`, 422, nul},
		{"GET", "/v1/exercises?q=curl%00", "", 400, url},
		{"GET", "/v1/exercises?q=%FF", "", 400, url},
		{"GET", "/v1/days/2026-10-2%00", "", 400, url},
		{"POST", "/v1/feedback", "{\"message\": \"bad night\x00\"}", 400, ""},
		{"POST", "/v1/feedback", `{"message": "bad night C:\\u0000", "category": "data"}`, 500, "internal error"},
		{"GET", "/v1/exercises?q=caf%C3%A9%5Cu0000", "", 500, "internal error"},
	} {
		rec := callNUL(srv, c.method, c.target, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s %.40s: %d %s, want %d", c.method, c.target, c.body, rec.Code, rec.Body, c.want)
			continue
		}
		if msg := errorBody(t, rec); c.msg != "" && msg != c.msg {
			t.Errorf("%s %s: %q, want %q", c.method, c.target, msg, c.msg)
		}
	}

	lines := checkWriteLog(t, buf.String())
	if len(lines["http"]) != 17 || len(lines["request failed"]) != 2 || len(lines) != 2 {
		t.Fatalf("want an access line each and the closed pool's two causes:\n%s", buf)
	}
	// The access line keeps the path, as for every request (a refused one's NUL
	// JSON-escaped); a body's text and the query never appear.
	for _, s := range []string{"curl", "Sauna", "Legs", "RYFT-7K3M", "MORNING_SCORE", "Europe/Berlin"} {
		if strings.Contains(buf.String(), s) {
			t.Fatalf("a line holds %q:\n%s", s, buf)
		}
	}
}

// Against Postgres, the requests that were a 500 are a 422 or a 400: a report
// whose message Postgres refused as text (22021) or whose diagnostics it
// refused as jsonb (22P05), and a search for a NUL (22021). Nothing is stored;
// the escaped backslash is stored as written.
func TestNULIs4xxWithPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := store.New(pool)
	user, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	srv, buf := nulServer(t, st, user)
	for target, c := range map[string]struct {
		method, body string
		want         int
	}{
		"text":        {"POST", `{"message": "bad night\u0000", "category": "data"}`, 422},
		"jsonb key":   {"POST", `{"message": "bad night", "diagnostics": {"woke at 3\u0000": true}}`, 422},
		"jsonb value": {"POST", `{"message": "bad night", "diagnostics": {"hrv_ms": 61.7, "note": "woke at 3\u0000"}}`, 422},
		"search":      {"GET", "", 400},
		"escaped":     {"POST", `{"message": "bad night C:\\u0000", "diagnostics": {"note": "\\u0000"}}`, 204},
	} {
		path := "/v1/feedback"
		if c.method == "GET" {
			path = "/v1/exercises?q=curl%00"
		}
		if rec := callNUL(srv, c.method, path, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", target, rec.Code, rec.Body, c.want)
		}
	}
	var n int
	var message, note string
	if err := pool.QueryRow(ctx, `SELECT count(*), min(message), min(diagnostics->>'note') FROM feedback WHERE user_id = $1`,
		user.ID).Scan(&n, &message, &note); err != nil || n != 1 || message != `bad night C:\u0000` || note != `\u0000` {
		t.Fatalf("only the escaped report, as written: %d %q %q %v", n, message, note, err)
	}
	lines := checkWriteLog(t, buf.String())
	if len(lines["http"]) != 5 || len(lines["feedback received"]) != 1 || len(lines["request failed"]) != 0 {
		t.Fatalf("five access lines, one report, no failure:\n%s", buf)
	}
}
