package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The error paths of POST /v1/sync log the status and, for a server error, its
// cause; never the body: a sample's value, the device's name, a source. A 400 or
// 422 may quote the request back to its sender, the log may not
// (docs/OPERATIONS.md → Data protection (logs)). The store is a closed pool, so
// the 500 needs no database.
func TestSyncErrorPathsLogNoHealthData(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewWriter(&buf, "ingest", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	pool, err := pgxpool.New(context.Background(), "postgres://ingest@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	h := &handler{st: store.New(pool), log: log, beta: config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}}
	user := model.User{ID: uuid.New(), TZ: "UTC", BetaAccess: true}
	srv := httpx.Instrument(log, httpx.Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f := httpx.Info(r.Context()); f != nil {
			f.UserID, f.Route = user.ID.String(), "POST /v1/sync"
		}
		h.sync(w, r.WithContext(auth.WithUser(r.Context(), user)))
	})))

	now := time.Now().UTC().Format(time.RFC3339)
	early := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	sample := func(start, end string) string {
		return `{"type": "hrv_sdnn", "start": "` + start + `", "end": "` + end + `", "value": 61.7, "unit": "ms",
			"source": "Tims Apple Watch", "source_bundle_id": "com.example.secretwatch"}`
	}
	body := func(samples, extra string) string {
		return `{"device_id": "` + uuid.NewString() + `", "device_name": "Tims iPhone 15 Pro", "app_version": "1.0 (7)",
			"samples": [` + samples + `], "sleep": [], "workouts": []` + extra + `}`
	}
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"unknown field", body(sample(early, now), `, "hrv": 61.7`), http.StatusBadRequest},
		{"end before start", body(sample(now, early), ""), http.StatusUnprocessableEntity},
		{"the store fails", body(sample(early, now), ""), http.StatusInternalServerError},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(c.body)))
		if rec.Code != c.status {
			t.Fatalf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.status)
		}
	}

	out := buf.String()
	if !strings.Contains(out, `"msg":"request failed"`) || strings.Count(out, `"msg":"http"`) != 3 {
		t.Fatalf("want three access lines and the 500's cause:\n%s", out)
	}
	for _, v := range []string{"61.7", "Tims iPhone", "Tims Apple Watch", "com.example.secretwatch", "hrv_sdnn", "1.0 (7)"} {
		if strings.Contains(out, v) {
			t.Errorf("the log holds %q:\n%s", v, out)
		}
	}
}
