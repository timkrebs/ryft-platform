package store

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// A pass that fails after the attachments and before the rating moves rolls everything back, and the retry moves
// each rating with its session from the original mapping. Two completed sessions, each attached to a HealthKit
// workout with a rating on it: A on hk1 (rated 8), B on hk2 (rated 5). The pass moves A to hk2 and B to its own
// workout (no HealthKit one matches any more): a chain, so hk2's rating moves on before hk1's moves in.
func TestSessionPassRollsBackAndRetries(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	u := applyUser(t, st)
	at := utc("2026-11-25T17:00:00Z")
	a, b := uuid.New(), uuid.New()
	hk1 := seedWorkout(t, pool, u, seedW{source: "Watch", start: at})
	hk2 := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at})
	ownB := train.SessionWorkoutID(u, b)
	for _, c := range []struct{ session, workout uuid.UUID }{{a, hk1}, {b, hk2}} {
		if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $3::timestamptz + interval '1 h', 'completed', $4)`,
			u, c.session, at, c.workout); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE workouts SET session_id=$3, muscular_load=10 WHERE user_id=$1 AND id=$2`, u, c.workout, c.session); err != nil {
			t.Fatal(err)
		}
	}
	rate(t, pool, u, hk1, 8) // A's
	rate(t, pool, u, hk2, 5) // B's
	previous := map[uuid.UUID]uuid.UUID{a: hk1, b: hk2}
	atts := []SessionAttachment{
		{SessionID: a, WorkoutID: hk2, TRIMP: 30, Strain: 7, VolumeKG: 900},
		{SessionID: b, WorkoutID: ownB, Own: true, Start: at, End: at.Add(time.Hour), TRIMP: 20, Strain: 5, VolumeKG: 400},
	}
	sessionWorkout := func(id uuid.UUID) *uuid.UUID {
		var w *uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT workout_id FROM sessions WHERE user_id=$1 AND id=$2`, u, id).Scan(&w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	carries := func(w uuid.UUID) *uuid.UUID {
		var s *uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT session_id FROM workouts WHERE user_id=$1 AND id=$2`, u, w).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	boom := errors.New("injected")
	sessionPassFault = func() error { return boom }
	t.Cleanup(func() { sessionPassFault = nil })
	if err := scoring.ApplySessionPass(ctx, u, atts, previous); !errors.Is(err, boom) {
		t.Fatalf("the pass: %v, want the injected failure", err)
	}
	// Nothing happened: the pointers, the attachments, B's own workout and the ratings are as they were.
	if w := sessionWorkout(a); w == nil || *w != hk1 {
		t.Fatalf("session A's workout after a failed pass: %v", w)
	}
	if w := sessionWorkout(b); w == nil || *w != hk2 {
		t.Fatalf("session B's workout after a failed pass: %v", w)
	}
	if s := carries(hk1); s == nil || *s != a {
		t.Fatalf("hk1 carries %v after a failed pass", s)
	}
	if s := carries(hk2); s == nil || *s != b {
		t.Fatalf("hk2 carries %v after a failed pass", s)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workouts WHERE user_id=$1 AND id=$2`, u, ownB); n != 0 {
		t.Fatalf("B's own workout exists after a failed pass")
	}
	if got, want := ratings(t, pool, u), map[uuid.UUID]float64{hk1: 8, hk2: 5}; !maps.Equal(got, want) {
		t.Fatalf("ratings after a failed pass: %v, want %v", got, want)
	}

	// The retry, twice (the pass is idempotent): each rating on its session's workout.
	sessionPassFault = nil
	for i := 1; i <= 2; i++ {
		if err := scoring.ApplySessionPass(ctx, u, atts, previous); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if got, want := ratings(t, pool, u), map[uuid.UUID]float64{hk2: 8, ownB: 5}; !maps.Equal(got, want) {
			t.Fatalf("retry %d: ratings %v, want A's 8 on hk2 and B's 5 on its own workout", i, got)
		}
		if w := sessionWorkout(a); w == nil || *w != hk2 {
			t.Fatalf("retry %d: session A's workout %v", i, w)
		}
		if w := sessionWorkout(b); w == nil || *w != ownB {
			t.Fatalf("retry %d: session B's workout %v", i, w)
		}
		if s := carries(hk2); s == nil || *s != a {
			t.Fatalf("retry %d: hk2 carries %v", i, s)
		}
		if s := carries(hk1); s != nil {
			t.Fatalf("retry %d: hk1 still carries %v", i, s)
		}
		if s := carries(ownB); s == nil || *s != b {
			t.Fatalf("retry %d: B's own workout carries %v", i, s)
		}
		var src string
		if err := pool.QueryRow(ctx, `SELECT source FROM workouts WHERE user_id=$1 AND id=$2`, u, ownB).Scan(&src); err != nil || src != model.SessionSource {
			t.Fatalf("B's own workout: %q %v", src, err)
		}
		previous = map[uuid.UUID]uuid.UUID{a: hk2, b: ownB} // what the next pass reads
	}
}

// Two sessions that swap two rated workouts would each move onto the other's rating: the ratings stay where they
// are (the pass does not fail, and loses nothing); a one-way move onto a rated workout that stays is refused too.
func TestSessionPassSwapKeepsBothRatings(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	u := applyUser(t, st)
	at := utc("2026-11-25T17:00:00Z")
	a, b := uuid.New(), uuid.New()
	hk1 := seedWorkout(t, pool, u, seedW{source: "Watch", start: at})
	hk2 := seedWorkout(t, pool, u, seedW{source: "Garmin", start: at})
	for _, c := range []struct{ session, workout uuid.UUID }{{a, hk1}, {b, hk2}} {
		if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, ended_at, status, workout_id) VALUES ($1, $2, $3, $3::timestamptz + interval '1 h', 'completed', $4)`,
			u, c.session, at, c.workout); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE workouts SET session_id=$3 WHERE user_id=$1 AND id=$2`, u, c.workout, c.session); err != nil {
			t.Fatal(err)
		}
	}
	rate(t, pool, u, hk1, 8)
	rate(t, pool, u, hk2, 5)
	atts := []SessionAttachment{{SessionID: a, WorkoutID: hk2}, {SessionID: b, WorkoutID: hk1}}
	if err := scoring.ApplySessionPass(ctx, u, atts, map[uuid.UUID]uuid.UUID{a: hk1, b: hk2}); err != nil {
		t.Fatal(err)
	}
	if got, want := ratings(t, pool, u), map[uuid.UUID]float64{hk1: 8, hk2: 5}; !maps.Equal(got, want) {
		t.Fatalf("ratings after a swap: %v, want them kept as they were %v", got, want)
	}
}

// A time edit of a session whose workout is gone from its session (not completed any more) is an error, not a write.
func TestEnsureSessionWorkoutTimesNeedTheCompletedSession(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	scoring := New(dupRolePool(t, pool, "ryft_scoring"))
	u := applyUser(t, st)
	at := utc("2026-11-25T17:00:00Z")
	session := uuid.New()
	own := train.SessionWorkoutID(u, session)
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, ended_at, status) VALUES ($1, $2, $3, $3::timestamptz + interval '1 h', 'completed')`, u, session, at); err != nil {
		t.Fatal(err)
	}
	if err := scoring.EnsureSessionWorkout(ctx, u, own, session, at, at.Add(time.Hour), 42); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status='in_progress' WHERE user_id=$1 AND id=$2`, u, session); err != nil {
		t.Fatal(err)
	}
	if err := scoring.EnsureSessionWorkout(ctx, u, own, session, at, at.Add(30*time.Minute), 42); err == nil {
		t.Fatal("the times of a workout whose session is not completed must not move")
	}
	var end string
	if err := pool.QueryRow(ctx, `SELECT to_char(end_at AT TIME ZONE 'UTC', 'HH24:MI') FROM workouts WHERE user_id=$1 AND id=$2`, u, own).Scan(&end); err != nil || end != "18:00" {
		t.Fatalf("end %q %v", end, err)
	}
}
