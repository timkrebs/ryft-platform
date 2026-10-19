package train

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestFixedIDsNeverChange(t *testing.T) {
	// Part of the contract: the app caches built-in content by these ids.
	if got := BuiltinExerciseID("back-squat").String(); got != "053b5627-084e-5417-a033-a6602d812f86" {
		t.Fatalf("back-squat id changed: %s", got)
	}
	if got := BuiltinPlanID("full-body-3x").String(); got != "58f0d1ad-9c48-5917-9770-df62caae3f91" {
		t.Fatalf("full-body-3x id changed: %s", got)
	}
	a, b := BuiltinExerciseID("back-squat"), BuiltinExerciseID("back-squat")
	if a != b || a == BuiltinExerciseID("front-squat") || a == BuiltinPlanID("back-squat") {
		t.Fatal("ids must be deterministic and distinct per kind and slug")
	}
	if BuiltinPlanDayID("p", 0, 1) == BuiltinPlanDayID("p", 1, 0) || BuiltinPlanExerciseID("p", 0, 0, 1) == BuiltinPlanExerciseID("p", 0, 1, 0) {
		t.Fatal("day and position must both enter the id")
	}
	u1, u2, s := uuid.New(), uuid.New(), uuid.New()
	if SessionWorkoutID(u1, s) == SessionWorkoutID(u2, s) {
		t.Fatal("the created workout's id is per user")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Back squat": "back-squat", "  Kniebeuge mit Pause!! ": "kniebeuge-mit-pause", "Überkopf-Drücken": "ueberkopf-druecken",
		"Fuß": "fuss", "5/3/1": "5-3-1", "***": "exercise", "Bench press (close grip)": "bench-press-close-grip",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGerman(t *testing.T) {
	for h, want := range map[string]bool{"de": true, "de-DE,en;q=0.8": true, "DE-at": true, "en-US,de;q=0.9": false, "": false, "dk": false} {
		if German(h) != want {
			t.Errorf("German(%q) = %v", h, !want)
		}
	}
}

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func TestNewExerciseDefaultsAndValidation(t *testing.T) {
	ex, err := NewExercise(model.ExerciseInput{ID: uuid.New(), Name: " Ring dip ", Kind: model.KindBodyweight,
		PrimaryMuscles: []string{"chest", "triceps"}, LoadType: model.LoadBodyweight})
	if err != nil {
		t.Fatal(err)
	}
	if ex.NameEN != "Ring dip" || ex.NameDE != "Ring dip" || ex.BodyweightFactor != DefaultBodyweightFactor || !ex.IsCustom {
		t.Fatalf("defaults: %+v", ex)
	}
	bad := []model.ExerciseInput{
		{Name: "x", Kind: "barbell", PrimaryMuscles: []string{"chest"}, LoadType: "weight"},                // no id
		{ID: uuid.New(), Name: "", Kind: "barbell", PrimaryMuscles: []string{"chest"}, LoadType: "weight"}, // no name
		{ID: uuid.New(), Name: "x", Kind: "sled", PrimaryMuscles: []string{"chest"}, LoadType: "weight"},   // kind
		{ID: uuid.New(), Name: "x", Kind: "barbell", PrimaryMuscles: []string{"pecs"}, LoadType: "weight"}, // muscle
		{ID: uuid.New(), Name: "x", Kind: "barbell", PrimaryMuscles: nil, LoadType: "weight"},              // no primary
		{ID: uuid.New(), Name: "x", Kind: "barbell", PrimaryMuscles: []string{"chest"}, LoadType: "reps"},  // load type
		{ID: uuid.New(), Name: "x", Kind: "barbell", PrimaryMuscles: []string{"chest"}, LoadType: "weight", BodyweightFactor: f(3)},
	}
	for k, in := range bad {
		var p Problem
		if _, err := NewExercise(in); !errors.As(err, &p) {
			t.Errorf("case %d must be a validation problem, got %v", k, err)
		}
	}
}

func plan() model.Plan {
	w := 4
	return model.Plan{ID: uuid.New(), Name: "P", Goal: "strength", Weeks: &w, DaysPerWeek: 3,
		Progression: &model.Progression{Type: model.ProgressionLinear, IncrementKG: f(2.5)},
		Days: []model.PlanDay{{ID: uuid.New(), DayIndex: 0, Exercises: []model.PlanExercise{
			{ID: uuid.New(), ExerciseID: uuid.New(), Sets: 5, RepsMin: 5, RepsMax: 5, TargetKind: model.TargetKG, TargetValue: f(60), RestSec: 180},
		}}}}
}

func TestCheckPlan(t *testing.T) {
	if err := CheckPlan(plan()); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*model.Plan){
		"goal":         func(p *model.Plan) { p.Goal = "fun" },
		"day index":    func(p *model.Plan) { p.Days[0].DayIndex = 3 },
		"week range":   func(p *model.Plan) { p.Days[0].Week = i(5) },
		"week w/o len": func(p *model.Plan) { p.Weeks = nil; p.Days[0].Week = i(1) },
		"reps":         func(p *model.Plan) { p.Days[0].Exercises[0].RepsMin = 6 },
		"pct target": func(p *model.Plan) {
			p.Days[0].Exercises[0].TargetKind = model.TargetPct1RM
			p.Days[0].Exercises[0].TargetValue = nil
		},
		"increment": func(p *model.Plan) { p.Progression.IncrementKG = nil },
		"same slot": func(p *model.Plan) { p.Days = append(p.Days, model.PlanDay{ID: uuid.New(), DayIndex: 0}) },
		"no days":   func(p *model.Plan) { p.Days = nil },
	} {
		p := plan()
		mut(&p)
		if err := CheckPlan(p); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

var t0 = time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)

func set(idx int, reps int, kg float64, at time.Time) model.Set {
	done := at
	return model.Set{ID: uuid.New(), SetIndex: idx, Reps: &reps, WeightKG: &kg, CompletedAt: &done, UpdatedAt: at}
}

func session(sets ...model.Set) model.Session {
	return model.Session{ID: uuid.New(), Title: "S", StartedAt: t0, Status: model.SessionInProgress,
		Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: uuid.New(), Sets: sets}}}
}

func TestCheckSessionClampsAndRejects(t *testing.T) {
	s := session(set(0, 500, 900, t0))
	s.Exercises[0].Sets[0].RPE = f(14)
	s.Exercises[0].Sets[0].UpdatedAt = time.Time{}
	if err := CheckSession(&s, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st := s.Exercises[0].Sets[0]
	if *st.Reps != 100 || *st.WeightKG != 500 || *st.RPE != 10 || !st.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("clamped: reps %d kg %v rpe %v updated %v", *st.Reps, *st.WeightKG, *st.RPE, st.UpdatedAt)
	}
	for name, mut := range map[string]func(*model.Session){
		"no start":  func(s *model.Session) { s.StartedAt = time.Time{} },
		"end first": func(s *model.Session) { e := t0.Add(-time.Minute); s.EndedAt = &e },
		"status":    func(s *model.Session) { s.Status = "paused" },
		"dup index": func(s *model.Session) { s.Exercises[0].Sets = append(s.Exercises[0].Sets, set(0, 5, 50, t0)) },
		"dup pos": func(s *model.Session) {
			s.Exercises = append(s.Exercises, model.SessionExercise{ID: uuid.New(), ExerciseID: uuid.New()})
		},
		"dup set id": func(s *model.Session) {
			x := s.Exercises[0].Sets[0]
			x.SetIndex = 1
			s.Exercises[0].Sets = append(s.Exercises[0].Sets, x)
		},
	} {
		s := session(set(0, 5, 100, t0))
		mut(&s)
		if err := CheckSession(&s, t0); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestMergeKeepsServerOnlySetsWhileInProgress(t *testing.T) {
	a, b := set(0, 5, 100, t0), set(1, 5, 100, t0.Add(3*time.Minute))
	stored := session(a, b)
	// An older queued save that only knew the first set.
	body := stored
	body.Exercises = []model.SessionExercise{stored.Exercises[0]}
	body.Exercises[0].Sets = []model.Set{a}
	body.Title = "renamed"
	merged, conflict := Merge(&stored, body)
	if conflict {
		t.Fatal("same updated_at is a replay, not a conflict")
	}
	if len(merged.Exercises[0].Sets) != 2 || merged.Title != "renamed" {
		t.Fatalf("server-only set must be kept, fields from the body: %+v", merged)
	}
	// An exercise only the server has is kept too.
	extra := model.SessionExercise{ID: uuid.New(), ExerciseID: uuid.New(), Position: 1}
	stored.Exercises = append(stored.Exercises, extra)
	merged, _ = Merge(&stored, body)
	if len(merged.Exercises) != 2 || merged.Exercises[1].ID != extra.ID {
		t.Fatalf("server-only exercise must be kept: %+v", merged.Exercises)
	}
}

func TestMergeConflictOnNewerServerSet(t *testing.T) {
	a := set(0, 5, 100, t0.Add(5*time.Minute))
	stored := session(a)
	body := stored
	body.Exercises = []model.SessionExercise{stored.Exercises[0]}
	older := a
	older.UpdatedAt = t0
	older.Reps = i(3)
	body.Exercises[0].Sets = []model.Set{older}
	got, conflict := Merge(&stored, body)
	if !conflict || *got.Exercises[0].Sets[0].Reps != 5 {
		t.Fatalf("a newer server set must win with 409 and the server copy: conflict=%v", conflict)
	}
	newer := a
	newer.UpdatedAt = t0.Add(6 * time.Minute)
	newer.Reps = i(4)
	body.Exercises[0].Sets = []model.Set{newer}
	got, conflict = Merge(&stored, body)
	if conflict || *got.Exercises[0].Sets[0].Reps != 4 {
		t.Fatal("a newer body set is written")
	}
}

func TestMergeReplacesCompletedSessionWhole(t *testing.T) {
	a, b := set(0, 5, 100, t0), set(1, 5, 100, t0)
	stored := session(a, b)
	stored.Status = model.SessionCompleted
	body := stored
	body.Exercises = []model.SessionExercise{stored.Exercises[0]}
	body.Exercises[0].Sets = []model.Set{a}
	end := t0.Add(time.Hour)
	stored.EndedAt = &end
	merged, conflict := Merge(&stored, body)
	if conflict || len(merged.Exercises[0].Sets) != 1 {
		t.Fatalf("a completed session is replaced whole: conflict=%v sets=%d", conflict, len(merged.Exercises[0].Sets))
	}
	if merged.EndedAt == nil || !merged.EndedAt.Equal(end) {
		t.Fatal("a completed session keeps its end when the body has none")
	}
}

func TestMergeNew(t *testing.T) {
	body := session(set(0, 5, 100, t0))
	if got, conflict := Merge(nil, body); conflict || got.ID != body.ID {
		t.Fatal("a new session is the body")
	}
}
