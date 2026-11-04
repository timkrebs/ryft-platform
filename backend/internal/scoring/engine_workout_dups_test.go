package scoring

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

const (
	dupWatch  = "Tim's Apple Watch"
	dupGarmin = "Garmin Connect"
)

// hrEvery writes source's heart rate at bpm every step over [from, to].
func hrEvery(source string, from, to time.Time, step time.Duration, bpm float64) []model.Sample {
	var out []model.Sample
	for t := from; !t.After(to); t = t.Add(step) {
		out = append(out, model.Sample{Type: model.SampleHeartRate, Start: t, End: t, Value: bpm, Unit: "count/min", Source: source})
	}
	return out
}

func visibleOn(t *testing.T, st *store.Store, user uuid.UUID, dayStart time.Time) []model.Workout {
	t.Helper()
	ws, err := st.VisibleWorkoutsBetween(context.Background(), user, dayStart, dayStart.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func workoutsDetail(t *testing.T, d model.DailyScore) int {
	t.Helper()
	switch n := d.Details["workouts"].(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("details.workouts = %#v", d.Details["workouts"])
		return 0
	}
}

// A Watch and Garmin Connect both record one evening run (D147, U3). Scoring
// hides the copy without its own heart rate, then re-decides when Garmin's
// heart rate arrives (both have it, Garmin's is longer), and a deleted primary
// shows its duplicate at once and is healed by the next pass. Day strain and
// the primary's strain stay what the run alone gives: they come from the
// de-duplicated heart rate of the time window, whichever row carries them.
func TestEngineHidesCrossSourceDuplicates(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	day := "2026-10-20"
	dayStart := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	run := dayStart.Add(18 * time.Hour)

	watch := model.Workout{ID: uuid.New(), Activity: "running", Start: run, End: run.Add(45 * time.Minute), Source: dupWatch}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{watch}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, user, hrEvery(dupWatch, run, watch.End, time.Minute, 150)); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	alone := scoresByDay(t, st, user, day, day)[day]
	watchAlone, _ := st.Workout(ctx, user, watch.ID)
	if alone.Strain == nil || watchAlone.Strain == nil || workoutsDetail(t, alone) != 1 {
		t.Fatalf("the run alone: strain %v, workout %v, details.workouts %v", alone.Strain, watchAlone.Strain, alone.Details["workouts"])
	}

	// Garmin's copy syncs, a minute longer at each end, without heart rate.
	garmin := model.Workout{ID: uuid.New(), Activity: "running", Start: run.Add(-time.Minute), End: run.Add(46 * time.Minute), Source: dupGarmin}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{garmin}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	vis := visibleOn(t, st, user, dayStart)
	if len(vis) != 1 || vis[0].ID != watch.ID || !slices.Equal(vis[0].AlsoRecordedBy, []string{dupGarmin}) {
		t.Fatalf("the Watch, which has heart rate, is the primary: %+v", vis)
	}
	both := scoresByDay(t, st, user, day, day)[day]
	watchNow, _ := st.Workout(ctx, user, watch.ID)
	if *both.Strain != *alone.Strain || *watchNow.Strain != *watchAlone.Strain || workoutsDetail(t, both) != 1 {
		t.Fatalf("strain %v → %v, the Watch's %v → %v, details.workouts %v (want unchanged, unchanged, 1)",
			*alone.Strain, *both.Strain, *watchAlone.Strain, *watchNow.Strain, both.Details["workouts"])
	}
	if g, err := st.Workout(ctx, user, garmin.ID); err != nil || g.Strain == nil {
		t.Fatalf("the hidden copy keeps its own load and still answers: %+v %v", g, err)
	}

	// Garmin's heart rate arrives (two readings): both have their own now, so
	// the longer, Garmin's, becomes the primary. The Watch still wins the hour
	// (46 samples to 2), so the day's strain does not move.
	if _, err := st.UpsertSamples(ctx, user, hrEvery(dupGarmin, run.Add(10*time.Minute), run.Add(20*time.Minute), 10*time.Minute, 150)); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	vis = visibleOn(t, st, user, dayStart)
	if len(vis) != 1 || vis[0].ID != garmin.ID || !slices.Equal(vis[0].AlsoRecordedBy, []string{dupWatch}) {
		t.Fatalf("late heart rate re-decides: %+v", vis)
	}
	if d := scoresByDay(t, st, user, day, day)[day]; *d.Strain != *alone.Strain || workoutsDetail(t, d) != 1 {
		t.Fatalf("day strain %v (want %v), details.workouts %v", *d.Strain, *alone.Strain, d.Details["workouts"])
	}

	// The primary is deleted: the Watch's copy shows at once, before any
	// scoring, and the next pass clears its duplicate_of.
	if _, err := st.ApplySync(ctx, user, store.Apply{Loc: time.UTC,
		Deleted: []model.DeletedRecord{{Kind: model.DeletedWorkout, ID: garmin.ID}}}); err != nil {
		t.Fatal(err)
	}
	if vis = visibleOn(t, st, user, dayStart); len(vis) != 1 || vis[0].ID != watch.ID {
		t.Fatalf("a deleted primary shows its duplicate: %+v", vis)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	vis = visibleOn(t, st, user, dayStart)
	if len(vis) != 1 || vis[0].ID != watch.ID || len(vis[0].AlsoRecordedBy) != 0 {
		t.Fatalf("after the re-score: %+v", vis)
	}
	if d := scoresByDay(t, st, user, day, day)[day]; *d.Strain != *alone.Strain || workoutsDetail(t, d) != 1 {
		t.Fatalf("day strain %v (want %v), details.workouts %v", *d.Strain, *alone.Strain, d.Details["workouts"])
	}
}

// A strength session recorded by the Watch (with heart rate) and by Garmin
// (without, and longer at both ends). Both cover the whole session, so the
// overlap ties and MatchWorkout would take the earlier start, Garmin's, which
// is the hidden copy. The candidates are the visible workouts, so the load
// lands on the Watch's, the one the user sees.
func TestEngineAttachesSessionsToTheVisibleWorkout(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	lib := library(t)
	day := "2026-10-18"
	dayStart := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	s := newBuilder().add(lib["back-squat"], 5, 5, kg(112)).s
	s.Status = model.SessionInProgress
	end := start.Add(60 * time.Minute)
	s.EndedAt = &end
	if _, err := st.SaveSession(ctx, user, s); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSession(ctx, user, s.ID, store.Completion{EndedAt: end}); err != nil {
		t.Fatal(err)
	}
	watch := model.Workout{ID: uuid.New(), Activity: "strength", Start: start.Add(-2 * time.Minute), End: end.Add(time.Minute), Source: dupWatch}
	garmin := model.Workout{ID: uuid.New(), Activity: "strength", Start: start.Add(-5 * time.Minute), End: end.Add(5 * time.Minute), Source: dupGarmin}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{watch, garmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, user, hrEvery(dupWatch, watch.Start, watch.End, time.Minute, 110)); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Session(ctx, user, s.ID)
	if got.WorkoutID == nil || *got.WorkoutID != watch.ID {
		t.Fatalf("the session attaches to the visible Watch workout, got %v", got.WorkoutID)
	}
	if g, _ := st.Workout(ctx, user, garmin.ID); g.SessionID != nil || g.MuscularLoad != nil {
		t.Fatalf("the hidden copy carries no session: %+v", g)
	}
	vis := visibleOn(t, st, user, dayStart)
	if len(vis) != 1 || vis[0].ID != watch.ID || vis[0].SessionID == nil || vis[0].MuscularLoad == nil {
		t.Fatalf("visible: %+v", vis)
	}
}

// U3b through the engine: the Watch logged the session's hour as "strength",
// Garmin as "other", longer and with heart rate of its own. The specific
// activity ranks first, so Garmin's is the hidden copy and the session stays
// on the Watch's workout, the one the user sees.
func TestEngineKeepsTheSpecificActivityOverOther(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	lib := library(t)
	day := "2026-10-18"
	dayStart := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	s := newBuilder().add(lib["back-squat"], 5, 5, kg(112)).s
	s.Status = model.SessionInProgress
	end := start.Add(60 * time.Minute)
	s.EndedAt = &end
	if _, err := st.SaveSession(ctx, user, s); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSession(ctx, user, s.ID, store.Completion{EndedAt: end}); err != nil {
		t.Fatal(err)
	}
	watch := model.Workout{ID: uuid.New(), Activity: "strength", Start: start.Add(-2 * time.Minute), End: end.Add(time.Minute), Source: dupWatch}
	garmin := model.Workout{ID: uuid.New(), Activity: "other", Start: start.Add(-5 * time.Minute), End: end.Add(5 * time.Minute), Source: dupGarmin}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{watch, garmin}); err != nil {
		t.Fatal(err)
	}
	hr := append(hrEvery(dupWatch, watch.Start, watch.End, time.Minute, 110), hrEvery(dupGarmin, garmin.Start, garmin.End, time.Minute, 110)...)
	if _, err := st.UpsertSamples(ctx, user, hr); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	vis := visibleOn(t, st, user, dayStart)
	if len(vis) != 1 || vis[0].ID != watch.ID || vis[0].SessionID == nil || *vis[0].SessionID != s.ID ||
		!slices.Equal(vis[0].AlsoRecordedBy, []string{dupGarmin}) {
		t.Fatalf("the strength workout stays visible with the session: %+v", vis)
	}
	if d := scoresByDay(t, st, user, day, day)[day]; workoutsDetail(t, d) != 1 {
		t.Fatalf("details.workouts %v", d.Details["workouts"])
	}
}

// A recording that starts before the day's widened window and runs into it:
// Garmin's 30-h "hiking" from 06:00 the day before to 12:00, and a phone app's
// hour of "other" at 10:00. The day's pass reads by end too, so the phone's
// hour is hidden behind the hike; it would be missed with a read by start.
func TestEngineSeesALongWorkoutStartedEarlier(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	day := "2026-10-20"
	dayStart := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	hike := model.Workout{ID: uuid.New(), Activity: "hiking", Start: dayStart.Add(-18 * time.Hour), End: dayStart.Add(12 * time.Hour), Source: dupGarmin}
	app := model.Workout{ID: uuid.New(), Activity: "other", Start: dayStart.Add(10 * time.Hour), End: dayStart.Add(11 * time.Hour), Source: "Trail App"}
	if hike.Start.After(dayStart.Add(-dupMargin)) {
		t.Fatal("precondition: the hike starts before the widened window")
	}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{hike, app}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	if vis := visibleOn(t, st, user, dayStart); len(vis) != 0 {
		t.Fatalf("the app's hour is hidden behind the hike (which belongs to the day before): %+v", vis)
	}
	if d := scoresByDay(t, st, user, day, day)[day]; workoutsDetail(t, d) != 0 {
		t.Fatalf("details.workouts %v", d.Details["workouts"])
	}
}

// Above maxNearWorkouts the day's decision is skipped: duplicate_of is
// cleared and every workout is shown, as before the rule.
func TestEngineSkipsTheDecisionAboveTheCap(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	day := "2026-10-20"
	dayStart := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	run := dayStart.Add(18 * time.Hour)
	pair := []model.Workout{
		{ID: uuid.New(), Activity: "running", Start: run, End: run.Add(45 * time.Minute), Source: dupWatch},
		{ID: uuid.New(), Activity: "running", Start: run.Add(-time.Minute), End: run.Add(46 * time.Minute), Source: dupGarmin},
	}
	if _, err := st.UpsertWorkouts(ctx, user, pair); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	if vis := visibleOn(t, st, user, dayStart); len(vis) != 1 {
		t.Fatalf("under the cap the pair is one: %+v", vis)
	}
	var filler []model.Workout
	for k := range maxNearWorkouts - 1 { // with the pair: cap + 1
		s := dayStart.Add(time.Duration(k) * 3 * time.Minute)
		filler = append(filler, model.Workout{ID: uuid.New(), Activity: "walking", Start: s, End: s.Add(time.Minute), Source: "Filler"})
	}
	if _, err := st.UpsertWorkouts(ctx, user, filler); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	if vis := visibleOn(t, st, user, dayStart); len(vis) != maxNearWorkouts+1 {
		t.Fatalf("above the cap everything is shown: %d", len(vis))
	}
}
