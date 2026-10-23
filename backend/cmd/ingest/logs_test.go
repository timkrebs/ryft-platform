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
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// syncLogKeys is every line a sync logs with the keys it may carry besides
// logx's and the request's, and the kind of each value: counts, the device id,
// the status and an error's cause (docs/OPERATIONS.md → Data protection
// (logs)). Any other line, key or kind of value fails the tests below, so a
// score under a count key (a float, a number that is not a count) cannot slip
// through.
var syncLogKeys = logcheck.Lines{
	"http":           logcheck.HTTP(model.CodeInviteRequired, model.CodeConsentRequired),
	"request failed": logcheck.RequestFailed(),
	"sync": {"device": logcheck.UUID, "samples": logcheck.Count, "sleep": logcheck.Count,
		"workouts": logcheck.Count, "days": logcheck.Count},
}

var syncStandardKeys = logcheck.Standard("ingest")

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

// checkSyncLog fails on a line, key or kind of value syncLogKeys does not
// allow, on any of texts and on any of numbers as a value. It returns the lines
// by message.
func checkSyncLog(t *testing.T, out string, texts []string, numbers []float64) map[string][]map[string]any {
	t.Helper()
	lines, problems := logcheck.Check(out, syncStandardKeys, syncLogKeys, logcheck.Forbidden{Texts: texts, Numbers: numbers})
	for _, p := range problems {
		t.Error(p)
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

func post(t *testing.T, srv http.Handler, body string, want int) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body)))
	if rec.Code != want {
		t.Fatalf("%d %s, want %d", rec.Code, rec.Body, want)
	}
	return rec.Body.Bytes()
}

// assertStatuses pins the status of each line, in order: the requests are known.
func assertStatuses(t *testing.T, recs []map[string]any, want ...float64) {
	t.Helper()
	if len(recs) != len(want) {
		t.Fatalf("%d lines, want %d: %v", len(recs), len(want), recs)
	}
	for i, r := range recs {
		if r["status"] != want[i] {
			t.Errorf("line %d: status %v, want %v", i, r["status"], want[i])
		}
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
	assertStatuses(t, lines["http"], 400, 422, 500)
	assertStatuses(t, lines["request failed"], 500)
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
	// The roles first: tests of other packages migrate on this server at the same time.
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
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
// fails at its samples, its night or its run (a source Postgres refuses: a
// check this test adds, since a NUL is a 422 before the database) logs which
// step failed and the database's reason, never the source or a value.
func TestSyncLogsCountsOnly(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	user, err := st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	srv, buf := syncServer(t, st, pool, user)
	body := syncBody("", "", false)
	var sent struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil { // the request names the device
		t.Fatal(err)
	}
	var answer struct {
		Days []string `json:"days_touched"`
	}
	if err := json.Unmarshal(post(t, srv, body, http.StatusOK), &answer); err != nil || len(answer.Days) == 0 {
		t.Fatalf("the sync names the days it touched: %v %v", answer, err)
	}
	refused := "Tims Apple Watch (refused)"
	for _, table := range []string{"samples", "sleep_segments", "workouts"} {
		if _, err := pool.Exec(context.Background(),
			`ALTER TABLE `+table+` ADD CONSTRAINT test_refused CHECK (source <> '`+refused+`')`); err != nil {
			t.Fatal(err)
		}
	}
	for _, part := range []string{"samples", "sleep", "workouts"} {
		post(t, srv, syncBody(part, refused, false), http.StatusInternalServerError)
	}

	lines := checkSyncLog(t, buf.String(), syncTexts, syncNumbers)
	// Exact, not just a count: the kinds are the backstop, the known values the check.
	if s := lines["sync"]; len(s) != 1 || s[0]["samples"] != 3.0 || s[0]["sleep"] != 1.0 || s[0]["workouts"] != 1.0 ||
		s[0]["days"] != float64(len(answer.Days)) || s[0]["device"] != sent.DeviceID {
		t.Fatalf("one sync line with its counts: %v", s)
	}
	assertStatuses(t, lines["http"], 200, 500, 500, 500)
	failed := lines["request failed"]
	assertStatuses(t, failed, 500, 500, 500)
	if len(failed) != 3 {
		t.Fatalf("three failed syncs: %v", failed)
	}
	for k, part := range []string{"samples", "sleep", "workouts"} {
		if e, _ := failed[k]["err"].(string); !strings.HasPrefix(e, part+": ") {
			t.Errorf("the %s step's error names it: %q", part, e)
		}
	}
}
