package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// trainStore is testStore with the built-ins seeded and two users.
func trainStore(t *testing.T) (*Store, *pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	st, pool := testStore(t)
	ctx := context.Background()
	if err := db.Seed(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	a, b := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	return st, pool, a, b
}

var (
	squat = train.BuiltinExerciseID("back-squat")
	bench = train.BuiltinExerciseID("bench-press")
	t0    = time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)
)

func ptr[T any](v T) *T { return &v }

func lift(idx, reps int, kg float64, at time.Time) model.Set {
	return model.Set{ID: uuid.New(), SetIndex: idx, Reps: &reps, WeightKG: &kg, CompletedAt: &at, UpdatedAt: at}
}

func newSession(ex uuid.UUID, sets ...model.Set) model.Session {
	return model.Session{ID: uuid.New(), Title: "Test", StartedAt: t0, Status: model.SessionInProgress,
		Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: ex, Sets: sets}}}
}

func TestCustomExercises(t *testing.T) {
	st, _, a, b := trainStore(t)
	ctx := context.Background()
	ex, err := train.NewExercise(model.ExerciseInput{ID: uuid.New(), Name: "Back squat", Kind: model.KindBarbell,
		PrimaryMuscles: []string{"quads"}, LoadType: model.LoadWeight})
	if err != nil {
		t.Fatal(err)
	}
	ex.LoadFactor = 1
	got, created, err := st.SaveCustomExercise(ctx, a, ex)
	if err != nil || !created || got.Slug != "back-squat" || !got.IsCustom {
		t.Fatalf("create: %+v created=%v err=%v", got, created, err)
	}
	if _, created, err := st.SaveCustomExercise(ctx, a, ex); err != nil || created {
		t.Fatalf("the same id again updates: created=%v err=%v", created, err)
	}
	ex2 := ex
	ex2.ID = uuid.New()
	if got, _, err := st.SaveCustomExercise(ctx, a, ex2); err != nil || got.Slug != "back-squat-2" {
		t.Fatalf("a second exercise of the same name gets back-squat-2: %q %v", got.Slug, err)
	}
	if _, _, err := st.SaveCustomExercise(ctx, b, ex); !errors.Is(err, ErrTaken) {
		t.Fatalf("another user's id must be taken: %v", err)
	}
	builtin := ex
	builtin.ID = squat
	if _, _, err := st.SaveCustomExercise(ctx, a, builtin); !errors.Is(err, ErrTaken) {
		t.Fatalf("a built-in id must be taken: %v", err)
	}
	if _, err := st.UpdateCustomExercise(ctx, a, squat, func(e model.Exercise) (model.Exercise, error) { return e, nil }); !errors.Is(err, ErrForbidden) {
		t.Fatalf("built-ins are read-only: %v", err)
	}
	if _, err := st.UpdateCustomExercise(ctx, b, ex.ID, func(e model.Exercise) (model.Exercise, error) { return e, nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user's exercise is not found: %v", err)
	}
	list, err := st.Exercises(ctx, b, ExerciseFilter{Q: "squat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.IsCustom {
			t.Fatal("b must not see a's exercises")
		}
	}
	if len(list) < 5 {
		t.Fatalf("the built-in squats must be listed for everyone: %d", len(list))
	}
	if l, _ := st.Exercises(ctx, a, ExerciseFilter{Muscle: "calves", Kind: model.KindMachine}); len(l) == 0 {
		t.Fatal("muscle and kind filters")
	}
}

func TestSaveSessionMergeConflictReplace(t *testing.T) {
	st, _, a, b := trainStore(t)
	ctx := context.Background()
	s1, s2 := lift(0, 5, 100, t0), lift(1, 5, 100, t0.Add(3*time.Minute))
	body := newSession(squat, s1, s2)
	res, err := st.SaveSession(ctx, a, body)
	if err != nil || !res.Created || res.NewSets != 2 || len(res.Session.Exercises[0].Sets) != 2 {
		t.Fatalf("create: %+v %v", res, err)
	}
	// The same session id is another user's own session.
	if res, err := st.SaveSession(ctx, b, body); err != nil || !res.Created {
		t.Fatalf("ids are per user: %v", err)
	}
	// An older queued save that knew one set: nothing is deleted.
	old := body
	old.Exercises = []model.SessionExercise{body.Exercises[0]}
	old.Exercises[0].Sets = []model.Set{s1}
	if res, err := st.SaveSession(ctx, a, old); err != nil || res.Conflict || len(res.Session.Exercises[0].Sets) != 2 {
		t.Fatalf("server-only sets are kept: %+v %v", res, err)
	}
	// A stale copy of a set the server has newer: 409 with the server's copy.
	stale := s2
	stale.UpdatedAt = t0
	stale.Reps = ptr(1)
	old.Exercises[0].Sets = []model.Set{s1, stale}
	res, err = st.SaveSession(ctx, a, old)
	if err != nil || !res.Conflict || *res.Session.Exercises[0].Sets[1].Reps != 5 {
		t.Fatalf("conflict: %+v %v", res, err)
	}
	// Completing via PUT is refused; references must be visible.
	done := body
	done.Status = model.SessionCompleted
	var p train.Problem
	if _, err := st.SaveSession(ctx, a, done); !errors.As(err, &p) {
		t.Fatalf("PUT cannot complete: %v", err)
	}
	alien := newSession(uuid.New(), lift(0, 1, 1, t0))
	if _, err := st.SaveSession(ctx, a, alien); !errors.As(err, &p) {
		t.Fatalf("unknown exercise: %v", err)
	}
	// Completed: replaced whole.
	if err := st.CompleteSession(ctx, a, body.ID, Completion{EndedAt: t0.Add(time.Hour), MuscularTRIMP: 10, Strain: 2.5,
		VolumeKG: 1000, WorkoutID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	done.Exercises = []model.SessionExercise{body.Exercises[0]}
	done.Exercises[0].Sets = []model.Set{s1}
	res, err = st.SaveSession(ctx, a, done)
	if err != nil || len(res.Session.Exercises[0].Sets) != 1 || res.Session.MuscularTRIMP == nil {
		t.Fatalf("completed sessions are replaced whole and keep their load: %+v %v", res.Session, err)
	}
	reopen := done
	reopen.Status = model.SessionInProgress
	if _, err := st.SaveSession(ctx, a, reopen); !errors.As(err, &p) {
		t.Fatalf("a completed session cannot be reopened: %v", err)
	}
	// Discarded sessions cannot be completed.
	gone := newSession(bench, lift(0, 5, 60, t0))
	gone.Status = model.SessionDiscarded
	if _, err := st.SaveSession(ctx, a, gone); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSession(ctx, a, gone.ID, Completion{EndedAt: t0}); !errors.Is(err, ErrTaken) {
		t.Fatalf("completing a discarded session: %v", err)
	}
	if err := st.CompleteSession(ctx, a, uuid.New(), Completion{EndedAt: t0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completing an unknown session: %v", err)
	}
	sums, err := st.SessionSummaries(ctx, a, t0.Add(-time.Hour), t0.Add(time.Hour), "", false)
	if err != nil || len(sums) != 2 {
		t.Fatalf("summaries: %+v %v", sums, err)
	}
	for _, x := range sums {
		if x.Status == model.SessionCompleted && (x.SetCount != 1 || x.DurationMin == nil || *x.DurationMin != 60) {
			t.Fatalf("summary counts: %+v", x)
		}
	}
	sets, err := st.LoggedSets(ctx, a, []uuid.UUID{squat, bench}, t0.Add(24*time.Hour), uuid.Nil)
	if err != nil || len(sets) != 1 {
		t.Fatalf("logged sets of completed sessions only: %d %v", len(sets), err)
	}
	if _, err := st.DeleteSession(ctx, a, body.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Session(ctx, a, body.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted")
	}
}

func userPlan() model.Plan {
	w := 2
	day := func(week, idx int) model.PlanDay {
		return model.PlanDay{ID: uuid.New(), Week: &week, DayIndex: idx, Name: "D", Exercises: []model.PlanExercise{
			{ID: uuid.New(), ExerciseID: squat, Sets: 3, RepsMin: 5, RepsMax: 5, TargetKind: model.TargetKG, TargetValue: ptr(60.0), RestSec: 120}}}
	}
	return model.Plan{ID: uuid.New(), Name: "Mine", Goal: "strength", Weeks: &w, DaysPerWeek: 2,
		Days: []model.PlanDay{day(1, 0), day(1, 1), day(2, 0), day(2, 1)}}
}

func TestPlansAndEnrollment(t *testing.T) {
	st, _, a, b := trainStore(t)
	ctx := context.Background()
	p := userPlan()
	if err := train.CheckPlan(p); err != nil {
		t.Fatal(err)
	}
	if created, err := st.SavePlan(ctx, a, p); err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.SavePlan(ctx, b, p); !errors.Is(err, ErrTaken) {
		t.Fatalf("another user's plan id: %v", err)
	}
	bi := p
	bi.ID = train.BuiltinPlanID("full-body-3x")
	if _, err := st.SavePlan(ctx, a, bi); !errors.Is(err, ErrForbidden) {
		t.Fatalf("built-in plans are read-only: %v", err)
	}
	got, err := st.Plan(ctx, a, p.ID)
	if err != nil || len(got.Days) != 4 || got.Days[0].Exercises[0].Exercise == nil || got.IsBuiltin {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if _, err := st.Plan(ctx, b, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("b cannot see a's plan")
	}
	builtin, mine, err := st.PlanSummaries(ctx, a)
	if err != nil || len(builtin) != 4 || len(mine) != 1 || mine[0].ExerciseCount != 4 {
		t.Fatalf("summaries: %d %d %v", len(builtin), len(mine), err)
	}

	e, created, err := st.Enroll(ctx, a, p.ID)
	if err != nil || !created || e.CurrentWeek != 1 || e.CurrentDayIndex != 0 {
		t.Fatalf("enroll: %+v %v", e, err)
	}
	full, err := st.Plan(ctx, a, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Four completed days: w1d0, w1d1, w2d0, w2d1 → completed.
	for k := 0; k < 4; k++ {
		cur, _ := st.ActiveEnrollment(ctx, a)
		if cur == nil {
			t.Fatalf("enrolment ended early after %d days", k)
		}
		d, err := CurrentPlanDay(full, *cur)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AdvanceEnrollment(ctx, a, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if cur, _ := st.ActiveEnrollment(ctx, a); cur != nil {
		t.Fatalf("after the last day the enrolment is completed: %+v", cur)
	}
	// A new enrolment in a built-in pauses nothing (none active) and starts at day 0.
	e2, _, err := st.Enroll(ctx, a, train.BuiltinPlanID("full-body-3x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PauseEnrollment(ctx, a); err != nil {
		t.Fatal(err)
	}
	e3, created, err := st.Enroll(ctx, a, train.BuiltinPlanID("full-body-3x"))
	if err != nil || created || e3.ID != e2.ID {
		t.Fatalf("enrolling again resumes the paused enrolment: %+v created=%v %v", e3, created, err)
	}
	// Replacing the plan without its week 2 deletes those days.
	p.Days = p.Days[:2]
	if created, err := st.SavePlan(ctx, a, p); err != nil || created {
		t.Fatal(err)
	}
	if got, _ := st.Plan(ctx, a, p.ID); len(got.Days) != 2 {
		t.Fatalf("replaced days: %d", len(got.Days))
	}
	if err := st.DeletePlan(ctx, b, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("b cannot delete a's plan")
	}
	if err := st.DeletePlan(ctx, a, p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWorkoutsAndAccountDeletion(t *testing.T) {
	st, pool, a, _ := trainStore(t)
	ctx := context.Background()
	s := newSession(squat, lift(0, 5, 100, t0))
	if _, err := st.SaveSession(ctx, a, s); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSession(ctx, a, s.ID, Completion{EndedAt: t0.Add(time.Hour), WorkoutID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	wid := train.SessionWorkoutID(a, s.ID)
	if err := st.EnsureSessionWorkout(ctx, a, wid, s.ID, t0, t0.Add(time.Hour), 42); err != nil {
		t.Fatal(err)
	}
	// New times replace the workout.
	if err := st.EnsureSessionWorkout(ctx, a, wid, s.ID, t0, t0.Add(50*time.Minute), 40); err != nil {
		t.Fatal(err)
	}
	w, err := st.Workout(ctx, a, wid)
	if err != nil || w.Source != model.SessionSource || *w.MuscularLoad != 40 || !w.End.Equal(t0.Add(50*time.Minute)) {
		t.Fatalf("session workout: %+v %v", w, err)
	}
	// Kept while chosen, gone once its session is deleted.
	if err := st.ReleaseSessionWorkouts(ctx, a, []uuid.UUID{s.ID}, map[uuid.UUID]uuid.UUID{s.ID: wid}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Workout(ctx, a, wid); err != nil {
		t.Fatal("the chosen workout stays")
	}
	if _, err := st.DeleteSession(ctx, a, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseSessionWorkouts(ctx, a, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Workout(ctx, a, wid); !errors.Is(err, ErrNotFound) {
		t.Fatal("a session workout without its session goes")
	}

	// DeleteUser erases the Train rows too.
	s2 := newSession(squat, lift(0, 5, 100, t0))
	if _, err := st.SaveSession(ctx, a, s2); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplacePRs(ctx, a, squat, []model.PR{{ExerciseID: squat, Kind: model.PRMaxWeight, Value: 100,
		SetID: s2.Exercises[0].Sets[0].ID, SessionID: s2.ID, AchievedAt: t0}}); err != nil {
		t.Fatal(err)
	}
	p := userPlan()
	if _, err := st.SavePlan(ctx, a, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Enroll(ctx, a, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sessions WHERE user_id=$1) + (SELECT count(*) FROM session_sets WHERE user_id=$1)
		+ (SELECT count(*) FROM personal_records WHERE user_id=$1) + (SELECT count(*) FROM plans WHERE user_id=$1)
		+ (SELECT count(*) FROM plan_days WHERE user_id=$1) + (SELECT count(*) FROM plan_enrollments WHERE user_id=$1)
		+ (SELECT count(*) FROM exercises WHERE user_id=$1)`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("train rows left after deletion: %d %v", n, err)
	}
}
