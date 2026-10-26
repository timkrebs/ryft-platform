package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// nulSource is a sample's, a night's or a run's source with a NUL (JSON
// "\u0000"), which Postgres refuses in text; escapedSource is the same text
// with the backslash escaped: six ordinary characters, stored as written.
const (
	nulSource     = `Tims Apple\u0000 Watch\u0000`
	escapedSource = `Tims Apple Watch\\u0000`
)

func postRec(srv http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body)))
	return rec
}

// nulSync is a sync with a NUL in every source, the device name, a bundle id
// and a key ("sou\u0000rce", which names source once the NUL is gone).
func nulSync() string {
	body := strings.ReplaceAll(syncBody("", "", false), `"Tims Apple Watch"`, `"`+nulSource+`"`)
	body = strings.Replace(body, `"Tims iPhone 15 Pro"`, `"Tims iPhone\u0000 15 Pro"`, 1)
	body = strings.Replace(body, `"com.example.secretwatch"`, `"com.example.\u0000secretwatch"`, 1)
	return strings.Replace(body, `"source": `, `"sou\u0000rce": `, 1)
}

// Before the database: a sync with NULs decodes (the store is a closed pool, so
// it fails there, with a 500 that logs the step, not the text); raw 0x00 is not
// JSON (400). No line holds the request's text.
func TestSyncStripsNULBeforeTheDatabase(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://ingest@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	srv, buf := syncServer(t, store.New(pool), pool, model.User{ID: uuid.New(), TZ: "UTC"})
	if rec := postRec(srv, nulSync()); rec.Code != http.StatusInternalServerError {
		t.Fatalf("NULs removed, the page reaches the (closed) store: %d %s", rec.Code, rec.Body)
	}
	if rec := postRec(srv, syncBody("samples", "Tims Apple Watch\x00", false)); rec.Code != http.StatusBadRequest {
		t.Fatalf("raw 0x00: %d %s, want 400", rec.Code, rec.Body)
	}
	lines := checkSyncLog(t, buf.String(), append(syncTexts, "u0000", `\u0000`, "secretwatch"), syncNumbers)
	if len(lines["http"]) != 2 || len(lines["request failed"]) != 1 || len(lines) != 2 {
		t.Fatalf("want two access lines and the closed pool's cause:\n%s", buf)
	}
}

// Against Postgres. A sync that was a 500 (Postgres refused the NUL in a
// source: 22021) is accepted without its NULs, keys included, and counted; the
// escaped backslash is stored as written; raw 0x00 stays a 400 and stores nothing.
func TestSyncStripsNULWithPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := store.New(pool)
	user, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	srv, buf := syncServer(t, st, pool, user)
	stripped := metrics.NULStripped.WithLabelValues("POST /v1/sync")
	before := testutil.ToFloat64(stripped)
	sources := func(source string) (n int) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM samples WHERE user_id = $1 AND source = $2)
			+ (SELECT count(*) FROM sleep_segments WHERE user_id = $1 AND source = $2)
			+ (SELECT count(*) FROM workouts WHERE user_id = $1 AND source = $2)`, user.ID, source).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if body := nulSync(); strings.Count(body, `\u0000`) != 13 || !strings.Contains(body, `"sou\u0000rce": `) {
		t.Fatalf("the fixture lost a NUL: %s", body)
	}
	if rec := postRec(srv, nulSync()); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":{"samples":3,"sleep":1,"workouts":1}`) {
		t.Fatalf("NULs removed, page accepted: %d %s", rec.Code, rec.Body)
	}
	if n := sources("Tims Apple Watch"); n != 5 {
		t.Errorf("five records under the source without its NULs, got %d", n)
	}
	var device, bundle string
	if err := pool.QueryRow(ctx, `SELECT d.name, s.bundle_id FROM devices d, user_sources s
		WHERE d.user_id = $1 AND s.user_id = $1 AND s.bundle_id IS NOT NULL`, user.ID).Scan(&device, &bundle); err != nil ||
		device != "Tims iPhone 15 Pro" || bundle != "com.example.secretwatch" {
		t.Errorf("device name and bundle id without their NULs: %q %q %v", device, bundle, err)
	}
	if n := testutil.ToFloat64(stripped) - before; n != 1 {
		t.Errorf("the sync counts once in ryft_http_nul_stripped_total, counted %v", n)
	}

	if rec := postRec(srv, syncBody("samples", escapedSource, false)); rec.Code != http.StatusOK {
		t.Fatalf("escaped backslash: %d %s, want 200", rec.Code, rec.Body)
	}
	if n := sources(`Tims Apple Watch\u0000`); n != 3 {
		t.Errorf("the three samples keep the six characters, got %d", n)
	}
	if rec := postRec(srv, syncBody("samples", "Tims Apple Watch (raw)\x00", false)); rec.Code != http.StatusBadRequest {
		t.Fatalf("raw 0x00: %d %s, want 400", rec.Code, rec.Body)
	}
	if n := sources("Tims Apple Watch (raw)"); n != 0 {
		t.Errorf("raw 0x00 stored %d records", n)
	}
	if n := testutil.ToFloat64(stripped) - before; n != 1 {
		t.Errorf("only the sync with NULs counts, counted %v", n)
	}

	lines := checkSyncLog(t, buf.String(), append(syncTexts, "u0000", "secretwatch"), syncNumbers)
	if len(lines["http"]) != 3 || len(lines["sync"]) != 2 || len(lines["request failed"]) != 0 {
		t.Fatalf("three access lines, two syncs, no failure:\n%s", buf)
	}
}

// Should a path ever bypass the checks, Postgres's refusal of text (22021) and
// of jsonb (22P05, the device's last sync summary) is a 422, without the
// database's text.
func TestFailAnswersPostgresRefusals422(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := store.New(pool)
	user, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, textErr := st.UpsertSamples(ctx, user.ID, []model.Sample{{Type: model.SampleSteps, Start: now, End: now,
		Value: 1, Unit: "count", Source: "Tims Apple Watch\x00"}})
	jsonbErr := st.RecordSync(ctx, user.ID, uuid.New(), map[string]any{"app_version": "1.0 (7)\x00"})
	for code, err := range map[string]error{"22021": textErr, "22P05": jsonbErr} {
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("want Postgres's %s, got %v", code, err)
		}
		rec := httptest.NewRecorder()
		httpx.Fail(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", nil), err)
		if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "SQLSTATE") {
			t.Errorf("%s: %d %s, want a 422 without the database's text", code, rec.Code, rec.Body)
		}
	}
}
