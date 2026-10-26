package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// The Train handlers' test bench: the api's chain as main builds it (Instrument, Recover,
// SecurityHeaders; auth with the API-key verifier, a counting rate limit and the user
// row; the invite gate; the import on its own route), on a migrated, seeded database.

const (
	benchKey    = "train-bench-key-0123456789"
	benchPolicy = "2026-10-01"
)

// lockedBuffer is a bytes.Buffer two requests may log to at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// benchBus records what the handlers publish directly (the import writes the outbox).
type benchBus struct {
	mu        sync.Mutex
	ingested  []model.IngestedEvent
	completed []model.SessionCompletedEvent
}

func (f *benchBus) PublishIngested(_ context.Context, ev model.IngestedEvent, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ingested = append(f.ingested, ev)
	return nil
}

func (f *benchBus) PublishSessionCompleted(_ context.Context, ev model.SessionCompletedEvent, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, ev)
	return nil
}

type bench struct {
	t      *testing.T
	pool   *pgxpool.Pool
	st     *store.Store
	h      *handler
	bus    *benchBus
	srv    http.Handler
	logs   *lockedBuffer
	tokens atomic.Int64 // rate-limit tokens taken
}

func defaultBeta() config.Beta {
	return config.Beta{Gate: config.ModeOff, Consent: config.ModeOn, PrivacyPolicyVersion: benchPolicy}
}

func newBench(t *testing.T, beta config.Beta) *bench {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Seed(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	b := &bench{t: t, pool: pool, logs: &lockedBuffer{}, bus: &benchBus{}}
	log := logx.NewWriter(b.logs, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	b.st = store.New(pool).WithImportRules(importRules())
	b.h = &handler{st: b.st, bus: b.bus, log: log, cache: newInsightCache(), engine: scoring.NewEngine(b.st, log), beta: beta}
	h := b.h
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/sync", h.syncStatus)
	api.HandleFunc("POST /v1/exercises", h.createExercise)
	api.HandleFunc("DELETE /v1/exercises/{id}", h.archiveExercise)
	api.HandleFunc("GET /v1/exercises/{id}/history", h.exerciseHistory)
	api.HandleFunc("GET /v1/plans/today", h.planToday)
	api.HandleFunc("GET /v1/plans/{id}", h.plan)
	api.HandleFunc("PUT /v1/plans/{id}", h.putPlan)
	api.HandleFunc("POST /v1/plans/{id}/enroll", h.enroll)
	api.HandleFunc("DELETE /v1/plans/enrollment", h.unenroll)
	api.HandleFunc("GET /v1/sessions", h.sessions)
	api.HandleFunc("GET /v1/sessions/{id}", h.session)
	api.HandleFunc("PUT /v1/sessions/{id}", h.putSession)
	api.HandleFunc("DELETE /v1/sessions/{id}", h.deleteSession)
	api.HandleFunc("POST /v1/sessions/{id}/complete", h.completeSession)
	api.HandleFunc("/v1/", httpx.NotFound)
	o := auth.Options{Log: log, Verifier: auth.NewAPIKey(benchKey, uuid.New()),
		Limit: func(*http.Request, auth.Principal) (string, bool, time.Duration) {
			b.tokens.Add(1)
			return "default", true, 0
		}}
	o.OnAuth = auth.LoadUser(ensureUser(b.st))
	mux := http.NewServeMux()
	mux.Handle("/v1/", auth.Middleware(o, betaGate(beta.Gate, httpx.Timeout(readTimeout, httpx.WithRoutes(api)))))
	mux.Handle("POST /v1/train/import", importRoute(o, beta.Gate, h.trainImport))
	b.srv = httpx.Instrument(log, httpx.Recover(httpx.SecurityHeaders(mux)))
	return b
}

// user is a new account in tz, with consent to the current policy unless consent is false.
func (b *bench) user(tz string, consent bool) (uuid.UUID, string) {
	b.t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := b.st.EnsureUser(ctx, store.Identity{ID: id}); err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.st.UpdateUser(ctx, id, model.UserPatch{TZ: &tz}, nil); err != nil {
		b.t.Fatal(err)
	}
	if consent {
		if err := b.st.GrantConsent(ctx, id, benchPolicy, nil); err != nil {
			b.t.Fatal(err)
		}
	}
	return id, benchKey + ":" + id.String()
}

// call sends body (a string is sent as it is, anything else as JSON; nil is no body).
func (b *bench) call(token, method, path string, body any) *httptest.ResponseRecorder {
	b.t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	case []byte:
		rd = bytes.NewReader(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			b.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	b.srv.ServeHTTP(rec, req)
	return rec
}

// want fails unless the answer has status, and decodes its body into out (if not nil).
func (b *bench) want(rec *httptest.ResponseRecorder, status int, out any) {
	b.t.Helper()
	if rec.Code != status {
		b.t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			b.t.Fatalf("%v: %s", err, rec.Body)
		}
	}
}

// imports posts an import and wants a 200.
func (b *bench) imports(token string, req any) model.TrainImportResponse {
	b.t.Helper()
	var out model.TrainImportResponse
	b.want(b.call(token, http.MethodPost, "/v1/train/import", req), http.StatusOK, &out)
	return out
}

func (b *bench) count(q string, args ...any) int {
	b.t.Helper()
	var n int
	if err := b.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		b.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// outbox is the user's outbox rows: the ingested events with their reason and days.
func (b *bench) outbox(user uuid.UUID) []model.IngestedEvent {
	b.t.Helper()
	rows, err := b.pool.Query(context.Background(), `SELECT routing_key, reason, payload::text FROM outbox WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		b.t.Fatal(err)
	}
	defer rows.Close()
	var out []model.IngestedEvent
	for rows.Next() {
		var key, reason, payload string
		if err := rows.Scan(&key, &reason, &payload); err != nil {
			b.t.Fatal(err)
		}
		var ev model.IngestedEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			b.t.Fatal(err)
		}
		if key != "ingested" || reason != ev.Reason {
			b.t.Fatalf("outbox row %s/%s: %s", key, reason, payload)
		}
		if strings.Contains(payload, "session_id") {
			b.t.Fatalf("an import's event carries no session: %s", payload)
		}
		out = append(out, ev)
	}
	return out
}

// scored marks days as scoring would after handling them.
func (b *bench) scored(user uuid.UUID, days ...string) {
	b.t.Helper()
	for _, d := range days {
		if _, _, err := b.st.MarkScored(context.Background(), user, d, time.Now().Add(time.Second)); err != nil {
			b.t.Fatal(err)
		}
	}
}

func (b *bench) pending(user uuid.UUID) []string {
	b.t.Helper()
	days, err := b.st.PendingDays(context.Background(), user)
	if err != nil {
		b.t.Fatal(err)
	}
	return days
}

// ---- building requests -------------------------------------------------------------------

var (
	squatID  = train.BuiltinExerciseID("back-squat")
	benchID  = train.BuiltinExerciseID("bench-press")
	pullUpID = train.BuiltinExerciseID("pull-up")
)

// daysAgo is 17:00 UTC n days back: in Europe/Berlin and UTC the same local day.
func daysAgo(n int) time.Time {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -n).Add(17 * time.Hour)
}

func day(t time.Time) string { return t.Format(time.DateOnly) }

// done is an imported session at start: one exercise with a completed set per weight.
func done(start time.Time, ex uuid.UUID, kgs ...float64) model.ImportSession {
	end := start.Add(time.Hour)
	s := model.ImportSession{ID: uuid.New(), Title: "Lower", StartedAt: start, EndedAt: &end, Status: model.SessionCompleted,
		Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: ex}}}
	for k, kg := range kgs {
		reps, w, at := 5, kg, start.Add(time.Duration(10+3*k)*time.Minute)
		s.Exercises[0].Sets = append(s.Exercises[0].Sets, model.Set{ID: uuid.New(), SetIndex: k, Reps: &reps, WeightKG: &w,
			CompletedAt: &at, UpdatedAt: at})
	}
	return s
}

// twoDayPlan is a plan of the user's with two days of squats.
func twoDayPlan() model.Plan {
	tv := 100.0
	p := model.Plan{ID: uuid.New(), Name: "Upper / Lower", Goal: "general", DaysPerWeek: 2}
	for k := 0; k < 2; k++ {
		p.Days = append(p.Days, model.PlanDay{ID: uuid.New(), DayIndex: k, Name: "Day", Exercises: []model.PlanExercise{
			{ID: uuid.New(), ExerciseID: squatID, Sets: 3, RepsMin: 5, RepsMax: 8, TargetKind: model.TargetKG, TargetValue: &tv, RestSec: 120},
		}})
	}
	return p
}

func request(plans []model.Plan, sessions ...model.ImportSession) model.TrainImportRequest {
	if plans == nil {
		plans = []model.Plan{}
	}
	if sessions == nil {
		sessions = []model.ImportSession{}
	}
	return model.TrainImportRequest{Source: model.ImportSourceAlphaProgression, Plans: plans, Sessions: sessions}
}

// statuses is the results as "kind status[ code]" in order.
func statuses(res model.TrainImportResponse) []string {
	out := make([]string, 0, len(res.Results))
	for _, x := range res.Results {
		s := x.Kind + " " + x.Status
		if x.Code != "" {
			s += " " + x.Code
		}
		out = append(out, s)
	}
	return out
}
