package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// syncLogKeys is every line a sync logs with the keys it may carry besides
// logx's and the request's: counts, the device id, the status and an error's
// cause (docs/OPERATIONS.md → Data protection (logs)). Any other line or key
// fails the tests below.
var syncLogKeys = map[string][]string{
	"http":           {"method", "path", "status", "bytes", "dur_ms", "code", "gate"},
	"request failed": {"status", "err"},
	"sync":           {"device", "samples", "sleep", "workouts", "days"},
}

var baseLogKeys = []string{"time", "level", "msg", "service", "request_id", "client_request_id", "user_id", "route"}

// syncServer is POST /v1/sync as main serves it (Instrument, Recover) for user,
// every line of it, httpx.Fail's included, written to the buffer.
func syncServer(t *testing.T, st *store.Store, pool *pgxpool.Pool, user model.User) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	log := logx.NewWriter(&buf, "ingest", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := &handler{st: st, outbox: outbox.New(pool, "ingest", nil, log), log: log,
		beta: config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}}
	return httpx.Instrument(log, httpx.Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := httpx.Info(r.Context()); f != nil {
			f.UserID, f.Route = user.ID.String(), "POST /v1/sync"
		}
		h.sync(w, r.WithContext(auth.WithUser(r.Context(), user)))
	}))), &buf
}

// checkSyncLog fails on a line or key syncLogKeys does not allow, on any of
// texts and on any of numbers as a value. It returns the lines by message.
func checkSyncLog(t *testing.T, out string, texts []string, numbers []float64) map[string][]map[string]any {
	t.Helper()
	lines := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		delete(rec, "time") // 07:38:47.3… is a time, not a resting heart rate
		msg, _ := rec["msg"].(string)
		lines[msg] = append(lines[msg], rec)
		allowed, known := syncLogKeys[msg]
		if !known {
			t.Errorf("a new log line %q: check it against the rule, then list its keys in syncLogKeys: %s", msg, line)
		}
		for k, v := range rec {
			if !slices.Contains(baseLogKeys, k) && !slices.Contains(allowed, k) {
				t.Errorf("%q carries %s, which it may not: %s", msg, k, line)
			}
			if f, ok := v.(float64); ok && slices.Contains(numbers, f) {
				t.Errorf("%q carries a value of the request as %s: %s", msg, k, line)
			}
		}
		b, _ := json.Marshal(rec)
		for _, s := range texts {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%q holds %q: %s", msg, s, line)
			}
		}
	}
	return lines
}

// What a sync carries that its log lines may not.
var (
	syncTexts   = []string{"61.7", "47.3", "152.3", "612.4", "10234.5", "Tims", "Apple Watch", "com.example.secretwatch", "hrv_sdnn", "resting_hr", "1.0 (7)"}
	syncNumbers = []float64{61.7, 47.3, 12345, 152.3, 181.9, 612.4, 10234.5}
)

// syncBody is a sync with a sample of each kind, a night and a run; source
// replaces the source of the part named by broken ("samples", "sleep",
// "workouts") and before swaps a sample's start and end.
func syncBody(broken, source string, before bool) string {
	start, end := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-time.Hour)
	at := func(t time.Time) string { return `"` + t.Format(time.RFC3339) + `"` }
	src := func(part string) string {
		if part == broken {
			return source
		}
		return "Tims Apple Watch"
	}
	sampleStart, sampleEnd := at(start), at(end)
	if before {
		sampleStart, sampleEnd = sampleEnd, sampleStart
	}
	return `{"device_id": "` + uuid.NewString() + `", "device_name": "Tims iPhone 15 Pro", "app_version": "1.0 (7)",
		"samples": [
			{"type": "hrv_sdnn", "start": ` + sampleStart + `, "end": ` + sampleEnd + `, "value": 61.7, "unit": "ms",
			 "source": "` + src("samples") + `", "source_bundle_id": "com.example.secretwatch"},
			{"type": "resting_hr", "start": ` + at(end) + `, "end": ` + at(end) + `, "value": 47.3, "unit": "count/min", "source": "` + src("samples") + `"},
			{"type": "steps", "start": ` + at(start) + `, "end": ` + at(end) + `, "value": 12345, "unit": "count", "source": "` + src("samples") + `"}],
		"sleep": [{"start": ` + at(start) + `, "end": ` + at(end) + `, "stage": "core", "source": "` + src("sleep") + `"}],
		"workouts": [{"id": "` + uuid.NewString() + `", "activity": "running", "start": ` + at(start) + `, "end": ` + at(end) + `,
			"avg_hr": 152.3, "max_hr": 181.9, "energy_kcal": 612.4, "distance_m": 10234.5, "source": "` + src("workouts") + `"}]}`
}

func post(t *testing.T, srv http.Handler, body string, want int) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body)))
	if rec.Code != want {
		t.Fatalf("%d %s, want %d", rec.Code, rec.Body, want)
	}
}

// The error paths of POST /v1/sync log the status and, for a server error, its
// cause; never the body: a sample's value, the device's name, a source. A 400 or
// 422 may quote the request back to its sender, the log may not. The store is a
// closed pool, so the 500 needs no database.
func TestSyncErrorPathsLogNoHealthData(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://ingest@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	srv, buf := syncServer(t, store.New(pool), pool, model.User{ID: uuid.New(), TZ: "UTC"})
	unknown := strings.TrimSuffix(syncBody("", "", false), "}") + `, "hrv": 61.7}`
	post(t, srv, unknown, http.StatusBadRequest)
	post(t, srv, syncBody("", "", true), http.StatusUnprocessableEntity)
	post(t, srv, syncBody("", "", false), http.StatusInternalServerError)

	lines := checkSyncLog(t, buf.String(), syncTexts, syncNumbers)
	if len(lines["http"]) != 3 || len(lines["request failed"]) != 1 {
		t.Fatalf("want three access lines and the 500's cause:\n%s", buf)
	}
}

// Integration test, opt-in like internal/db's: RYFT_TEST_POSTGRES_URL is a
// superuser connection to a throwaway server.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	name := fmt.Sprintf("ryft_ingesttest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return pool
}

// A sync stored in Postgres logs its counts and the device id; a sync that
// fails at its samples, its night or its run (a NUL in the source, which
// Postgres refuses) logs which step failed and the database's reason, never
// the source or a value.
func TestSyncLogsCountsOnly(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	user, err := st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	srv, buf := syncServer(t, st, pool, user)
	post(t, srv, syncBody("", "", false), http.StatusOK)
	nul := `Tims Apple Watch\u0000`
	for _, part := range []string{"samples", "sleep", "workouts"} {
		post(t, srv, syncBody(part, nul, false), http.StatusInternalServerError)
	}

	lines := checkSyncLog(t, buf.String(), syncTexts, syncNumbers)
	if s := lines["sync"]; len(s) != 1 || s[0]["samples"] != 3.0 || s[0]["sleep"] != 1.0 || s[0]["workouts"] != 1.0 {
		t.Fatalf("one sync line with its counts: %v", s)
	}
	failed := lines["request failed"]
	if len(failed) != 3 {
		t.Fatalf("three failed syncs: %v", failed)
	}
	for k, part := range []string{"samples", "sleep", "workouts"} {
		if e, _ := failed[k]["err"].(string); !strings.HasPrefix(e, part+": ") {
			t.Errorf("the %s step's error names it: %q", part, e)
		}
	}
}
