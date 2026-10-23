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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The error paths of the api's writes that carry health data or free text — a
// feedback report, a journal day — log the status and a server error's cause,
// never the body: not the message, not a diagnostic, not an answer's value, even
// where the 400 quotes the value back to its sender (docs/OPERATIONS.md → Data
// protection (logs)). The store is a closed pool, so the 500s need no database.
func TestErrorPathsLogNoBodies(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewWriter(&buf, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	h := &handler{st: store.New(pool), log: log, cache: newInsightCache()}
	user := model.User{ID: uuid.New(), TZ: "UTC"}
	serve := func(route string, fn http.HandlerFunc, pathValues ...string) http.Handler {
		return httpx.Instrument(log, httpx.Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if f := httpx.Info(r.Context()); f != nil {
				f.UserID, f.Route = user.ID.String(), route
			}
			for k := 0; k+1 < len(pathValues); k += 2 {
				r.SetPathValue(pathValues[k], pathValues[k+1])
			}
			fn(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})))
	}
	day := time.Now().UTC().AddDate(0, 0, -1).Format(dayLayout)
	feedback := serve("POST /v1/feedback", h.postFeedback)
	journal := serve("PUT /v1/journal/{day}", h.putJournal, "day", day)
	drinks := behavior.BuiltinID("alcohol_drinks").String()
	for _, c := range []struct {
		name   string
		srv    http.Handler
		method string
		path   string
		body   string
		status int
	}{
		{"feedback, the store fails", feedback, http.MethodPost, "/v1/feedback",
			`{"message": "my HRV read 61.7 after a bad night", "category": "data", "device_model": "Tims iPhone 15 Pro",
			"diagnostics": {"hrv_ms": 61.7, "note": "woke at 3"}}`, http.StatusInternalServerError},
		{"feedback, refused", feedback, http.MethodPost, "/v1/feedback",
			`{"message": "my HRV read 61.7 after a bad night", "category": "rantings"}`, http.StatusUnprocessableEntity},
		{"journal, a value that does not decode", journal, http.MethodPut, "/v1/journal/" + day,
			`{"entries": [{"behavior_id": "` + drinks + `", "value_bool": true, "value_num": 61.7}]}`, http.StatusBadRequest},
		{"journal, the store fails", journal, http.MethodPut, "/v1/journal/" + day,
			`{"entries": [{"behavior_id": "` + drinks + `", "value_bool": true, "value_num": 617}]}`, http.StatusInternalServerError},
	} {
		rec := httptest.NewRecorder()
		c.srv.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)))
		if rec.Code != c.status {
			t.Fatalf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.status)
		}
	}

	out := buf.String()
	if strings.Count(out, `"msg":"request failed"`) != 2 || strings.Count(out, `"msg":"http"`) != 4 {
		t.Fatalf("want four access lines and the 500s' causes:\n%s", out)
	}
	for _, v := range []string{"61.7", "bad night", "Tims iPhone", "woke at 3", "hrv_ms", "rantings"} {
		if strings.Contains(out, v) {
			t.Errorf("the log holds %q:\n%s", v, out)
		}
	}
	// 617 as a value, whatever the key (as a substring it could be part of an id).
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		for k, v := range rec {
			if f, ok := v.(float64); ok && (f == 617 || f == 61.7) {
				t.Errorf("%s carries the answer's value: %s", k, line)
			}
		}
	}
}
