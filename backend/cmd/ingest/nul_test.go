package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// nulSource is a sample's, a night's or a run's source with a NUL (JSON
// "\u0000"), which Postgres refuses in text; escapedSource is the same text
// with the backslash escaped: six ordinary characters, stored as written.
const (
	nulSource     = `Tims Apple Watch\u0000`
	escapedSource = `Tims Apple Watch\\u0000`
)

func postRec(srv http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body)))
	return rec
}

// wantNUL422 checks the answer API.md documents for a NUL in a string.
func wantNUL422(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]string
	if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		body["error"] != `strings must not contain the NUL character (\u0000)` || body["request_id"] == "" || len(body) != 2 {
		t.Fatalf("want the 422 of API.md, got %d %s", rec.Code, rec.Body)
	}
}

// A NUL anywhere in a sync's text (a source in each part, the device name, a
// bundle id) is a 422 before the database is asked: the store is a closed pool,
// which would make it a 500. Raw 0x00 is not JSON (400); the escaped backslash
// passes validation and reaches the store. The log holds access lines with
// their status, the closed pool's cause for the escaped one, and none of the text.
func TestSyncRefusesNULBeforeTheDatabase(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://ingest@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	srv, buf := syncServer(t, store.New(pool), pool, model.User{ID: uuid.New(), TZ: "UTC"})
	for _, part := range []string{"samples", "sleep", "workouts"} {
		wantNUL422(t, postRec(srv, syncBody(part, nulSource, false)))
	}
	ok := syncBody("", "", false)
	wantNUL422(t, postRec(srv, strings.Replace(ok, `"Tims iPhone 15 Pro"`, `"Tims iPhone\u0000"`, 1)))
	wantNUL422(t, postRec(srv, strings.Replace(ok, `"com.example.secretwatch"`, `"com.example.secretwatch\u0000"`, 1)))
	if rec := postRec(srv, syncBody("samples", "Tims Apple Watch\x00", false)); rec.Code != http.StatusBadRequest {
		t.Fatalf("raw 0x00: %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := postRec(srv, syncBody("samples", escapedSource, false)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("an escaped backslash reaches the (closed) store: %d %s", rec.Code, rec.Body)
	}

	lines := checkSyncLog(t, buf.String(), append(syncTexts, "u0000", `\u0000`), syncNumbers)
	if len(lines["http"]) != 7 || len(lines["request failed"]) != 1 || len(lines) != 2 {
		t.Fatalf("want seven access lines and the closed pool's cause:\n%s", buf)
	}
	for _, l := range lines["http"][:5] {
		if l["status"] != 422.0 {
			t.Errorf("access line of a refused sync: %v", l)
		}
	}
}

// Against Postgres. The syncs that were a 500 (Postgres refused the NUL at the
// samples, the night or the run: 22021) are a 422 and store nothing, not even
// the device; the escaped backslash is stored as written. Should a path ever
// bypass ReadJSON, the database's refusal is a 422 as well: of text (22021),
// and of jsonb (22P05, the device's last sync summary).
func TestSyncNULIs422WithPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := store.New(pool)
	user, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	srv, buf := syncServer(t, st, pool, user)
	count := func(table string) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, user.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, part := range []string{"samples", "sleep", "workouts"} {
		wantNUL422(t, postRec(srv, syncBody(part, nulSource, false)))
	}
	for _, table := range []string{"devices", "samples", "sleep_segments", "workouts", "user_sources"} {
		if n := count(table); n != 0 {
			t.Errorf("%s: %d rows after refused syncs", table, n)
		}
	}
	if rec := postRec(srv, syncBody("samples", escapedSource, false)); rec.Code != http.StatusOK {
		t.Fatalf("escaped backslash: %d %s, want 200", rec.Code, rec.Body)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM samples WHERE user_id = $1 AND source = $2`,
		user.ID, `Tims Apple Watch\u0000`).Scan(&stored); err != nil || stored != 3 {
		t.Fatalf("the three samples keep the six characters: %d, %v", stored, err)
	}
	lines := checkSyncLog(t, buf.String(), append(syncTexts, "u0000"), syncNumbers)
	if len(lines["http"]) != 4 || len(lines["sync"]) != 1 || len(lines["request failed"]) != 0 {
		t.Fatalf("four access lines, one sync, no failure:\n%s", buf)
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
