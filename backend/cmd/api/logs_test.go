package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// writeLogKeys is every line the writes below log with the keys it may carry
// besides logx's and the request's, and the kind of each value (docs/OPERATIONS.md
// → Data protection (logs)). Any other line, key or kind of value fails the
// tests, so a score under a count key cannot slip through.
var writeLogKeys = logcheck.Lines{
	"http":           logcheck.HTTP(model.CodeInviteRequired, model.CodeConsentRequired),
	"request failed": logcheck.RequestFailed(),
	"feedback received": {"feedback_id": logcheck.Count,
		"category":    logcheck.Enum(model.FeedbackBug, model.FeedbackIdea, model.FeedbackData, model.FeedbackOther),
		"request_ids": logcheck.CountMax(maxRequestIDs)},
}

var writeStandardKeys = logcheck.Standard("api")

// What the requests below carry that no log line may.
var (
	writeTexts   = []string{"61.7", "bad night", "Tims iPhone", "woke at 3", "hrv_ms", "rantings", "01a1-feedback-ref"}
	writeNumbers = []float64{61.7, 617}
)

// writeServer serves the api's feedback and journal writes as main does
// (Instrument, Recover) for user, every line, httpx.Fail's included, written to
// the buffer.
func writeServer(t *testing.T, st *store.Store, user model.User, day string) (feedback, journal http.Handler, buf *bytes.Buffer) {
	t.Helper()
	buf = &bytes.Buffer{}
	log := logx.NewWriter(buf, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := &handler{st: st, log: log, cache: newInsightCache()}
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
	return serve("POST /v1/feedback", h.postFeedback), serve("PUT /v1/journal/{day}", h.putJournal, "day", day), buf
}

func send(t *testing.T, srv http.Handler, method, path, body string, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	if rec.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, rec.Code, rec.Body, want)
	}
}

// checkWriteLog fails on a line, key or kind of value writeLogKeys does not
// allow and on what the requests carried; it returns the lines by message.
func checkWriteLog(t *testing.T, out string) map[string][]map[string]any {
	t.Helper()
	lines, problems := logcheck.Check(out, writeStandardKeys, writeLogKeys)
	for _, p := range problems {
		t.Error(p)
	}
	for msg, recs := range lines {
		for _, rec := range recs {
			for k, v := range rec {
				if f, ok := v.(float64); ok && slices.Contains(writeNumbers, f) {
					t.Errorf("%q carries the request's value as %s: %v", msg, k, rec)
				}
			}
			b, _ := json.Marshal(rec)
			for _, s := range writeTexts {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("%q holds %q: %s", msg, s, b)
				}
			}
		}
	}
	return lines
}

const (
	feedbackBody = `{"message": "my HRV read 61.7 after a bad night", "category": "data", "device_model": "Tims iPhone 15 Pro",
		"request_ids": ["01a1-feedback-ref"], "diagnostics": {"hrv_ms": 61.7, "note": "woke at 3"}}`
	feedbackRefused = `{"message": "my HRV read 61.7 after a bad night", "category": "rantings"}`
)

func journalBody(num string) string {
	return `{"entries": [{"behavior_id": "` + behavior.BuiltinID("alcohol_drinks").String() + `", "value_bool": true, "value_num": ` + num + `}]}`
}

// The error paths of the api's writes that carry health data or free text — a
// feedback report, a journal day — log the status and a server error's cause,
// never the body: not the message, not a diagnostic, not an answer's value, even
// where the 400 quotes the value back to its sender. The store is a closed
// pool, so the 500s need no database.
func TestErrorPathsLogNoBodies(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	day := time.Now().UTC().AddDate(0, 0, -1).Format(dayLayout)
	feedback, journal, buf := writeServer(t, store.New(pool), model.User{ID: uuid.New(), TZ: "UTC"}, day)
	send(t, feedback, http.MethodPost, "/v1/feedback", feedbackBody, http.StatusInternalServerError)
	send(t, feedback, http.MethodPost, "/v1/feedback", feedbackRefused, http.StatusUnprocessableEntity)
	send(t, journal, http.MethodPut, "/v1/journal/"+day, journalBody("61.7"), http.StatusBadRequest)
	send(t, journal, http.MethodPut, "/v1/journal/"+day, journalBody("617"), http.StatusInternalServerError)

	lines := checkWriteLog(t, buf.String())
	if len(lines["http"]) != 4 || len(lines["request failed"]) != 2 {
		t.Fatalf("want four access lines and the 500s' causes:\n%s", buf)
	}
}

// Stored in Postgres, a feedback report logs its id, category and how many
// request ids it named, a journal day nothing but the access line; neither the
// message, the diagnostics, the device nor an answer.
func TestWritesLogNoBodies(t *testing.T) {
	pool := testPool(t)
	if err := db.Seed(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	user, err := st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(user.TZ)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(loc).AddDate(0, 0, -1).Format(dayLayout)
	feedback, journal, buf := writeServer(t, st, user, day)
	send(t, feedback, http.MethodPost, "/v1/feedback", feedbackBody, http.StatusNoContent)
	send(t, journal, http.MethodPut, "/v1/journal/"+day, journalBody("617"), http.StatusUnprocessableEntity) // a count of 617 drinks
	send(t, journal, http.MethodPut, "/v1/journal/"+day, journalBody("3"), http.StatusOK)

	lines := checkWriteLog(t, buf.String())
	if f := lines["feedback received"]; len(f) != 1 || f[0]["category"] != "data" || f[0]["request_ids"] != 1.0 {
		t.Fatalf("one feedback line with its category and count: %v", f)
	}
	if len(lines["http"]) != 3 || len(lines["request failed"]) != 0 {
		t.Fatalf("three access lines, no failure:\n%s", buf)
	}
}
