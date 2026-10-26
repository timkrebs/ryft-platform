package scoring

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Calibration of model 2026.10.2 (docs/SCORING.md → Strength, D134, D135,
// D138): assisted bodyweight sets, the 90-day e1RM reference, and a session's
// history that ends at its last set. Every number below is worked out in the
// comment next to it from the real seed library:
//
//	pull-up:    bodyweight_factor 1.0, muscleFactor (lats 0.7 + ½ (biceps 0.3 +
//	            upper_back 0.6 + rear_delts 0.25 + forearms 0.2)) × 0.32 = 0.44
//	back squat: muscleFactor (quads 1 + glutes 1 + ½ (hamstrings 0.8 +
//	            adductors 0.4 + lower_back 0.6)) × 1.0 = 2.9

// assisted logs n sets of reps with assist kg of support.
func (b *sessionBuilder) assisted(ex model.Exercise, n, reps int, assist float64) *sessionBuilder {
	b.exs[ex.ID] = ex
	se := model.SessionExercise{ID: uuid.New(), ExerciseID: ex.ID, Position: len(b.s.Exercises)}
	for k := 0; k < n; k++ {
		b.clock = b.clock.Add(2 * time.Minute)
		done, r, a := b.clock, reps, assist
		se.Sets = append(se.Sets, model.Set{ID: uuid.New(), SetIndex: k, Reps: &r, AssistKG: &a, CompletedAt: &done, UpdatedAt: done})
	}
	b.s.Exercises = append(b.s.Exercises, se)
	return b
}

// own is the session's sets as store.LoggedSets returns them.
func (b *sessionBuilder) own() []store.LoggedSet {
	var out []store.LoggedSet
	for _, se := range b.s.Exercises {
		for _, st := range se.Sets {
			out = append(out, store.LoggedSet{SessionID: b.s.ID, ExerciseID: se.ExerciseID, SessionStart: b.s.StartedAt, Set: st})
		}
	}
	return out
}

// withoutAssist is the session as model 2026.10.1 scored it: assist_kg ignored (D127).
func withoutAssist(s model.Session) model.Session {
	out := s
	out.Exercises = slices.Clone(s.Exercises)
	for k := range out.Exercises {
		out.Exercises[k].Sets = slices.Clone(out.Exercises[k].Sets)
		for j := range out.Exercises[k].Sets {
			out.Exercises[k].Sets[j].AssistKG = nil
		}
	}
	return out
}

func unassisted(sets []store.LoggedSet) []store.LoggedSet {
	out := slices.Clone(sets)
	for k := range out {
		out[k].AssistKG = nil
	}
	return out
}

// A beginner on the assisted pull-up machine: 80 kg, 3 × 8 with 30 kg of
// support, nothing else in the window.
//
//	weight    80 × 1.0 − 30 = 50 kg (2026.10.1: 80, the support ignored)
//	e1RM      50 × (1 + 8/30) = 63.3 (its own sets; 2026.10.1: 101.3)
//	intensity (50 / 63.3)² = 0.623, the same as 8 reps unassisted against
//	          their own e1RM: intensity is relative to what the athlete can do
//	TRIMP     3 × 8 × 0.623 × 0.44 × 0.6 = 3.9, strain 1.0 (unchanged)
//	volume    24 × 50 = 1200 kg (2026.10.1: 1920)
func TestAssistedSetCalibration(t *testing.T) {
	lib := library(t)
	pu := lib["pull-up"]
	near(t, "pull-up muscle factor", MuscleFactor(pu), 0.44, 1e-9)
	b := newBuilder().assisted(pu, 3, 8, 30)
	e1rm := BestE1RMs(b.own(), b.exs, 80)
	near(t, "e1RM of the assisted sets", e1rm[pu.ID], 63.33, 0.01)
	got := LoadOfSession(b.s, b.exs, e1rm, 80)
	if got.TRIMP != 3.9 || got.Strain != 1.0 || got.VolumeKG != 1200 {
		t.Errorf("assisted 3 × 8: TRIMP %v strain %v volume %v, want 3.9 1.0 1200", got.TRIMP, got.Strain, got.VolumeKG)
	}
	if got.ByMuscle["lats"] != 1200 || got.ByMuscle["biceps"] != 600 {
		t.Errorf("volume by muscle uses the effective weight: %v", got.ByMuscle)
	}
	old := withoutAssist(b.s)
	before := LoadOfSession(old, b.exs, BestE1RMs(unassisted(b.own()), b.exs, 80), 80)
	if before.TRIMP != 3.9 || before.VolumeKG != 1920 {
		t.Errorf("2026.10.1 (support ignored): TRIMP %v volume %v, want 3.9 1920", before.TRIMP, before.VolumeKG)
	}
}

// The same 3 × 8 with 30 kg of support, one week after a set of 5 unassisted
// pull-ups (in the window).
//
//	e1RM      max(80 × (1 + 5/30) = 93.3, 63.3) = 93.3 (2026.10.1: 101.3,
//	          from the assisted sets counted as unassisted)
//	intensity (50 / 93.3)² = 0.287
//	TRIMP     24 × 0.287 × 0.44 × 0.6 = 1.8, strain 0.5 (2026.10.1: 3.9, 1.0)
//
// Records: the five unassisted reps hold max_reps (2026.10.1: the 8 assisted
// reps did).
func TestAssistedSetAfterUnassisted(t *testing.T) {
	lib := library(t)
	pu := lib["pull-up"]
	b := newBuilder().assisted(pu, 3, 8, 30)
	week := start.Add(-7 * 24 * time.Hour)
	five := 5
	hist := store.LoggedSet{SessionID: uuid.New(), ExerciseID: pu.ID, SessionStart: week,
		Set: model.Set{ID: uuid.New(), Reps: &five, CompletedAt: &week}}
	sets := append([]store.LoggedSet{hist}, b.own()...)
	e1rm := E1RMsAsOf(sets, b.exs, 80, HistoryUntil(b.s), b.s.ID)
	near(t, "e1RM from the unassisted set", e1rm[pu.ID], 93.33, 0.01)
	got := LoadOfSession(b.s, b.exs, e1rm, 80)
	if got.TRIMP != 1.8 || got.Strain != 0.5 || got.VolumeKG != 1200 {
		t.Errorf("assisted after unassisted: TRIMP %v strain %v volume %v, want 1.8 0.5 1200", got.TRIMP, got.Strain, got.VolumeKG)
	}
	before := LoadOfSession(withoutAssist(b.s), b.exs, BestE1RMs(unassisted(sets), b.exs, 80), 80)
	if before.TRIMP != 3.9 || before.Strain != 1.0 {
		t.Errorf("2026.10.1: TRIMP %v strain %v, want 3.9 1.0", before.TRIMP, before.Strain)
	}
	prs := PersonalRecords(sets, pu)
	if len(prs) != 1 || prs[0].Kind != model.PRMaxReps || prs[0].Value != 5 || prs[0].SetID != hist.ID {
		t.Errorf("only the unassisted set holds a record: %+v", prs)
	}
	if old := PersonalRecords(unassisted(sets), pu); len(old) != 1 || old[0].Value != 8 {
		t.Errorf("2026.10.1 gave the assisted reps the record: %+v", old)
	}
}

// Support at or above the moved body weight leaves nothing to lift: no load,
// no volume, no e1RM; a record neither.
func TestAssistanceFloor(t *testing.T) {
	lib := library(t)
	pu, dip := lib["pull-up"], lib["dip"]
	for _, c := range []struct {
		ex     model.Exercise
		assist float64
	}{{pu, 80}, {pu, 120}, {dip, 72}} { // dip: 80 × 0.9 = 72
		b := newBuilder().assisted(c.ex, 2, 10, c.assist)
		st := b.s.Exercises[0].Sets[0]
		if w, ok := EffectiveWeight(c.ex, st, 80); !ok || w != 0 {
			t.Errorf("%s with %v kg support: weight %v, want 0", c.ex.Slug, c.assist, w)
		}
		if e := BestE1RMs(b.own(), b.exs, 80); e[c.ex.ID] != 0 {
			t.Errorf("%s: no e1RM from nothing lifted: %v", c.ex.Slug, e)
		}
		if got := LoadOfSession(b.s, b.exs, map[uuid.UUID]float64{c.ex.ID: 50}, 80); got.TRIMP != 0 || got.VolumeKG != 0 {
			t.Errorf("%s: load %+v, want none", c.ex.Slug, got)
		}
	}
	// 10 kg less support is 10 kg more weight, kg for kg (the support acts on the moving body).
	a, _ := EffectiveWeight(dip, model.Set{AssistKG: kg(30)}, 80)
	b, _ := EffectiveWeight(dip, model.Set{AssistKG: kg(20)}, 80)
	near(t, "dip with 30 kg support", a, 42, 1e-9)
	near(t, "10 kg less support", b-a, 10, 1e-9)
}

// Assisted sets never hold a record; an exercise logged only with support has
// none, and the unassisted set keeps max_reps whatever the assisted reps.
func TestAssistedSetsHoldNoRecord(t *testing.T) {
	lib := library(t)
	pu := lib["pull-up"]
	only := newBuilder().assisted(pu, 3, 12, 25)
	if prs := PersonalRecords(only.own(), pu); len(prs) != 0 {
		t.Errorf("assisted sets alone hold no record: %+v", prs)
	}
	six := 6
	at := start.Add(-time.Hour)
	plain := store.LoggedSet{SessionID: uuid.New(), ExerciseID: pu.ID, Set: model.Set{ID: uuid.New(), Reps: &six, CompletedAt: &at}}
	prs := PersonalRecords(append([]store.LoggedSet{plain}, only.own()...), pu)
	if len(prs) != 1 || prs[0].Value != 6 || prs[0].SetID != plain.ID {
		t.Errorf("6 unassisted reps beat 12 assisted ones: %+v", prs)
	}
	// A weighted set (a belt) still holds max_weight and max_reps.
	belt := store.LoggedSet{SessionID: uuid.New(), ExerciseID: pu.ID, Set: model.Set{ID: uuid.New(), Reps: &six, WeightKG: kg(20), CompletedAt: &at}}
	if prs := PersonalRecords([]store.LoggedSet{belt}, pu); len(prs) != 2 {
		t.Errorf("a belt set: %+v", prs)
	}
}

// assist_kg null or 0 is a set without support: every number is what 2026.10.1
// gave, so the stored history (which has no assist_kg) moves only through the
// e1RM window and the session's end (D135), never through D134. A weight
// exercise's assist_kg (refused by validation) is ignored.
func TestAssistNilOrZeroChangesNothing(t *testing.T) {
	lib := library(t)
	exs := []model.Exercise{lib["back-squat"], lib["pull-up"], lib["push-up"], lib["weighted-dip"], lib["plank"], lib["bench-press"]}
	rng := rand.New(rand.NewPCG(7, 11))
	zero := 0.0
	for k := 0; k < 500; k++ {
		ex := exs[rng.IntN(len(exs))]
		r := rng.IntN(20)
		done := start.Add(time.Duration(k) * time.Minute)
		st := model.Set{ID: uuid.New(), Reps: &r, CompletedAt: &done, IsWarmup: rng.IntN(5) == 0}
		if rng.IntN(2) == 0 {
			w := float64(rng.IntN(200))
			st.WeightKG = &w
		}
		d := rng.IntN(300)
		st.DurationSec = &d
		withZero := st
		withZero.AssistKG = &zero
		bw := 60 + float64(rng.IntN(40))
		e1 := 50 + float64(rng.IntN(150))
		w1, ok1 := EffectiveWeight(ex, st, bw)
		w2, ok2 := EffectiveWeight(ex, withZero, bw)
		if w1 != w2 || ok1 != ok2 || SetLoad(st, e1, ex, bw) != SetLoad(withZero, e1, ex, bw) {
			t.Fatalf("%s: assist 0 differs from none: %v %v", ex.Slug, w1, w2)
		}
		a := []store.LoggedSet{{SessionID: uuid.New(), ExerciseID: ex.ID, Set: st}}
		b := []store.LoggedSet{{SessionID: a[0].SessionID, ExerciseID: ex.ID, Set: withZero}}
		if !slices.Equal(PersonalRecords(a, ex), PersonalRecords(b, ex)) {
			t.Fatalf("%s: records differ with assist 0", ex.Slug)
		}
	}
	sq := lib["back-squat"]
	if w, _ := EffectiveWeight(sq, model.Set{WeightKG: kg(100), AssistKG: kg(30)}, 80); w != 100 {
		t.Errorf("a weight set's assist_kg is ignored: %v", w)
	}
	// The calibration anchors of 2026.10.1 are unchanged (strength_test.go
	// pins them with assist_kg null; here with 0 on every set).
	ref := newBuilder().add(sq, 5, 5, kg(112)).add(lib["bench-press"], 5, 5, kg(80)).add(lib["barbell-row"], 5, 5, kg(72))
	for k := range ref.s.Exercises {
		for j := range ref.s.Exercises[k].Sets {
			ref.s.Exercises[k].Sets[j].AssistKG = &zero
		}
	}
	e1rm := map[uuid.UUID]float64{sq.ID: 140, lib["bench-press"].ID: 100, lib["barbell-row"].ID: 90}
	if got := LoadOfSession(ref.s, ref.exs, e1rm, 80); got.Strain != 11.0 || got.TRIMP != 59.3 {
		t.Errorf("reference session with assist 0: %+v", got)
	}
}

// The e1RM reference is the best of the 90 days before the session's end
// (D135). The import case: a lifter whose squat peaked two years ago at
// 140 × 5 (e1RM 163.3) imports that history, and now squats 5 × 5 at 100 kg
// (e1RM 116.7 from its own sets).
//
//	2026.10.1 (best ever):  intensity (100 / 163.3)² = 0.375,
//	                        TRIMP 25 × 0.375 × 2.9 × 0.6 = 16.3, strain 3.9
//	2026.10.2 (90 days):    intensity (100 / 116.7)² = 0.735,
//	                        TRIMP 25 × 0.735 × 2.9 × 0.6 = 32.0, strain 6.9
//
// Without the window the import would have nearly halved every later squat
// session's load, for as long as the old peak stands. The reference session
// (5 × 5 squat, bench, row at 80 %) stays 11.0 next to peaks 25 % above its
// e1RMs from two years ago; against them, best-ever scoring gives 7.9.
func TestE1RMWindow(t *testing.T) {
	lib := library(t)
	sq := lib["back-squat"]
	cur := newBuilder().add(sq, 5, 5, kg(100))
	twoYears := start.AddDate(-2, 0, 0)
	five := 5
	peak := store.LoggedSet{SessionID: uuid.New(), ExerciseID: sq.ID, SessionStart: twoYears,
		Set: model.Set{ID: uuid.New(), Reps: &five, WeightKG: kg(140), CompletedAt: &twoYears}}
	sets := append([]store.LoggedSet{peak}, cur.own()...)
	until := HistoryUntil(cur.s)
	ever, window := BestE1RMs(sets, cur.exs, 80), E1RMsAsOf(sets, cur.exs, 80, until, cur.s.ID)
	near(t, "best ever", ever[sq.ID], 163.33, 0.01)
	near(t, "best of 90 days", window[sq.ID], 116.67, 0.01)
	if b := LoadOfSession(cur.s, cur.exs, ever, 80); b.TRIMP != 16.3 || b.Strain != 3.9 {
		t.Errorf("2026.10.1 against the old peak: %+v", b)
	}
	if a := LoadOfSession(cur.s, cur.exs, window, 80); a.TRIMP != 32.0 || a.Strain != 6.9 {
		t.Errorf("2026.10.2: %+v", a)
	}

	// The reference session next to peaks 25 % higher, two years old.
	bp, row := lib["bench-press"], lib["barbell-row"]
	ref := newBuilder().add(sq, 5, 5, kg(112)).add(bp, 5, 5, kg(80)).add(row, 5, 5, kg(72))
	old := func(ex model.Exercise, w float64) store.LoggedSet { // w × (1 + 5/30) = 1.25 × the anchor's e1RM
		return store.LoggedSet{SessionID: peak.SessionID, ExerciseID: ex.ID, Set: model.Set{ID: uuid.New(), Reps: &five, WeightKG: kg(w), CompletedAt: &twoYears}}
	}
	recent := func(ex model.Exercise, w float64) store.LoggedSet { // the anchor's e1RM, a week ago
		week := start.Add(-7 * 24 * time.Hour)
		return store.LoggedSet{SessionID: uuid.New(), ExerciseID: ex.ID, Set: model.Set{ID: uuid.New(), Reps: &five, WeightKG: kg(w), CompletedAt: &week}}
	}
	hist := []store.LoggedSet{old(sq, 150), old(bp, 107.142857), old(row, 96.428571),
		recent(sq, 120), recent(bp, 85.714286), recent(row, 77.142857)}
	all := append(hist, ref.own()...)
	if got := LoadOfSession(ref.s, ref.exs, E1RMsAsOf(all, ref.exs, 80, HistoryUntil(ref.s), ref.s.ID), 80); got.Strain != 11.0 {
		t.Errorf("reference session with the window: %+v", got)
	}
	if got := LoadOfSession(ref.s, ref.exs, BestE1RMs(all, ref.exs, 80), 80); got.Strain != 7.9 {
		t.Errorf("reference session against two-year-old peaks, best ever: %+v", got)
	}

	// The window's edges: (asOf − 90 days, asOf]; the session's own sets always.
	at := func(d time.Duration, w float64) store.LoggedSet {
		t := until.Add(-d)
		return store.LoggedSet{SessionID: uuid.New(), ExerciseID: sq.ID, Set: model.Set{ID: uuid.New(), Reps: &five, WeightKG: kg(w), CompletedAt: &t}}
	}
	cases := []struct {
		name string
		set  store.LoggedSet
		want float64
	}{
		{"exactly 90 days before is out", at(E1RMWindow, 200), 116.67},
		{"a minute inside is in", at(E1RMWindow-time.Minute, 200), 233.33},
		{"after the session's history is out", at(-time.Hour, 200), 116.67},
	}
	for _, c := range cases {
		got := E1RMsAsOf(append([]store.LoggedSet{c.set}, cur.own()...), cur.exs, 80, until, cur.s.ID)
		near(t, c.name, got[sq.ID], c.want, 0.01)
	}
	strange := cur.own()
	far := until.Add(-200 * 24 * time.Hour)
	strange[0].CompletedAt = &far // a set of its own, stamped long before
	if got := E1RMsAsOf(strange, cur.exs, 80, until, cur.s.ID); got[sq.ID] < 116 {
		t.Errorf("the session's own sets count whenever they were stamped: %v", got)
	}
	// A plan target's current e1RM (own = uuid.Nil): nothing in the window, nothing to scale.
	if got := E1RMsAsOf([]store.LoggedSet{peak}, cur.exs, 80, start, uuid.Nil); len(got) != 0 {
		t.Errorf("no current e1RM from a two-year-old set: %v", got)
	}
}

// A session's history ends at its last set when that is after ended_at (an
// assumed 60 minutes, a drifting clock): its own sets count, so the load does
// not depend on ended_at (D138). Minutes and the workout match still use
// ended_at (SessionEnd). A first squat session, no history: 4 × 5 at 100 kg
// (+2 … +8 min) and a top set of 3 at 110 kg stamped at +70 min.
//
//	2026.10.2, any ended_at: e1RM max(116.7, 110 × 1.1 = 121) = 121
//	          TRIMP (20 × (100/121)² + 3 × (110/121)²) × 2.9 × 0.6 = 28.1
//	2026.10.1, ended_at +60: the top set is cut off, e1RM 116.7,
//	          TRIMP (20 × 0.735 + 3 × 0.889) × 1.74 = 30.2; ended_at +120: 28.1
func TestHistoryUntilAndAssumedDurations(t *testing.T) {
	lib := library(t)
	sq := lib["back-squat"]
	b := newBuilder().add(sq, 4, 5, kg(100))
	top := start.Add(70 * time.Minute)
	three := 3
	b.s.Exercises[0].Sets = append(b.s.Exercises[0].Sets, model.Set{ID: uuid.New(), SetIndex: 4, Reps: &three, WeightKG: kg(110), CompletedAt: &top, UpdatedAt: top})
	pool := b.own()
	logged := func(until time.Time) []store.LoggedSet { // store.LoggedSets: completed_at ≤ until
		var out []store.LoggedSet
		for _, l := range pool {
			if !l.CompletedAt.After(until) {
				out = append(out, l)
			}
		}
		return out
	}
	for _, c := range []struct {
		minutes   int
		before    float64 // 2026.10.1: history cut at SessionEnd
		after     float64
		untilWant time.Time
	}{
		{45, 30.2, 28.1, top},
		{60, 30.2, 28.1, top},
		{120, 28.1, 28.1, start.Add(120 * time.Minute)},
	} {
		s := b.s
		end := start.Add(time.Duration(c.minutes) * time.Minute)
		s.EndedAt = &end
		until := HistoryUntil(s)
		if !until.Equal(c.untilWant) || !SessionEnd(s).Equal(end) {
			t.Errorf("ended_at +%d: history until %v, end %v", c.minutes, until, SessionEnd(s))
		}
		after := LoadOfSession(s, b.exs, E1RMsAsOf(logged(until), b.exs, 80, until, s.ID), 80)
		before := LoadOfSession(s, b.exs, BestE1RMs(logged(SessionEnd(s)), b.exs, 80), 80)
		if after.TRIMP != c.after || before.TRIMP != c.before {
			t.Errorf("ended_at +%d: TRIMP %v (2026.10.1: %v), want %v (%v)", c.minutes, after.TRIMP, before.TRIMP, c.after, c.before)
		}
	}
}

// Which later sessions an import (or any change to past sessions) can move
// (DependentScope, D135): the e1RM window and the body-weight fallback.
func TestDependentScope(t *testing.T) {
	lib := library(t)
	sq, bp, pu := lib["back-squat"], lib["bench-press"], lib["pull-up"]
	exs := map[uuid.UUID]model.Exercise{sq.ID: sq, bp.ID: bp, pu.ID: pu}
	day := func(n int) time.Time { return time.Date(2026, 3, 1, 17, 0, 0, 0, time.UTC).AddDate(0, 0, n) }
	sess := func(startDay int, bw *float64, ex ...model.Exercise) model.Session {
		s := model.Session{ID: uuid.New(), StartedAt: day(startDay), BodyweightKG: bw, Status: model.SessionCompleted}
		end := s.StartedAt.Add(time.Hour)
		s.EndedAt = &end
		for k, e := range ex {
			done, r := s.StartedAt.Add(10*time.Minute), 5
			s.Exercises = append(s.Exercises, model.SessionExercise{ID: uuid.New(), ExerciseID: e.ID, Position: k,
				Sets: []model.Set{{ID: uuid.New(), Reps: &r, WeightKG: kg(60), CompletedAt: &done}}})
		}
		return s
	}
	if _, ok := ScopeOf(nil); ok {
		t.Fatal("no changed sessions, no scope")
	}
	// Imported: a squat session on day 0 (with a body weight), a bench session on day 10.
	scope, ok := ScopeOf([]model.Session{sess(10, nil, bp), sess(0, kg(82), sq)})
	if !ok || !scope.Since.Equal(day(0)) || len(scope.Exercises) != 2 || scope.BodyweightSince == nil || !scope.BodyweightSince.Equal(day(0)) {
		t.Fatalf("scope: %+v", scope)
	}
	if want := day(10).Add(time.Hour + E1RMWindow); !scope.Through.Equal(want) {
		t.Errorf("through %v, want the newest changed session's end + 90 days %v", scope.Through, want)
	}
	cases := []struct {
		name string
		s    model.Session
		want bool
	}{
		{"a later squat session", sess(30, kg(80), sq), true},
		{"a later bench session 99 days after the bench import", sess(99, kg(80), bp), true},
		{"a bench session after the window", sess(101, kg(80), bp), false},
		{"an earlier squat session", sess(-1, kg(80), sq), false},
		{"the same start", sess(0, kg(80), sq), false},
		{"a later pull-up session with its own body weight", sess(30, kg(80), pu), false},
		{"a later pull-up session without one: its fallback may be the import's 82 kg", sess(300, nil, pu), true},
		{"a later squat-only session without a body weight, after the window", sess(300, nil, sq), false},
	}
	for _, c := range cases {
		if got := scope.Covers(c.s, exs); got != c.want {
			t.Errorf("%s: covered %v, want %v", c.name, got, c.want)
		}
	}
	// Without a body weight among the changed sessions the fallback cannot move.
	plain, _ := ScopeOf([]model.Session{sess(0, nil, sq)})
	if plain.BodyweightSince != nil || plain.Covers(sess(30, nil, pu), exs) {
		t.Errorf("no body weight imported: %+v", plain)
	}
	// An update's replaced version counts: its exercises leave later e1RMs too.
	upd, _ := ScopeOf([]model.Session{sess(5, nil, bp), sess(5, nil, sq)})
	if !upd.Covers(sess(20, kg(80), sq), exs) || !upd.Covers(sess(20, kg(80), bp), exs) {
		t.Error("both versions' exercises are in the scope")
	}
}
