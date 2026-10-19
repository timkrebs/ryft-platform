package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// library is the real built-in exercise library, by slug, so the calibration
// below breaks when a seed's muscles or load factor change.
func library(t *testing.T) map[string]model.Exercise {
	t.Helper()
	b, err := db.LoadBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.Exercise{}
	for _, e := range b.Exercises() {
		out[e.Slug] = e
	}
	return out
}

var start = time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)

type sessionBuilder struct {
	s     model.Session
	exs   map[uuid.UUID]model.Exercise
	clock time.Time
}

func newBuilder() *sessionBuilder {
	return &sessionBuilder{s: model.Session{ID: uuid.New(), StartedAt: start, Status: model.SessionCompleted},
		exs: map[uuid.UUID]model.Exercise{}, clock: start}
}

// add logs n sets of reps at kg (nil kg = bodyweight only) of ex.
func (b *sessionBuilder) add(ex model.Exercise, n, reps int, kg *float64) *sessionBuilder {
	b.exs[ex.ID] = ex
	se := model.SessionExercise{ID: uuid.New(), ExerciseID: ex.ID, Position: len(b.s.Exercises)}
	for k := 0; k < n; k++ {
		b.clock = b.clock.Add(2 * time.Minute)
		done, r := b.clock, reps
		se.Sets = append(se.Sets, model.Set{ID: uuid.New(), SetIndex: k, Reps: &r, WeightKG: kg, CompletedAt: &done, UpdatedAt: done})
	}
	b.s.Exercises = append(b.s.Exercises, se)
	return b
}

func (b *sessionBuilder) hold(ex model.Exercise, n, sec int) *sessionBuilder {
	b.exs[ex.ID] = ex
	se := model.SessionExercise{ID: uuid.New(), ExerciseID: ex.ID, Position: len(b.s.Exercises)}
	for k := 0; k < n; k++ {
		b.clock = b.clock.Add(time.Minute)
		done, d := b.clock, sec
		se.Sets = append(se.Sets, model.Set{ID: uuid.New(), SetIndex: k, DurationSec: &d, CompletedAt: &done, UpdatedAt: done})
	}
	b.s.Exercises = append(b.s.Exercises, se)
	return b
}

func kg(v float64) *float64 { return &v }

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.2f, want %.1f ± %.1f", what, got, want, tol)
	}
}

// The reference strength session: 60 min, 5 × 5 back squat, bench press and
// barbell row at 80 % of e1RM, 80 kg athlete → strain ≈ 11 from the muscular
// load alone (the spec's anchor; its "TRIMP ≈ 85" would be strain 13.7 on the
// existing compression, so the strain value is what is pinned — D59).
func TestStrengthCalibrationReference(t *testing.T) {
	lib := library(t)
	sq, bp, row := lib["back-squat"], lib["bench-press"], lib["barbell-row"]
	e1rm := map[uuid.UUID]float64{sq.ID: 140, bp.ID: 100, row.ID: 90}
	b := newBuilder().add(sq, 5, 5, kg(112)).add(bp, 5, 5, kg(80)).add(row, 5, 5, kg(72))
	got := LoadOfSession(b.s, b.exs, e1rm, 80)
	near(t, "reference strain", got.Strain, 11.0, 0.3)
	near(t, "reference muscular TRIMP", got.TRIMP, 59.4, 1.5)
	if want := 25*112.0 + 25*80 + 25*72; got.VolumeKG != want {
		t.Errorf("volume %.1f, want %.1f", got.VolumeKG, want)
	}
	if got.ByMuscle["quads"] != 25*112 || got.ByMuscle["hamstrings"] != 25*112/2.0 {
		t.Errorf("volume by muscle: %v", got.ByMuscle)
	}
}

// Twelve singles at 90 %: six snatches and six clean & jerks → ≈ 8.
func TestStrengthCalibrationOlympicSingles(t *testing.T) {
	lib := library(t)
	sn, cj := lib["snatch"], lib["clean-and-jerk"]
	e1rm := map[uuid.UUID]float64{sn.ID: 100, cj.ID: 125}
	b := newBuilder().add(sn, 6, 1, kg(90)).add(cj, 6, 1, kg(112.5))
	near(t, "olympic strain", LoadOfSession(b.s, b.exs, e1rm, 80).Strain, 8.0, 0.3)
}

// A 20-minute bodyweight circuit, four rounds of 10 push-ups, 15 bodyweight
// squats and a 45 s plank, scored against its own sets (no history) → ≈ 5.
func TestStrengthCalibrationBodyweightCircuit(t *testing.T) {
	lib := library(t)
	b := newBuilder()
	for r := 0; r < 4; r++ {
		b.add(lib["push-up"], 1, 10, nil).add(lib["air-squat"], 1, 15, nil).hold(lib["plank"], 1, 45)
	}
	var logged []store.LoggedSet
	for _, se := range b.s.Exercises {
		for _, st := range se.Sets {
			logged = append(logged, store.LoggedSet{SessionID: b.s.ID, ExerciseID: se.ExerciseID, Set: st})
		}
	}
	e1rm := BestE1RMs(logged, b.exs, 80)
	near(t, "circuit strain", LoadOfSession(b.s, b.exs, e1rm, 80).Strain, 5.0, 0.4)
}

func TestSetLoadRules(t *testing.T) {
	lib := library(t)
	sq := lib["back-squat"]
	done := start
	five := 5
	work := model.Set{Reps: &five, WeightKG: kg(100), CompletedAt: &done}
	full := SetLoad(work, 125, sq, 80)
	near(t, "set load", full, 5*0.64*MuscleFactor(sq), 1e-9)
	warm := work
	warm.IsWarmup = true
	near(t, "warm-up counts a quarter", SetLoad(warm, 125, sq, 80), full/4, 1e-9)
	planned := work
	planned.CompletedAt = nil
	if SetLoad(planned, 125, sq, 80) != 0 {
		t.Error("only completed sets count")
	}
	if SetLoad(work, 50, sq, 80) != 5*MuscleFactor(sq) {
		t.Error("intensity is capped at 1 (an e1RM below the set's weight)")
	}
	bss := lib["bulgarian-split-squat"]
	if !bss.IsUnilateral || SetLoad(work, 125, bss, 80) != 2*5*0.64*MuscleFactor(bss) {
		t.Error("unilateral sets count both sides")
	}
	if E1RM(100, 20) != E1RM(100, 12) || math.Abs(E1RM(100, 5)-116.67) > 0.01 {
		t.Error("Epley with reps capped at 12")
	}
	pu := lib["pull-up"]
	if w, _ := EffectiveWeight(pu, model.Set{WeightKG: kg(20)}, 80); w != 100 {
		t.Errorf("a weighted pull-up moves body weight × factor + the belt: %v", w)
	}
	if MuscleFactor(lib["snatch"]) <= MuscleFactor(sq) {
		t.Error("the Olympic lifts load more than a squat")
	}
}

func TestMatchWorkout(t *testing.T) {
	end := start.Add(60 * time.Minute)
	s := model.Session{ID: uuid.New(), StartedAt: start, EndedAt: &end}
	w := func(activity string, from, to time.Duration, source string) model.Workout {
		return model.Workout{ID: uuid.New(), Activity: activity, Start: start.Add(from), End: start.Add(to), Source: source}
	}
	half := w("strength", 30*time.Minute, 120*time.Minute, "Apple Watch")      // exactly half
	more := w("functional_strength", -10*time.Minute, 50*time.Minute, "Watch") // 50 of 60
	run := w("running", 0, 60*time.Minute, "Watch")
	mine := w("strength", 0, 60*time.Minute, model.SessionSource)
	ws := []model.Workout{half, run, mine, more}
	if got := MatchWorkout(s, ws, nil); got == nil || got.ID != more.ID {
		t.Fatalf("the largest strength overlap wins: %+v", got)
	}
	if got := MatchWorkout(s, ws, map[uuid.UUID]bool{more.ID: true}); got == nil || got.ID != half.ID {
		t.Fatalf("a taken workout is skipped; half the session is enough: %+v", got)
	}
	short := w("strength", 40*time.Minute, 60*time.Minute, "Watch")
	if MatchWorkout(s, []model.Workout{short, run, mine}, nil) != nil {
		t.Fatal("under half, other activities and the platform's own workouts never match")
	}
}

func logged(ex uuid.UUID, at time.Time, reps int, w float64) store.LoggedSet {
	done := at
	return store.LoggedSet{SessionID: uuid.New(), ExerciseID: ex, Set: model.Set{ID: uuid.New(), Reps: &reps, WeightKG: &w, CompletedAt: &done}}
}

func TestPersonalRecords(t *testing.T) {
	lib := library(t)
	sq := lib["back-squat"]
	a := logged(sq.ID, start, 5, 100)
	b := logged(sq.ID, start.Add(time.Hour), 3, 110) // e1RM 110 × 1.1 = 121 beats 100 × 5 (116.7)
	c := logged(sq.ID, start.Add(2*time.Hour), 5, 100)
	warm := logged(sq.ID, start.Add(3*time.Hour), 1, 200)
	warm.IsWarmup = true
	prs := PersonalRecords([]store.LoggedSet{a, b, c, warm}, sq)
	got := map[string]model.PR{}
	for _, p := range prs {
		got[p.Kind] = p
	}
	if got[model.PRE1RM].Value != 121 || got[model.PRE1RM].SetID != b.ID {
		t.Errorf("e1rm: %+v", got[model.PRE1RM])
	}
	if got[model.PRMaxWeight].Value != 110 {
		t.Errorf("max weight ignores warm-ups: %+v", got[model.PRMaxWeight])
	}
	if got[model.PRMaxReps].SetID != a.ID || got[model.PRMaxVolume].SetID != a.ID {
		t.Error("on a tie the first set keeps the record")
	}
	push := lib["push-up"]
	bw := PersonalRecords([]store.LoggedSet{logged(push.ID, start, 30, 0)}, push)
	if len(bw) != 1 || bw[0].Kind != model.PRMaxReps || bw[0].Value != 30 {
		t.Errorf("bodyweight exercises: max reps only without added load: %+v", bw)
	}
	if n := len(PersonalRecords([]store.LoggedSet{logged(lib["plank"].ID, start, 1, 0)}, lib["plank"])); n != 0 {
		t.Error("time exercises have no records")
	}
}

func attempt(n, reps int, w float64) store.Attempt {
	var a store.Attempt
	for k := 0; k < n; k++ {
		r, kgv, done := reps, w, start
		a.Sets = append(a.Sets, model.Set{Reps: &r, WeightKG: &kgv, CompletedAt: &done})
	}
	return a
}

func TestNextTargetKG(t *testing.T) {
	inc := 2.5
	linear := &model.Progression{Type: model.ProgressionLinear, IncrementKG: &inc}
	pe := model.PlanExercise{Sets: 5, RepsMin: 5, RepsMax: 5, TargetKind: model.TargetKG, TargetValue: kg(100)}
	check := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	check("no history", NextTargetKG(pe, linear, nil, 0), 100)
	check("success adds the increment", NextTargetKG(pe, linear, []store.Attempt{attempt(5, 5, 100)}, 0), 102.5)
	check("a miss repeats", NextTargetKG(pe, linear, []store.Attempt{attempt(5, 5, 100), attempt(4, 5, 102.5)}, 0), 102.5)
	misses := []store.Attempt{attempt(5, 5, 100), attempt(5, 4, 102.5), attempt(5, 4, 102.5), attempt(5, 4, 102.5)}
	check("three misses deload 10 %", NextTargetKG(pe, linear, misses, 0), 92.5)
	check("then it climbs again", NextTargetKG(pe, linear, append(misses, attempt(5, 5, 92.5)), 0), 95)
	check("too light does not count", NextTargetKG(pe, linear, []store.Attempt{attempt(5, 5, 90)}, 0), 100)
	check("no progression keeps kg", NextTargetKG(pe, &model.Progression{Type: model.ProgressionNone}, []store.Attempt{attempt(5, 5, 100)}, 0), 100)
	pct := model.PlanExercise{Sets: 3, RepsMin: 3, RepsMax: 3, TargetKind: model.TargetPct1RM, TargetValue: kg(80)}
	check("pct of e1RM, rounded to 2.5", NextTargetKG(pct, nil, nil, 143), 115)
	if NextTargetKG(pct, nil, nil, 0) != nil {
		t.Error("pct without an e1RM has no target")
	}
	rpe := model.PlanExercise{Sets: 3, RepsMin: 8, RepsMax: 10, TargetKind: model.TargetRPE, TargetValue: kg(8)}
	if NextTargetKG(rpe, linear, nil, 140) != nil {
		t.Error("rpe targets have no kg")
	}
}
