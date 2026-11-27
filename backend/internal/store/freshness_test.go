package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestMuscleFreshnessHistory(t *testing.T) {
	st, pool, user, other := trainStore(t)
	ctx := context.Background()
	until := t0.Add(2 * time.Hour)
	since := until.Add(-14 * 24 * time.Hour)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	addSession := func(owner, session uuid.UUID, status string) {
		exec(`INSERT INTO sessions (user_id, id, started_at, status) VALUES ($1,$2,$3,$4)`, owner, session, t0, status)
	}
	addExercise := func(owner, session, entry, exercise uuid.UUID) {
		exec(`INSERT INTO session_exercises (user_id, session_id, id, exercise_id, position) VALUES ($1,$2,$3,$4,0)`,
			owner, session, entry, exercise)
	}
	addSet := func(owner, entry uuid.UUID, at *time.Time, warmup bool) {
		exec(`INSERT INTO session_sets (user_id, session_exercise_id, id, set_index, reps, weight_kg, completed_at, is_warmup, updated_at)
			VALUES ($1,$2,$3,0,5,50,$4,$5,now())`, owner, entry, uuid.New(), at, warmup)
	}
	history := func(since, until time.Time) MuscleHistory {
		t.Helper()
		h, err := st.MuscleHistory(ctx, user, since, until)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	ownExercise, foreignExercise := uuid.New(), uuid.New()
	exec(`INSERT INTO exercises (id, user_id, slug, name_en, name_de, kind, load_type, primary_muscles, secondary_muscles, is_unilateral)
		VALUES ($1,$2,'own','Own','Own','dumbbell','weight',ARRAY['calves'],ARRAY['forearms'],true),
		       ($3,$4,'foreign','Foreign','Foreign','dumbbell','weight',ARRAY['biceps'],ARRAY['abs'],false)`,
		ownExercise, user, foreignExercise, other)

	// The same session and entry IDs can exist under two users.
	session, benchEntry := uuid.New(), uuid.New()
	addSession(other, session, model.SessionCompleted)
	addExercise(other, session, benchEntry, squat)
	addSet(other, benchEntry, ptr(until.Add(-time.Minute)), false)
	addSession(user, session, model.SessionInProgress)
	addExercise(user, session, benchEntry, bench)
	builtInAt := until.Add(-time.Hour)
	addSet(user, benchEntry, &builtInAt, false)
	addSet(user, benchEntry, &since, false)
	addSet(user, benchEntry, &until, false)
	addSet(user, benchEntry, ptr(until.Add(time.Second)), false)
	addSet(user, benchEntry, ptr(until.Add(-time.Second)), true)
	addSet(user, benchEntry, nil, false)

	ownEntry, foreignEntry, oldEntry := uuid.New(), uuid.New(), uuid.New()
	addExercise(user, session, ownEntry, ownExercise)
	customAt := until.Add(-30 * time.Minute)
	addSet(user, ownEntry, &customAt, false)
	// The schema permits this reference, but the exercise is invisible to user.
	addExercise(user, session, foreignEntry, foreignExercise)
	addSet(user, foreignEntry, &customAt, false)
	addExercise(user, session, oldEntry, squat)
	oldAt := since.Add(-time.Hour)
	addSet(user, oldEntry, &oldAt, false)

	for _, status := range []string{model.SessionDiscarded, model.SessionInProgress} {
		id, entry := uuid.New(), uuid.New()
		addSession(user, id, status)
		addExercise(user, id, entry, bench)
		addSet(user, entry, ptr(until.Add(-time.Minute)), false)
	}
	if h := history(since, until); h.HasCompletedSession || len(h.Sets) != 0 || len(h.Last) != 0 {
		t.Fatalf("history before completion = %+v; want nothing", h)
	}
	if err := st.CompleteSession(ctx, user, session, Completion{EndedAt: until}); err != nil {
		t.Fatal(err)
	}

	h := history(since, until)
	if !h.HasCompletedSession {
		t.Fatal("HasCompletedSession = false after completing a session")
	}
	sets, last := h.Sets, h.Last
	wantSets := []MuscleSet{
		{CompletedAt: builtInAt, Kind: model.KindBarbell, Primary: []string{"chest", "triceps", "front_delts"}},
		{CompletedAt: customAt, Kind: model.KindDumbbell, Primary: []string{"calves"}, Secondary: []string{"forearms"}},
	}
	if len(sets) != len(wantSets) {
		t.Fatalf("got working sets %v, want %v", sets, wantSets)
	}
	for i, want := range wantSets {
		got := sets[i]
		if !got.CompletedAt.Equal(want.CompletedAt) || got.Kind != want.Kind || !slices.Equal(got.Primary, want.Primary) || !slices.Equal(got.Secondary, want.Secondary) {
			t.Errorf("set %d = %+v, want %+v", i, got, want)
		}
	}

	wantLast := map[string]time.Time{
		"chest": builtInAt, "triceps": builtInAt, "front_delts": builtInAt,
		"calves": customAt, "forearms": customAt,
		"quads": oldAt, "glutes": oldAt, "hamstrings": oldAt, "adductors": oldAt, "lower_back": oldAt,
	}
	if len(last) != len(wantLast) {
		t.Fatalf("training history = %v, want %v", last, wantLast)
	}
	for muscle, want := range wantLast {
		if got := last[muscle]; !got.Equal(want) {
			t.Errorf("last trained for %s = %v, want %v", muscle, got, want)
		}
	}

	// The timestamps at the original boundaries now lie inside the open window.
	h = history(since.Add(-time.Nanosecond), until.Add(time.Nanosecond))
	if len(h.Sets) != 4 {
		t.Fatalf("working sets with fractional boundaries = %v; want four sets", h.Sets)
	}
	if !h.Last["chest"].Equal(until) {
		t.Fatalf("latest chest set with fractional boundary = %v; want %v", h.Last["chest"], until)
	}
}
