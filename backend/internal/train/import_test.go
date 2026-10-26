package train

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// clone is a deep copy (the table cases mutate their own).
func clone[T any](t *testing.T, v T) T {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// importedSession is a stored imported session: two exercises, three completed sets,
// with the server's fields filled in.
func importedSession() model.Session {
	src, end := "alpha_progression", t0.Add(time.Hour)
	a, b, c := set(0, 5, 100, t0.Add(10*time.Minute)), set(1, 5, 100, t0.Add(15*time.Minute)), set(0, 8, 0, t0.Add(30*time.Minute))
	c.WeightKG = nil
	return model.Session{ID: uuid.New(), Title: "Lower", StartedAt: t0, EndedAt: &end, Status: model.SessionCompleted,
		Notes: "", BodyweightKG: f(82), Origin: model.OriginImport, ImportSource: &src,
		MuscularTRIMP: f(55.3), Strain: f(10.6), VolumeKG: f(1000), WorkoutID: ptrTo(uuid.New()), UpdatedAt: t0.Add(2 * time.Hour),
		PRs: []model.PR{{ExerciseID: uuid.New(), Kind: model.PRE1RM, Value: 116}},
		Exercises: []model.SessionExercise{
			{ID: uuid.New(), ExerciseID: uuid.New(), Position: 0, Sets: []model.Set{a, b}},
			{ID: uuid.New(), ExerciseID: uuid.New(), Position: 1, PlanExerciseID: ptrTo(uuid.New()), Sets: []model.Set{c}},
		}}
}

func ptrTo[T any](v T) *T { return &v }

func TestSameSession(t *testing.T) {
	stored := importedSession()
	for _, tc := range []struct {
		name string
		mut  func(*model.Session)
		same bool
	}{
		{"identical", func(*model.Session) {}, true},
		{"the server's fields differ", func(s *model.Session) {
			s.MuscularTRIMP, s.Strain, s.VolumeKG, s.WorkoutID, s.PRs, s.UpdatedAt, s.Origin = nil, nil, nil, nil, nil, time.Time{}, ""
		}, true},
		{"status is not content", func(s *model.Session) { s.Status = model.SessionInProgress }, true},
		{"exercises reordered", func(s *model.Session) { s.Exercises[0], s.Exercises[1] = s.Exercises[1], s.Exercises[0] }, true},
		{"sets reordered", func(s *model.Session) {
			x := s.Exercises[0].Sets
			x[0], x[1] = x[1], x[0]
		}, true},
		{"another zone, the same instant", func(s *model.Session) {
			s.StartedAt = s.StartedAt.In(time.FixedZone("x", 2*3600))
			e := s.EndedAt.In(time.FixedZone("y", -5*3600))
			s.EndedAt = &e
		}, true},
		{"below a microsecond", func(s *model.Session) {
			s.StartedAt = s.StartedAt.Add(400 * time.Nanosecond)
			s.Exercises[0].Sets[0].UpdatedAt = s.Exercises[0].Sets[0].UpdatedAt.Add(999 * time.Nanosecond)
		}, true},
		{"weight_kg null equals 0", func(s *model.Session) { s.Exercises[1].Sets[0].WeightKG = f(0) }, true},
		{"assist_kg null equals 0", func(s *model.Session) { s.Exercises[1].Sets[0].AssistKG = f(0) }, true},
		{"reps", func(s *model.Session) { s.Exercises[0].Sets[0].Reps = i(6) }, false},
		{"weight_kg", func(s *model.Session) { s.Exercises[0].Sets[0].WeightKG = f(102.5) }, false},
		{"assist_kg", func(s *model.Session) { s.Exercises[1].Sets[0].AssistKG = f(20) }, false},
		{"completed_at", func(s *model.Session) {
			c := s.Exercises[0].Sets[0].CompletedAt.Add(time.Second)
			s.Exercises[0].Sets[0].CompletedAt = &c
		}, false},
		{"updated_at of a set", func(s *model.Session) {
			s.Exercises[0].Sets[0].UpdatedAt = s.Exercises[0].Sets[0].UpdatedAt.Add(time.Microsecond)
		}, false},
		{"set_index", func(s *model.Session) { s.Exercises[0].Sets[1].SetIndex = 2 }, false},
		{"is_warmup", func(s *model.Session) { s.Exercises[0].Sets[1].IsWarmup = true }, false},
		{"a set less", func(s *model.Session) { s.Exercises[0].Sets = s.Exercises[0].Sets[:1] }, false},
		{"a set more", func(s *model.Session) {
			s.Exercises[1].Sets = append(s.Exercises[1].Sets, set(1, 8, 0, t0.Add(40*time.Minute)))
		}, false},
		{"an exercise less", func(s *model.Session) { s.Exercises = s.Exercises[:1] }, false},
		{"position", func(s *model.Session) { s.Exercises[0].Position = 2 }, false},
		{"exercise_id", func(s *model.Session) { s.Exercises[0].ExerciseID = uuid.New() }, false},
		{"plan_exercise_id", func(s *model.Session) { s.Exercises[1].PlanExerciseID = nil }, false},
		{"exercise notes", func(s *model.Session) { s.Exercises[0].Notes = "belt" }, false},
		{"import_source", func(s *model.Session) { x := "other_app"; s.ImportSource = &x }, false},
		{"title", func(s *model.Session) { s.Title = "Legs" }, false},
		{"notes", func(s *model.Session) { s.Notes = "tired" }, false},
		{"started_at", func(s *model.Session) { s.StartedAt = s.StartedAt.Add(time.Minute) }, false},
		{"ended_at", func(s *model.Session) { s.EndedAt = nil }, false},
		{"bodyweight_kg", func(s *model.Session) { s.BodyweightKG = f(83) }, false},
		{"device_id", func(s *model.Session) { s.DeviceID = ptrTo(uuid.New()) }, false},
		{"plan_day_id", func(s *model.Session) { s.PlanDayID = ptrTo(uuid.New()) }, false},
	} {
		base := clone(t, stored)
		body := clone(t, base)
		tc.mut(&body)
		if got := SameSession(base, body); got != tc.same {
			t.Errorf("%s: SameSession = %v, want %v", tc.name, got, tc.same)
		}
		if got := SameSession(body, base); got != tc.same {
			t.Errorf("%s: not symmetric", tc.name)
		}
	}
}

// Values out of range compare after the same clamping on both sides; nil and []
// exercises are the same.
func TestSameSessionClampsAndEmpty(t *testing.T) {
	stored := importedSession()
	body := clone(t, stored)
	stored.Exercises[0].Sets[0].Reps, body.Exercises[0].Sets[0].Reps = i(500), i(100)
	stored.Exercises[0].Sets[1].RPE, body.Exercises[0].Sets[1].RPE = f(12), f(10)
	stored.Exercises[1].Sets[0].AssistKG, body.Exercises[1].Sets[0].AssistKG = f(-3), f(0)
	stored.BodyweightKG, body.BodyweightKG = f(20), f(30)
	if !SameSession(stored, body) {
		t.Fatal("values that clamp to the same must be the same")
	}
	if *stored.Exercises[0].Sets[0].Reps != 500 || *stored.BodyweightKG != 20 {
		t.Fatal("SameSession must not change its arguments")
	}
	stored.Exercises, body.Exercises = nil, []model.SessionExercise{}
	if !SameSession(stored, body) {
		t.Fatal("nil and [] exercises must be the same")
	}
}

// The request's timestamps as the app may write them: Z, +00:00 and fractional zeros are the
// same instants as the stored ones.
func TestSameSessionTimestampSpellings(t *testing.T) {
	stored := importedSession()
	stored.StartedAt = ts(t, "2026-06-08T17:02:00Z")
	stored.Exercises[0].Sets[0].UpdatedAt = ts(t, "2026-06-08T17:20:00.123456Z")
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	spelled := strings.Replace(string(raw), `"2026-06-08T17:02:00Z"`, `"2026-06-08T19:02:00.000+02:00"`, 1)
	spelled = strings.Replace(spelled, `"2026-06-08T17:20:00.123456Z"`, `"2026-06-08T17:20:00.123456000+00:00"`, 1)
	if spelled == string(raw) {
		t.Fatal("the spellings were not replaced")
	}
	var body model.Session
	if err := json.Unmarshal([]byte(spelled), &body); err != nil {
		t.Fatal(err)
	}
	if !SameSession(stored, body) {
		t.Fatal("Z, +00:00, +02:00 and fractional zeros must compare as instants")
	}
}

func TestImportConflict(t *testing.T) {
	stored := importedSession()
	for _, tc := range []struct {
		name     string
		mut      func(stored, body *model.Session)
		conflict bool
	}{
		{"identical", func(_, _ *model.Session) {}, false},
		{"the request's set is newer", func(_, b *model.Session) {
			b.Exercises[0].Sets[0].UpdatedAt = b.Exercises[0].Sets[0].UpdatedAt.Add(time.Minute)
		}, false},
		{"the stored set is newer (edited in the app)", func(s, _ *model.Session) {
			s.Exercises[0].Sets[1].UpdatedAt = s.Exercises[0].Sets[1].UpdatedAt.Add(time.Minute)
		}, true},
		{"newer by less than a microsecond", func(s, _ *model.Session) {
			s.Exercises[0].Sets[1].UpdatedAt = s.Exercises[0].Sets[1].UpdatedAt.Add(500 * time.Nanosecond)
		}, false},
		{"a newer set only the server has", func(s, b *model.Session) {
			b.Exercises[0].Sets = b.Exercises[0].Sets[:1]
			s.Exercises[0].Sets[1].UpdatedAt = s.Exercises[0].Sets[1].UpdatedAt.Add(time.Hour)
		}, false},
		{"discarded in the app", func(s, _ *model.Session) { s.Status = model.SessionDiscarded }, true},
		{"reopened", func(s, _ *model.Session) { s.Status = model.SessionInProgress }, true},
	} {
		s, b := clone(t, stored), clone(t, stored)
		tc.mut(&s, &b)
		if got := ImportConflict(s, b); got != tc.conflict {
			t.Errorf("%s: ImportConflict = %v, want %v", tc.name, got, tc.conflict)
		}
	}
}

func TestSamePlan(t *testing.T) {
	stored := plan()
	stored.Days = append(stored.Days, model.PlanDay{ID: uuid.New(), DayIndex: 1, Name: "B", Exercises: []model.PlanExercise{
		{ID: uuid.New(), ExerciseID: uuid.New(), Position: 0, Sets: 3, RepsMin: 8, RepsMax: 12, TargetKind: model.TargetBodyweight},
		{ID: uuid.New(), ExerciseID: uuid.New(), Position: 1, Sets: 3, RepsMin: 8, RepsMax: 12, TargetKind: model.TargetRPE, TargetValue: f(8)},
	}})
	for _, tc := range []struct {
		name string
		mut  func(*model.Plan)
		same bool
	}{
		{"identical", func(*model.Plan) {}, true},
		{"days reordered", func(p *model.Plan) { p.Days[0], p.Days[1] = p.Days[1], p.Days[0] }, true},
		{"exercises reordered", func(p *model.Plan) { e := p.Days[1].Exercises; e[0], e[1] = e[1], e[0] }, true},
		{"slug, is_builtin and the resolved exercise are not content", func(p *model.Plan) {
			p.Slug, p.IsBuiltin = ptrTo("mine"), true
			p.Days[0].Exercises[0].Exercise = &model.Exercise{NameEN: "Back squat"}
		}, true},
		{"name", func(p *model.Plan) { p.Name = "Q" }, false},
		{"goal", func(p *model.Plan) { p.Goal = "hypertrophy" }, false},
		{"weeks", func(p *model.Plan) { p.Weeks = nil }, false},
		{"days_per_week", func(p *model.Plan) { p.DaysPerWeek = 4 }, false},
		{"description", func(p *model.Plan) { p.Description = "x" }, false},
		{"archived", func(p *model.Plan) { p.Archived = true }, false},
		{"progression", func(p *model.Plan) { p.Progression.IncrementKG = f(5) }, false},
		{"progression null vs linear", func(p *model.Plan) { p.Progression = nil }, false},
		{"a day less", func(p *model.Plan) { p.Days = p.Days[:1] }, false},
		{"day name", func(p *model.Plan) { p.Days[1].Name = "C" }, false},
		{"day week", func(p *model.Plan) { p.Days[1].Week = i(1) }, false},
		{"day_index", func(p *model.Plan) { p.Days[1].DayIndex = 2 }, false},
		{"an exercise less", func(p *model.Plan) { p.Days[1].Exercises = p.Days[1].Exercises[:1] }, false},
		{"target_value", func(p *model.Plan) { p.Days[1].Exercises[0].TargetValue = f(10) }, false},
		{"target_kind", func(p *model.Plan) { p.Days[1].Exercises[1].TargetKind = model.TargetKG }, false},
		{"sets", func(p *model.Plan) { p.Days[0].Exercises[0].Sets = 4 }, false},
		{"reps", func(p *model.Plan) { p.Days[0].Exercises[0].RepsMax = 6 }, false},
		{"rest_sec", func(p *model.Plan) { p.Days[0].Exercises[0].RestSec = 120 }, false},
		{"superset_group", func(p *model.Plan) { p.Days[0].Exercises[0].SupersetGroup = i(1) }, false},
		{"exercise notes", func(p *model.Plan) { p.Days[0].Exercises[0].Notes = "pause" }, false},
		{"exercise_id", func(p *model.Plan) { p.Days[0].Exercises[0].ExerciseID = uuid.New() }, false},
		{"position", func(p *model.Plan) { p.Days[1].Exercises[1].Position = 5 }, false},
	} {
		body := clone(t, stored)
		tc.mut(&body)
		if got := SamePlan(stored, body); got != tc.same {
			t.Errorf("%s: SamePlan = %v, want %v", tc.name, got, tc.same)
		}
	}
	// progression null equals {"type":"none"}, and only that.
	a, b := clone(t, stored), clone(t, stored)
	a.Progression, b.Progression = nil, &model.Progression{Type: model.ProgressionNone}
	if !SamePlan(a, b) || !SamePlan(b, a) {
		t.Fatal("progression null must equal none")
	}
	b.Progression.IncrementKG = f(2.5)
	if SamePlan(a, b) {
		t.Fatal("none with an increment is not null")
	}
}

func TestCheckImportSession(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	weight := model.Exercise{ID: uuid.New(), LoadType: model.LoadWeight}
	bw := model.Exercise{ID: uuid.New(), LoadType: model.LoadBodyweight}
	exercises := map[uuid.UUID]model.Exercise{weight.ID: weight, bw.ID: bw}
	valid := func() model.Session {
		end := t0.Add(time.Hour)
		s := model.Session{ID: uuid.New(), Title: "Lower", StartedAt: t0, EndedAt: &end, Status: model.SessionCompleted,
			Exercises: []model.SessionExercise{
				{ID: uuid.New(), ExerciseID: weight.ID, Position: 0, Sets: []model.Set{set(0, 5, 100, t0.Add(10*time.Minute))}},
				{ID: uuid.New(), ExerciseID: bw.ID, Position: 1, Sets: []model.Set{set(0, 6, 0, t0.Add(20*time.Minute))}},
			}}
		s.Exercises[1].Sets[0].WeightKG = nil
		s.Exercises[1].Sets[0].AssistKG = f(20)
		return s
	}

	s := valid()
	s.Exercises[0].Sets[0].UpdatedAt = time.Time{}
	s.Exercises[0].Sets[0].Reps = i(400)
	s.Exercises[1].Sets[0].AssistKG = f(250)
	if err := CheckImportSession(&s, now, exercises); err != nil {
		t.Fatal(err)
	}
	if st := s.Exercises[0].Sets[0]; !st.UpdatedAt.Equal(*st.CompletedAt) || *st.Reps != 100 {
		t.Fatalf("a missing updated_at takes completed_at (not now), reps clamp: %v %d", st.UpdatedAt, *st.Reps)
	}
	if *s.Exercises[1].Sets[0].AssistKG != 200 {
		t.Fatalf("assist_kg clamps to 200: %v", *s.Exercises[1].Sets[0].AssistKG)
	}
	// An exercise the user cannot see is the store's unknown_exercise, not invalid.
	s = valid()
	s.Exercises[0].ExerciseID = uuid.New()
	s.Exercises[0].Sets[0].AssistKG = f(5)
	if err := CheckImportSession(&s, now, exercises); err != nil {
		t.Fatalf("unknown exercise: %v", err)
	}

	for _, tc := range []struct {
		name, msg string
		mut       func(*model.Session)
	}{
		{"in progress", "status must be completed", func(s *model.Session) { s.Status = model.SessionInProgress }},
		{"discarded", "status must be completed", func(s *model.Session) { s.Status = model.SessionDiscarded }},
		{"no end", "ended_at is required", func(s *model.Session) { s.EndedAt = nil }},
		{"end before start", "ended_at must not be before started_at", func(s *model.Session) {
			e := s.StartedAt.Add(-time.Second)
			s.EndedAt = &e
		}},
		{"no start", "started_at is required", func(s *model.Session) { s.StartedAt = time.Time{} }},
		{"before 2000", "started_at must be between", func(s *model.Session) {
			s.StartedAt = time.Date(1999, 12, 31, 23, 0, 0, 0, time.UTC)
			e := s.StartedAt.Add(time.Hour)
			s.EndedAt = &e
		}},
		{"over a day ahead", "started_at must be between", func(s *model.Session) {
			s.StartedAt = now.Add(25 * time.Hour)
			e := s.StartedAt.Add(time.Hour)
			s.EndedAt = &e
		}},
		{"a set not done", "completed_at is required", func(s *model.Session) { s.Exercises[0].Sets[0].CompletedAt = nil }},
		{"assist_kg on a weight exercise", "assist_kg is only for bodyweight", func(s *model.Session) {
			s.Exercises[0].Sets[0].AssistKG = f(10)
		}},
		{"assist_kg 0 on a weight exercise", "assist_kg is only for bodyweight", func(s *model.Session) {
			s.Exercises[0].Sets[0].AssistKG = f(0)
		}},
		{"assist_kg with an added load", "cannot both be above 0", func(s *model.Session) {
			s.Exercises[1].Sets[0].WeightKG = f(10)
		}},
		{"CheckSession: a set_index twice", "set_index must be unique", func(s *model.Session) {
			x := set(0, 5, 100, t0.Add(11*time.Minute))
			s.Exercises[0].Sets = append(s.Exercises[0].Sets, x)
		}},
		{"CheckSession: title", "title: at most", func(s *model.Session) { s.Title = strings.Repeat("x", 121) }},
	} {
		s := valid()
		tc.mut(&s)
		err := CheckImportSession(&s, now, exercises)
		var p Problem
		if !errors.As(err, &p) || !strings.Contains(p.Msg, tc.msg) {
			t.Errorf("%s: want a Problem with %q, got %v", tc.name, tc.msg, err)
		}
	}
	// The edge: 24 h ahead is allowed, 2000-01-01 too.
	s = valid()
	s.StartedAt = now.Add(24 * time.Hour)
	e := s.StartedAt.Add(time.Hour)
	s.EndedAt = &e
	if err := CheckImportSession(&s, now, exercises); err != nil {
		t.Errorf("24 h ahead: %v", err)
	}
	s = valid()
	s.StartedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	e = s.StartedAt.Add(time.Hour)
	s.EndedAt = &e
	if err := CheckImportSession(&s, now, exercises); err != nil {
		t.Errorf("2000-01-01: %v", err)
	}
	// The message names ids and fields, never a value from the file.
	s = valid()
	s.Title = "Leg day with Anna"
	s.Exercises[0].Sets[0].AssistKG = f(17.5)
	err := CheckImportSession(&s, now, exercises)
	if err == nil || strings.Contains(err.Error(), "17.5") || strings.Contains(err.Error(), "Anna") ||
		!strings.Contains(err.Error(), s.Exercises[0].Sets[0].ID.String()) {
		t.Fatalf("message: %v", err)
	}
}

func TestCheckAssist(t *testing.T) {
	bw, weight := model.Exercise{LoadType: model.LoadBodyweight}, model.Exercise{LoadType: model.LoadWeight}
	timed := model.Exercise{LoadType: model.LoadTime}
	for _, tc := range []struct {
		name   string
		ex     model.Exercise
		weight *float64
		assist *float64
		ok     bool
	}{
		{"no assist, weight exercise", weight, f(100), nil, true},
		{"no assist, bodyweight", bw, nil, nil, true},
		{"assisted pull-up", bw, nil, f(20), true},
		{"assisted, weight_kg 0", bw, f(0), f(20), true},
		{"assist 0 with a belt", bw, f(10), f(0), true},
		{"assisted and loaded", bw, f(10), f(20), false},
		{"on a weight exercise", weight, f(60), f(20), false},
		{"0 on a weight exercise", weight, f(60), f(0), false},
		{"on a time exercise", timed, nil, f(5), false},
	} {
		err := CheckAssist(tc.ex, model.Set{ID: uuid.New(), WeightKG: tc.weight, AssistKG: tc.assist})
		var p Problem
		if (err == nil) != tc.ok || (err != nil && !errors.As(err, &p)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestClampSetAssist(t *testing.T) {
	for in, want := range map[float64]float64{250: 200, -5: 0, 17.5: 17.5} {
		st := model.Set{AssistKG: f(in)}
		ClampSet(&st)
		if *st.AssistKG != want {
			t.Errorf("assist_kg %v clamps to %v, got %v", in, want, *st.AssistKG)
		}
	}
	st := model.Set{}
	ClampSet(&st)
	if st.AssistKG != nil {
		t.Fatal("null stays null")
	}
}

func TestCheckPlanBodyweightTarget(t *testing.T) {
	for _, tc := range []struct {
		v  *float64
		ok bool
	}{{nil, true}, {f(0), true}, {f(20), true}, {f(500), true}, {f(-1), false}, {f(500.5), false}} {
		p := plan()
		p.Days[0].Exercises[0].TargetKind, p.Days[0].Exercises[0].TargetValue = model.TargetBodyweight, tc.v
		var problem Problem
		if err := CheckPlan(p); (err == nil) != tc.ok || (err != nil && !errors.As(err, &problem)) {
			t.Errorf("bodyweight target_value %v: %v", tc.v, err)
		}
	}
}

func TestCheckImportRequest(t *testing.T) {
	ok := model.TrainImportRequest{Source: model.ImportSourceAlphaProgression}
	if err := CheckImportRequest(ok); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	for _, tc := range []struct {
		name string
		req  model.TrainImportRequest
		msg  string
	}{
		{"unknown source", model.TrainImportRequest{Source: "other_app"}, "source must be one of"},
		{"no source", model.TrainImportRequest{}, "source must be one of"},
		{"6 plans", model.TrainImportRequest{Source: ok.Source, Plans: make([]model.Plan, 6)}, "at most 5 plans"},
		{"51 sessions", model.TrainImportRequest{Source: ok.Source, Sessions: make([]model.ImportSession, 51)}, "at most 50 sessions"},
		{"a plan twice", model.TrainImportRequest{Source: ok.Source, Plans: []model.Plan{{ID: id}, {ID: uuid.New()}, {ID: id}}}, "plan " + id.String()},
		{"a session twice", model.TrainImportRequest{Source: ok.Source, Sessions: []model.ImportSession{{ID: id}, {ID: id}}}, "session " + id.String()},
	} {
		err := CheckImportRequest(tc.req)
		var p Problem
		if !errors.As(err, &p) || !strings.Contains(p.Msg, tc.msg) {
			t.Errorf("%s: want %q, got %v", tc.name, tc.msg, err)
		}
		if err != nil && strings.Contains(err.Error(), "other_app") {
			t.Errorf("%s: the message quotes the value", tc.name)
		}
	}
	// Ids left out are each item's own invalid, not the whole request's.
	if err := CheckImportRequest(model.TrainImportRequest{Source: ok.Source, Plans: make([]model.Plan, 2),
		Sessions: make([]model.ImportSession, 2)}); err != nil {
		t.Fatalf("nil ids: %v", err)
	}
	// The limits themselves are fine.
	if err := CheckImportRequest(model.TrainImportRequest{Source: ok.Source, Plans: plans(5), Sessions: sessions(50)}); err != nil {
		t.Fatalf("5 plans, 50 sessions: %v", err)
	}
}

func plans(n int) []model.Plan {
	out := make([]model.Plan, n)
	for k := range out {
		out[k].ID = uuid.New()
	}
	return out
}

func sessions(n int) []model.ImportSession {
	out := make([]model.ImportSession, n)
	for k := range out {
		out[k].ID = uuid.New()
	}
	return out
}

func TestImportOrder(t *testing.T) {
	a := uuid.MustParse("10000000-0000-0000-0000-000000000000")
	b := uuid.MustParse("20000000-0000-0000-0000-000000000000")
	c := uuid.MustParse("30000000-0000-0000-0000-000000000000")
	in := []model.Session{
		{ID: c, StartedAt: t0},
		{ID: a, StartedAt: t0.Add(time.Hour)},
		{ID: b, StartedAt: t0},
		{ID: a, StartedAt: t0.Add(-time.Hour).In(time.FixedZone("x", 3600))},
	}
	got := ImportOrder(in)
	if want := []int{3, 2, 0, 1}; len(got) != len(want) || got[0] != 3 || got[1] != 2 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("oldest first, ties by id: %v, want %v", got, want)
	}
	if len(ImportOrder(nil)) != 0 {
		t.Fatal("no sessions")
	}
}

func TestImportWindow(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		now      time.Time
		loc      *time.Location
		from, to string
	}{
		{"UTC", time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), time.UTC, "2025-09-22", "2026-10-26"},
		{"local day after UTC midnight", time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC), berlin, "2025-09-22", "2026-10-26"},
		{"the night DST ends", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), berlin, "2025-09-21", "2026-10-25"},
		{"the night DST starts", time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC), berlin, "2025-02-23", "2026-03-29"},
		{"a leap day in the window", time.Date(2028, 3, 1, 12, 0, 0, 0, time.UTC), time.UTC, "2027-01-27", "2028-03-01"},
	} {
		from, to := ImportWindow(tc.now, tc.loc)
		if from != tc.from || to != tc.to {
			t.Errorf("%s: %s..%s, want %s..%s", tc.name, from, to, tc.from, tc.to)
		}
		a, _ := time.Parse(time.DateOnly, from)
		b, _ := time.Parse(time.DateOnly, to)
		if n := int(b.Sub(a).Hours()/24) + 1; n != ImportWindowDays {
			t.Errorf("%s: %d days, want %d", tc.name, n, ImportWindowDays)
		}
	}
}

func TestExpectedSeconds(t *testing.T) {
	for _, tc := range []struct{ base, days, want int }{
		{10, 0, 10}, {10, 5, 20}, {40, 2, 44}, {40, 200, 300}, {290, 10, 300}, {400, 3, 400},
	} {
		if got := ExpectedSeconds(tc.base, tc.days); got != tc.want {
			t.Errorf("ExpectedSeconds(%d, %d) = %d, want %d", tc.base, tc.days, got, tc.want)
		}
	}
}

func TestCheckEnroll(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fixed := plan() // 4 weeks, 3 days a week
	open := plan()
	open.Weeks = nil
	for _, tc := range []struct {
		name string
		p    model.Plan
		in   model.EnrollPost
		ok   bool
	}{
		{"nothing", fixed, model.EnrollPost{}, true},
		{"every field", fixed, model.EnrollPost{StartedAt: ptrTo(now.AddDate(0, -2, 0)), CurrentWeek: i(3), CurrentDayIndex: i(2)}, true},
		{"the last week", fixed, model.EnrollPost{CurrentWeek: i(4)}, true},
		{"past the last week", fixed, model.EnrollPost{CurrentWeek: i(5)}, false},
		{"week 0", fixed, model.EnrollPost{CurrentWeek: i(0)}, false},
		{"no weeks: 1000", open, model.EnrollPost{CurrentWeek: i(1000)}, true},
		{"no weeks: 1001", open, model.EnrollPost{CurrentWeek: i(1001)}, false},
		{"day index past days_per_week", fixed, model.EnrollPost{CurrentDayIndex: i(3)}, false},
		{"negative day index", fixed, model.EnrollPost{CurrentDayIndex: i(-1)}, false},
		{"started before 2000", fixed, model.EnrollPost{StartedAt: ptrTo(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))}, false},
		{"started over a day ahead", fixed, model.EnrollPost{StartedAt: ptrTo(now.Add(25 * time.Hour))}, false},
		{"started a day ahead", fixed, model.EnrollPost{StartedAt: ptrTo(now.Add(24 * time.Hour))}, true},
	} {
		err := CheckEnroll(tc.p, tc.in, now)
		var p Problem
		if (err == nil) != tc.ok || (err != nil && !errors.As(err, &p)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
