package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Characterisation tests of ApplySync on a real Postgres (D145, D146): what the apply
// transaction deletes, folds and queues, the days it returns, that Emit runs once in the
// same transaction, that a failing step leaves nothing, and that syncs of one user
// serialise on the apply lock. They pin the behaviour the steps refactor must keep.

var berlin = mustLoc("Europe/Berlin")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func applyUser(t *testing.T, st *Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := st.EnsureUser(context.Background(), Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSample(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, typ string, at time.Time) uuid.UUID {
	t.Helper()
	hk := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO samples (user_id, type, start_at, end_at, value, source, hk_id)
		VALUES ($1, $2, $3, $3, 1, 'W', $4)`, user, typ, at, hk); err != nil {
		t.Fatal(err)
	}
	return hk
}

func seedSleep(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, end time.Time) uuid.UUID {
	t.Helper()
	hk := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id)
		VALUES ($1, $2::timestamptz - interval '1 h', $2, 'core', 'W', $3)`, user, end, hk); err != nil {
		t.Fatal(err)
	}
	return hk
}

type seedW struct {
	source  string
	start   time.Time
	created time.Time // zero: now()
	session bool
}

func seedWorkout(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, w seedW) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var session *uuid.UUID
	if w.session {
		s := uuid.New()
		session = &s
	}
	var created *time.Time
	if !w.created.IsZero() {
		created = &w.created
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, session_id, created_at)
		VALUES ($1, $2, 'running', $3, $3::timestamptz + interval '1 h', $4, $5, COALESCE($6, now()))`,
		id, user, w.start, w.source, session, created); err != nil {
		t.Fatal(err)
	}
	return id
}

func countRows(t *testing.T, q querier, query string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func queuedDays(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT day::text FROM score_queue WHERE user_id=$1 ORDER BY day`, user)
	if err != nil {
		t.Fatal(err)
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return days
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) int {
	t.Helper()
	return countRows(t, pool, `SELECT count(*) FROM outbox WHERE user_id=$1`, user)
}

// emitter is an Emit that writes one outbox row in the transaction and records its calls.
type emitter struct {
	user  uuid.UUID
	calls atomic.Int32
	mu    sync.Mutex
	days  [][]string
	check func(pgx.Tx) // runs inside the transaction, optional
}

func (e *emitter) emit(tx pgx.Tx, days []string) error {
	e.calls.Add(1)
	e.mu.Lock()
	e.days = append(e.days, slices.Clone(days))
	e.mu.Unlock()
	if e.check != nil {
		e.check(tx)
	}
	_, err := tx.Exec(context.Background(), `INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', $1, 'ingested', '{}'::jsonb)`, e.user)
	return err
}

func del(kind string, id uuid.UUID) model.DeletedRecord {
	return model.DeletedRecord{Kind: kind, ID: id}
}

// A deletion names rows by kind; the day set is the local days of what was removed. A
// night sample (hrv, respiration, SpO2) also changes the day after it; a workout changes
// its day and the next (tomorrow's sleep need uses today's strain) and, with a session on
// it, the day before.
func TestApplySyncDeletedRowsDays(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		seed func(*testing.T, uuid.UUID) model.DeletedRecord
		days []string
	}{
		{"steps do not spill", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleSteps, utc("2026-01-12T10:00:00Z")))
		}, []string{"2026-01-12"}},
		{"hrv sample spills to the next day", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleHRVSDNN, utc("2026-01-20T05:00:00Z")))
		}, []string{"2026-01-20", "2026-01-21"}},
		{"respiratory rate spills", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleRespiratoryRate, utc("2026-01-20T05:00:00Z")))
		}, []string{"2026-01-20", "2026-01-21"}},
		{"spo2 spills", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleSpO2, utc("2026-01-20T05:00:00Z")))
		}, []string{"2026-01-20", "2026-01-21"}},
		{"local day of a night sample is the user's, not UTC's", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			// 23:30Z is 00:30 the next day in Berlin (CET).
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleHRVSDNN, utc("2026-01-12T23:30:00Z")))
		}, []string{"2026-01-13", "2026-01-14"}},
		{"sleep: the day of its end", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSleep, seedSleep(t, pool, u, utc("2026-01-12T23:30:00Z")))
		}, []string{"2026-01-13"}},
		{"workout: its day and the next", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-01-12T10:00:00Z")}))
		}, []string{"2026-01-12", "2026-01-13"}},
		{"workout after local midnight: the local day, not UTC's", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-01-12T23:30:00Z")}))
		}, []string{"2026-01-13", "2026-01-14"}},
		{"workout with a session: the day before too", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-01-12T10:00:00Z"), session: true}))
		}, []string{"2026-01-11", "2026-01-12", "2026-01-13"}},
		// Characterisation, not endorsement. The next and the previous day are the start +-24
		// absolute hours, which on a day with a clock change is not the neighbouring local day.
		// This is pre-existing (ApplySync's apply.go, planFold in fold.go and touchedDays in
		// cmd/ingest compute it the same way), it is symmetrical, and it also hits the night
		// samples (hrv, respiratory rate, SpO2). Four shapes in Europe/Berlin 2026, each missing
		// a neighbouring day, each pinned as the code behaves today. Follow-up: one helper that
		// derives the next and previous local date from the date (AddDate(0, 0, +-1)), used by
		// touchedDays, the deletions and planFold together; these cases then flip to the full
		// day set, and the touchedDays table (C3) should pin the same shapes.
		//
		// The night sample has the same quirk: 00:30 CEST 10-25 + 24 h is 23:30 CET 10-25.
		{"25-hour day, night sample at 00:30: the next day is missed", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedSample, seedSample(t, pool, u, model.SampleHRVSDNN, utc("2026-10-24T22:30:00Z")))
		}, []string{"2026-10-25"}},
		// Autumn, next day: 00:30 CEST 10-25 + 24 h = 23:30 CET 10-25, so 10-26 is not queued.
		{"25-hour day, workout at 00:30: the next day is missed", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-10-24T22:30:00Z")}))
		}, []string{"2026-10-25"}},
		// Autumn, day before: 23:30 CET 10-25 - 24 h = 00:30 CEST 10-25, so 10-24 is not queued.
		{"25-hour day, workout at 23:30 with a session: the day before is missed", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-10-25T22:30:00Z"), session: true}))
		}, []string{"2026-10-25", "2026-10-26"}},
		// Spring, next day: 23:30 CET 03-28 + 24 h = 00:30 CEST 03-30, so 03-29 is skipped.
		{"23-hour day, workout at 23:30 the day before: the day after it is skipped", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-03-28T22:30:00Z")}))
		}, []string{"2026-03-28", "2026-03-30"}},
		// Spring, day before: 00:30 CEST 03-30 - 24 h = 23:30 CET 03-28, so 03-29 is lost.
		{"23-hour day, workout at 00:30 the day after with a session: the day between is lost", func(t *testing.T, u uuid.UUID) model.DeletedRecord {
			return del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-03-29T22:30:00Z"), session: true}))
		}, []string{"2026-03-28", "2026-03-30", "2026-03-31"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := applyUser(t, st)
			d := c.seed(t, u)
			out, err := st.ApplySync(ctx, u, Apply{Deleted: []model.DeletedRecord{d}, Loc: berlin})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(out.Days, c.days) {
				t.Fatalf("days = %v, want %v", out.Days, c.days)
			}
			if got := queuedDays(t, pool, u); !slices.Equal(got, c.days) {
				t.Fatalf("score_queue = %v, want %v", got, c.days)
			}
		})
	}
}

// Deletions are scoped to the user and the kind: an unknown id, the nil id, another
// user's id, a ryft-session workout and an id sent twice are "unknown" or counted once;
// a kind of a later app is skipped without being counted.
func TestApplySyncDeletionsAndUnknownIDs(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u, other := applyUser(t, st), applyUser(t, st)
	at := utc("2026-01-12T10:00:00Z")
	s1, s2 := seedSample(t, pool, u, model.SampleSteps, at), seedSample(t, pool, u, model.SampleSteps, at.Add(time.Hour))
	foreign := seedSample(t, pool, other, model.SampleSteps, at)
	sl := seedSleep(t, pool, u, at)
	w := seedWorkout(t, pool, u, seedW{source: "W", start: at})
	sessionW := seedWorkout(t, pool, u, seedW{source: model.SessionSource, start: at})
	keep := seedSample(t, pool, u, model.SampleSteps, at.Add(2*time.Hour))
	sleepOnSampleID := seedSleep(t, pool, u, at.Add(5*time.Hour)) // an id of kind sleep, named as a sample

	out, err := st.ApplySync(ctx, u, Apply{Loc: berlin, Deleted: []model.DeletedRecord{
		del(model.DeletedSample, s1), del(model.DeletedSample, s1), // twice: one
		del(model.DeletedSample, s2),
		del(model.DeletedSample, foreign),         // another user's
		del(model.DeletedSample, uuid.New()),      // nobody's
		del(model.DeletedSample, uuid.Nil),        // the nil id is unknown
		del(model.DeletedSample, sleepOnSampleID), // right id, wrong kind
		del(model.DeletedSleep, sl),
		del(model.DeletedSleep, uuid.New()),
		del(model.DeletedWorkout, w),
		del(model.DeletedWorkout, sessionW), // ryft-session: not deletable
		del(model.DeletedWorkout, uuid.New()),
		del("ring", uuid.New()), // a later app's kind: skipped
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (model.SyncDeleted{Samples: 2, Sleep: 1, Workouts: 1}); out.Deleted != want {
		t.Fatalf("deleted = %+v, want %+v", out.Deleted, want)
	}
	// samples: s1, s2 deleted; foreign, uuid.New, Nil, sleepOnSampleID unknown (4).
	// sleep: sl deleted; one unknown. workouts: w deleted; sessionW, uuid.New unknown.
	if want := (model.SyncDeleted{Samples: 4, Sleep: 1, Workouts: 2}); out.Unknown != want {
		t.Fatalf("unknown = %+v, want %+v", out.Unknown, want)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("user's samples left = %d, want 1 (%v kept)", n, keep)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1`, other); n != 1 {
		t.Fatal("another user's sample was deleted")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("sleep left = %d, want 1 (the one named as a sample)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1 AND source=$2`, u, model.SessionSource); n != 1 {
		t.Fatal("the ryft-session workout was deleted")
	}
}

// Only unknown ids: nothing is deleted, there are no days, so nothing is queued and Emit
// does not run; the lock was taken and released with the commit.
func TestApplySyncNothingToQueueRunsNoEmit(t *testing.T) {
	st, pool := testStore(t)
	u := applyUser(t, st)
	e := &emitter{user: u}
	out, err := st.ApplySync(context.Background(), u, Apply{Loc: berlin, Emit: e.emit, Deleted: []model.DeletedRecord{
		del(model.DeletedSample, uuid.New()), del(model.DeletedSleep, uuid.Nil), del("ring", uuid.New())}})
	if err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 0 || outboxCount(t, pool, u) != 0 || len(queuedDays(t, pool, u)) != 0 {
		t.Fatal("emit or queue ran without days")
	}
	if out.Days != nil || out.Unknown != (model.SyncDeleted{Samples: 1, Sleep: 1}) {
		t.Fatalf("out = %+v", out)
	}
}

// Emit runs exactly once, in the apply transaction, with the final sorted distinct days
// (the request's, the deletions' and the fold's); the outbox row and the queue rows commit
// with the deletions. A day already scored is queued again.
func TestApplySyncEmitsOnceInTheSameTransaction(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := applyUser(t, st)
	hrv := seedSample(t, pool, u, model.SampleHRVSDNN, utc("2026-01-20T05:00:00Z"))
	if _, err := pool.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at) VALUES ($1, '2026-01-21', now() - interval '1 h', now())`, u); err != nil {
		t.Fatal(err)
	}
	e := &emitter{user: u}
	e.check = func(tx pgx.Tx) {
		if n := countRows(t, tx, `SELECT count(*) FROM samples WHERE user_id=$1`, u); n != 0 {
			t.Error("emit does not see the deletion in its own transaction")
		}
		if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1`, u); n != 1 {
			t.Error("the deletion is visible outside the transaction before it commits")
		}
	}
	out, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-22", "2026-01-20", "2026-01-22"}, Loc: berlin, Emit: e.emit,
		Deleted: []model.DeletedRecord{del(model.DeletedSample, hrv)}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-01-20", "2026-01-21", "2026-01-22"}
	if !slices.Equal(out.Days, want) {
		t.Fatalf("days = %v, want %v", out.Days, want)
	}
	if e.calls.Load() != 1 || !slices.Equal(e.days[0], want) {
		t.Fatalf("emit calls = %d days = %v, want one call with %v", e.calls.Load(), e.days, want)
	}
	if n := outboxCount(t, pool, u); n != 1 {
		t.Fatalf("outbox rows = %d, want 1", n)
	}
	if got := queuedDays(t, pool, u); !slices.Equal(got, want) {
		t.Fatalf("score_queue = %v, want %v", got, want)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM score_queue WHERE user_id=$1 AND scored_at IS NOT NULL`, u); n != 0 {
		t.Fatal("a re-queued day is still marked scored")
	}
}

// With no Emit and only the request's days, the days are queued and returned.
func TestApplySyncDaysOnlyWithoutEmit(t *testing.T) {
	st, pool := testStore(t)
	u := applyUser(t, st)
	out, err := st.ApplySync(context.Background(), u, Apply{Days: []string{"2026-02-02", "2026-02-01"}, Loc: berlin})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2026-02-01", "2026-02-02"}; !slices.Equal(out.Days, want) || !slices.Equal(queuedDays(t, pool, u), want) {
		t.Fatalf("days = %v queue = %v", out.Days, queuedDays(t, pool, u))
	}
	if out.Folded != 0 || out.FoldSkipped || out.Deleted != (model.SyncDeleted{}) {
		t.Fatalf("out = %+v", out)
	}
}

func foldScope(src string, at time.Time) *FoldScope {
	return &FoldScope{Sources: []string{src}, From: at.Add(-24 * time.Hour), To: at.Add(24 * time.Hour)}
}

// The fold removes same-source copies in the scope (keeping the latest upload), after the
// deletions, and returns their days; it does not touch other sources, rows outside the
// scope or ryft-session rows, and runs only when the request asks for it.
func TestApplySyncFoldsSameSourceCopies(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := applyUser(t, st)
	at := utc("2026-01-12T10:00:00Z")
	old := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-time.Hour), session: true})
	kept := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.Add(30 * time.Second), created: at})
	otherSrc := seedWorkout(t, pool, u, seedW{source: "Watch", start: at, created: at})
	farAway := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.AddDate(0, 0, 10), created: at})
	farAway2 := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.AddDate(0, 0, 10).Add(time.Second), created: at})
	ryftS := seedWorkout(t, pool, u, seedW{source: model.SessionSource, start: at})

	// Without Fold nothing is folded.
	out, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-12"}, Loc: berlin})
	if err != nil || out.Folded != 0 || countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1`, u) != 6 {
		t.Fatalf("fold ran without a scope: %+v %v", out, err)
	}

	e := &emitter{user: u}
	out, err = st.ApplySync(ctx, u, Apply{Loc: berlin, Fold: foldScope("Garmin", at), Emit: e.emit})
	if err != nil {
		t.Fatal(err)
	}
	if out.Folded != 1 || out.FoldSkipped || out.Deleted != (model.SyncDeleted{}) {
		t.Fatalf("out = %+v, want one fold and no deletions", out)
	}
	if want := []string{"2026-01-11", "2026-01-12", "2026-01-13"}; !slices.Equal(out.Days, want) {
		t.Fatalf("days = %v, want %v (the dropped copy's day, the next, and the day before for its session)", out.Days, want)
	}
	gone := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1 AND id=$2`, u, old)
	left := func(id uuid.UUID) int { return countRows(t, pool, `SELECT count(*) FROM workouts WHERE id=$1`, id) }
	if gone != 0 || left(kept) != 1 || left(otherSrc) != 1 || left(farAway) != 1 || left(farAway2) != 1 || left(ryftS) != 1 {
		t.Fatal("the fold removed the wrong rows")
	}
	if e.calls.Load() != 1 || !slices.Equal(e.days[0], out.Days) || outboxCount(t, pool, u) != 1 {
		t.Fatalf("emit calls = %d", e.calls.Load())
	}
}

// Deletions run before the fold: deleting the copy the fold would have removed leaves
// nothing to fold, and the deletion is counted as a deletion, not a fold.
func TestApplySyncDeletionsRunBeforeTheFold(t *testing.T) {
	st, pool := testStore(t)
	u := applyUser(t, st)
	at := utc("2026-01-12T10:00:00Z")
	old := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-time.Hour)})
	kept := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at})
	out, err := st.ApplySync(context.Background(), u, Apply{Loc: berlin, Fold: foldScope("Garmin", at),
		Deleted: []model.DeletedRecord{del(model.DeletedWorkout, old)}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Folded != 0 || out.Deleted.Workouts != 1 || out.Unknown != (model.SyncDeleted{}) {
		t.Fatalf("out = %+v, want one deletion, no fold, no unknown", out)
	}
	if countRows(t, pool, `SELECT count(*) FROM workouts WHERE id=$1`, kept) != 1 {
		t.Fatal("the kept copy is gone")
	}
}

// A scope with more than FoldMaxRows rows is skipped: nothing is folded or locked
// for deletion, FoldSkipped says so, and the rest of the apply still happens.
func TestApplySyncFoldSkippedAboveTheRowBound(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := applyUser(t, st)
	at := utc("2026-01-12T10:00:00Z")
	if _, err := pool.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		SELECT gen_random_uuid(), $1, 'running', $2::timestamptz + n * interval '1 day', $2::timestamptz + n * interval '1 day' + interval '1 h', 'Garmin'
		FROM generate_series(1, $3) AS n`, u, at, FoldMaxRows+1); err != nil {
		t.Fatal(err)
	}
	// Two copies that would fold if the scope were read.
	seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-time.Hour)})
	seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at})
	scope := &FoldScope{Sources: []string{"Garmin"}, From: at.Add(-24 * time.Hour), To: at.AddDate(30, 0, 0)}
	e := &emitter{user: u}
	out, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-12"}, Loc: berlin, Fold: scope, Emit: e.emit})
	if err != nil {
		t.Fatal(err)
	}
	if !out.FoldSkipped || out.Folded != 0 {
		t.Fatalf("out = %+v, want the fold skipped", out)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1`, u); n != FoldMaxRows+3 {
		t.Fatalf("workouts = %d, want all %d kept", n, FoldMaxRows+3)
	}
	if !slices.Equal(out.Days, []string{"2026-01-12"}) || e.calls.Load() != 1 || outboxCount(t, pool, u) != 1 {
		t.Fatalf("the rest of the apply did not happen: %+v, emit %d", out, e.calls.Load())
	}
}

// A failing step rolls back every step before it: no deletion, no fold, no queue row and
// no outbox row, whichever step fails; the error names the step.
func TestApplySyncRollsBackWhenAStepFails(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	fail := func(t *testing.T, table, when string) { failTrigger(t, pool, table, when) }
	errEmit := errors.New("emit failed")
	cases := []struct {
		name    string
		prepare func(*testing.T)
		emit    func(pgx.Tx, []string) error
		prefix  string // the wrapped error's prefix, "" when it is Emit's own error
		delW    bool   // the request deletes a workout by id
	}{
		{"sleep deletion", func(t *testing.T) { fail(t, "sleep_segments", "DELETE") }, nil, "delete sleep: ", false},
		{"workout deletion", func(t *testing.T) { fail(t, "workouts", "DELETE") }, nil, "delete workouts: ", true},
		{"fold delete", func(t *testing.T) { fail(t, "workouts", "DELETE") }, nil, "fold delete: ", false},
		{"queue", func(t *testing.T) { fail(t, "score_queue", "INSERT") }, nil, "queue days: ", true},
		{"emit", func(*testing.T) {}, func(tx pgx.Tx, _ []string) error {
			if _, err := tx.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', NULL, 'x', '{}'::jsonb)`); err != nil {
				return err
			}
			return errEmit
		}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := applyUser(t, st)
			at := utc("2026-01-12T10:00:00Z")
			s := seedSample(t, pool, u, model.SampleSteps, at)
			sl := seedSleep(t, pool, u, at)
			w := seedWorkout(t, pool, u, seedW{source: "W", start: at})
			seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.AddDate(0, 0, 3), created: at.Add(-time.Hour)})
			seedWorkout(t, pool, u, seedW{source: "Garmin", start: at.AddDate(0, 0, 3), created: at})
			c.prepare(t)
			deleted := []model.DeletedRecord{del(model.DeletedSample, s), del(model.DeletedSleep, sl)}
			if c.delW {
				deleted = append(deleted, del(model.DeletedWorkout, w))
			}
			out, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-12"}, Deleted: deleted, Loc: berlin, Emit: c.emit,
				Fold: &FoldScope{Sources: []string{"Garmin"}, From: at, To: at.AddDate(0, 0, 5)}})
			if err == nil {
				t.Fatal("ApplySync succeeded")
			}
			if c.prefix != "" && !strings.HasPrefix(err.Error(), c.prefix) {
				t.Fatalf("error %q does not start with %q", err, c.prefix)
			}
			if c.prefix == "" && !errors.Is(err, errEmit) {
				t.Fatalf("error %q is not Emit's own", err)
			}
			if c.prefix == "" && err.Error() != errEmit.Error() {
				t.Fatalf("Emit's error was wrapped: %q", err)
			}
			if !zeroApplied(out) {
				t.Fatalf("out = %+v on error, want zero", out)
			}
			if n := countRows(t, pool, `SELECT (SELECT count(*) FROM samples WHERE user_id=$1) + (SELECT count(*) FROM sleep_segments WHERE user_id=$1)
				+ (SELECT count(*) FROM workouts WHERE user_id=$1)`, u); n != 5 {
				t.Fatalf("rows left = %d, want all 5 (the apply left something half done)", n)
			}
			if len(queuedDays(t, pool, u)) != 0 || countRows(t, pool, `SELECT count(*) FROM outbox WHERE routing_key='x' OR user_id=$1`, u) != 0 {
				t.Fatal("a queue or outbox row survived the rollback")
			}
		})
	}
}

// zeroApplied reports whether a is the zero Applied (what a failed apply returns).
func zeroApplied(a Applied) bool {
	return a.Days == nil && a.Deleted == (model.SyncDeleted{}) && a.Unknown == (model.SyncDeleted{}) && a.Folded == 0 && !a.FoldSkipped
}

// The fold metrics count what committed: an apply that folds (or skips the fold) and then
// fails a later step is rolled back and counts nothing; the same apply that commits counts
// once (the control, so the rolled-back half is not vacuous).
func TestApplySyncFoldMetricsCountOnlyCommittedApplies(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	at := utc("2026-01-12T10:00:00Z")
	folded := func() float64 { return testutil.ToFloat64(metrics.WorkoutsFolded.WithLabelValues("same_source")) }
	skipped := func() float64 { return testutil.ToFloat64(metrics.WorkoutsFoldSkipped.WithLabelValues("too_many")) }
	cases := []struct {
		name             string
		bound            bool // true: more than FoldMaxRows rows in scope, the fold is skipped
		wantFold, wantSk float64
	}{
		{"a fold of one workout", false, 1, 0},
		{"a skipped fold", true, 0, 1},
	}
	for _, c := range cases {
		for _, commit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, commit=%v", c.name, commit), func(t *testing.T) {
				u := applyUser(t, st)
				if c.bound {
					if _, err := pool.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
						SELECT gen_random_uuid(), $1, 'running', $2::timestamptz + n * interval '1 day', $2::timestamptz + n * interval '1 day' + interval '1 h', 'Garmin'
						FROM generate_series(1, $3) AS n`, u, at, FoldMaxRows+1); err != nil {
						t.Fatal(err)
					}
				} else {
					seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-time.Hour)})
					seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at})
				}
				if !commit {
					failTrigger(t, pool, "score_queue", "INSERT") // after the fold, before the commit
				}
				f0, s0 := folded(), skipped()
				_, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-12"}, Loc: berlin,
					Fold: &FoldScope{Sources: []string{"Garmin"}, From: at, To: at.AddDate(30, 0, 0)}})
				if commit != (err == nil) {
					t.Fatalf("commit=%v but err = %v", commit, err)
				}
				wantF, wantS := c.wantFold, c.wantSk
				if !commit {
					wantF, wantS = 0, 0
				}
				if df, ds := folded()-f0, skipped()-s0; df != wantF || ds != wantS {
					t.Fatalf("counted folded %v skipped %v, want %v and %v", df, ds, wantF, wantS)
				}
			})
		}
	}
}

// Two syncs of one user serialise on the apply lock (D145, class 7419005): while another
// transaction holds it, ApplySync waits and then completes; another user's apply does not
// wait.
func TestApplySyncSerialisesPerUserOnTheApplyLock(t *testing.T) {
	st, pool := testStore(t)
	// A deadline on every call: if the lock ever serialises more than one user (keyed on the
	// class alone, say), the other user's apply blocks behind the holder and would hang the
	// whole test binary until go test's timeout instead of failing this test.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, other := applyUser(t, st), applyUser(t, st)

	holder := holdApplyLock(t, pool, u)
	done := make(chan error, 1)
	go func() {
		_, err := st.ApplySync(ctx, u, Apply{Days: []string{"2026-01-12"}, Loc: berlin})
		done <- err
	}()
	waitForApplyLockWaiter(t, pool, done)
	if _, err := st.ApplySync(ctx, other, Apply{Days: []string{"2026-01-12"}, Loc: berlin}); err != nil {
		t.Fatalf("another user's apply: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("ApplySync finished while the lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := queuedDays(t, pool, u); !slices.Equal(got, []string{"2026-01-12"}) {
		t.Fatalf("queue = %v", got)
	}
}

// holdApplyLock opens a transaction that holds the user's apply lock; the test's cleanup
// rolls it back, so a failing assertion does not leave the lock held (the pool's Close
// would wait for it).
func holdApplyLock(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) pgx.Tx {
	t.Helper()
	ctx := context.Background() // nobody holds the lock yet: this does not wait
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(ctx, lockApply, user); err != nil {
		t.Fatal(err)
	}
	return holder
}

// waitForApplyLockWaiter polls until a session waits on the apply lock (class 7419005),
// failing if the apply behind done finishes first or nobody waits within 10 s.
func waitForApplyLockWaiter(t *testing.T, pool *pgxpool.Pool, done <-chan error) {
	t.Helper()
	waiting := func() int {
		return countRows(t, pool, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=7419005
			AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
	}
	deadline := time.Now().Add(10 * time.Second)
	for waiting() == 0 {
		select {
		case err := <-done:
			t.Fatalf("ApplySync finished while the lock was held: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("ApplySync never waited on the apply lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// "Lock first", behaviourally and independent of the statement trace: while another
// transaction holds the user's apply lock, a waiting ApplySync has not yet touched a row.
// If the lock moved behind the deletions, the waiting call would hold the workout's row
// lock and the NOWAIT probe below would fail with 55P03 (lock_not_available).
func TestApplySyncTakesNoRowLockBeforeTheApplyLock(t *testing.T) {
	st, pool := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := applyUser(t, st)
	w := seedWorkout(t, pool, u, seedW{source: "W", start: utc("2026-01-12T10:00:00Z")})

	holder := holdApplyLock(t, pool, u)
	done := make(chan error, 1)
	go func() {
		_, err := st.ApplySync(ctx, u, Apply{Loc: berlin, Deleted: []model.DeletedRecord{del(model.DeletedWorkout, w)}})
		done <- err
	}()
	waitForApplyLockWaiter(t, pool, done)
	if _, err := holder.Exec(ctx, `SAVEPOINT probe`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT id FROM workouts WHERE id=$1 FOR UPDATE NOWAIT`, w); err != nil {
		t.Errorf("the ApplySync waiting on the apply lock already holds a row lock: %v", err)
		if _, err := holder.Exec(ctx, `ROLLBACK TO SAVEPOINT probe`); err != nil {
			t.Fatal(err)
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workouts WHERE id=$1`, w); n != 0 {
		t.Fatal("the deletion did not happen once the lock was released")
	}
}

// Concurrent applies of one user never run their transaction at the same time.
func TestApplySyncConcurrentAppliesOfOneUserDoNotOverlap(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	u := applyUser(t, st)
	var inflight, peak atomic.Int32
	e := &emitter{user: u}
	e.check = func(pgx.Tx) {
		n := inflight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.ApplySync(ctx, u, Apply{Days: []string{fmt.Sprintf("2026-02-%02d", i+1)}, Loc: berlin, Emit: e.emit})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.calls.Load() != 6 || peak.Load() != 1 {
		t.Fatalf("emit calls = %d, peak concurrent apply transactions = %d, want 6 and 1", e.calls.Load(), peak.Load())
	}
}
