package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// The training import's changes to the routes beside it (design §9, §10, T3, T4, T8, T11, T12).

// ---- PUT /v1/sessions/{id}: assist_kg and origin (§9) -------------------------------------

func TestPutSessionAssistAndOrigin(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	start := daysAgo(1)
	live := func(ex uuid.UUID, weight, assist *float64) map[string]any {
		at := start.Add(10 * time.Minute)
		return map[string]any{"id": uuid.NewString(), "title": "Pull", "started_at": start, "status": "in_progress",
			"origin": "import", "import_source": "alpha_progression",
			"exercises": []any{map[string]any{"id": uuid.NewString(), "exercise_id": ex, "position": 0, "notes": "",
				"sets": []any{map[string]any{"id": uuid.NewString(), "set_index": 0, "reps": 6, "weight_kg": weight,
					"assist_kg": assist, "completed_at": at, "updated_at": at}}}}}
	}
	for _, tc := range []struct {
		name   string
		body   map[string]any
		status int
		msg    string
	}{
		{"assist_kg on a weight exercise", live(squatID, ptrTo(100.0), ptrTo(10.0)), http.StatusUnprocessableEntity, "assist_kg is only for bodyweight"},
		{"assist_kg with an added load", live(pullUpID, ptrTo(10.0), ptrTo(20.0)), http.StatusUnprocessableEntity, "cannot both be above 0"},
	} {
		rec := b.call(token, http.MethodPut, "/v1/sessions/"+tc.body["id"].(string), tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	// A live session with origin and import_source in the body: accepted, ignored (an app
	// echoing a Session back is not refused), stored as the app's; assist_kg clamped to 200.
	body := live(pullUpID, nil, ptrTo(250.0))
	b.want(b.call(token, http.MethodPut, "/v1/sessions/"+body["id"].(string), body), http.StatusCreated, nil)
	var s model.Session
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+body["id"].(string), nil), http.StatusOK, &s)
	if s.Origin != model.OriginApp || s.ImportSource != nil || *s.Exercises[0].Sets[0].AssistKG != 200 || s.Exercises[0].Sets[0].WeightKG != nil {
		t.Fatalf("stored: origin %q source %v assist %v", s.Origin, s.ImportSource, *s.Exercises[0].Sets[0].AssistKG)
	}

	// An imported session saved with PUT (an edit in the app) keeps origin import whatever
	// the body says; an import of the app's session id is id_taken.
	imp := done(daysAgo(3), pullUpID, 0)
	imp.Exercises[0].Sets[0].WeightKG, imp.Exercises[0].Sets[0].AssistKG = nil, ptrTo(15.0)
	b.imports(token, request(nil, imp))
	var stored map[string]any
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+imp.ID.String(), nil), http.StatusOK, &stored)
	stored["origin"], stored["import_source"], stored["title"] = "app", nil, "Pull (edited)"
	b.want(b.call(token, http.MethodPut, "/v1/sessions/"+imp.ID.String(), stored), http.StatusOK, &s)
	if s.Origin != model.OriginImport || s.ImportSource == nil || *s.ImportSource != "alpha_progression" || s.Title != "Pull (edited)" ||
		*s.Exercises[0].Sets[0].AssistKG != 15 {
		t.Fatalf("an edited imported session: origin %q source %v title %q", s.Origin, s.ImportSource, s.Title)
	}
	appSession := done(daysAgo(1), pullUpID, 0)
	appSession.ID = uuid.MustParse(body["id"].(string))
	if res := b.imports(token, request(nil, appSession)); statuses(res)[0] != "session rejected id_taken" {
		t.Fatalf("the app's session id: %v", statuses(res))
	}
	if n := b.count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND origin='app'`, user); n != 1 {
		t.Fatalf("the app's session is the app's: %d", n)
	}
}

// ---- PUT /v1/plans/{id}: the bodyweight target (T11) ---------------------------------------

func TestPutPlanBodyweightTarget(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	p := twoDayPlan()
	p.Days[0].Exercises[0].ExerciseID, p.Days[0].Exercises[0].TargetKind = pullUpID, model.TargetBodyweight
	p.Days[0].Exercises[0].TargetValue = ptrTo(600.0)
	rec := b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), p)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "bodyweight needs target_value 0–500") {
		t.Fatalf("600 kg added: %d %s", rec.Code, rec.Body)
	}
	for _, v := range []*float64{nil, ptrTo(0.0), ptrTo(20.0)} {
		p.Days[0].Exercises[0].TargetValue = v
		if rec := b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), p); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("target_value %v: %d %s", v, rec.Code, rec.Body)
		}
	}
	// The import applies the same rule: invalid, the request goes on.
	p.ID, p.Days[0].ID, p.Days[1].ID = uuid.New(), uuid.New(), uuid.New()
	p.Days[0].Exercises[0].ID, p.Days[1].Exercises[0].ID = uuid.New(), uuid.New()
	p.Days[0].Exercises[0].TargetValue = ptrTo(-5.0)
	if res := b.imports(token, request([]model.Plan{p})); statuses(res)[0] != "plan rejected invalid" {
		t.Fatalf("import: %v", statuses(res))
	}
}

// ---- POST /v1/exercises un-archives (T12) ----------------------------------------------------

func TestCreateExerciseUnarchives(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	id := uuid.NewString()
	body := map[string]any{"id": id, "name": "Band pull-up", "kind": "bodyweight", "primary_muscles": []string{"lats"}, "load_type": "bodyweight"}
	b.want(b.call(token, http.MethodPost, "/v1/exercises", body), http.StatusCreated, nil)
	b.want(b.call(token, http.MethodDelete, "/v1/exercises/"+id, nil), http.StatusNoContent, nil)
	var ex model.Exercise
	b.want(b.call(token, http.MethodPost, "/v1/exercises", body), http.StatusOK, &ex)
	if ex.Archived || ex.ID.String() != id {
		t.Fatalf("sent again, an archived custom exercise is un-archived: %+v", ex)
	}
}

// ---- POST /v1/plans/{id}/enroll with a position (T8) ------------------------------------------

func TestEnrollWithAPosition(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	repeat := twoDayPlan() // no weeks, days 0 and 1
	week := func(n int) *int { return &n }
	fixed := twoDayPlan() // two weeks; week 2 has only day 0
	fixed.Weeks = week(2)
	fixed.Days[0].Week, fixed.Days[1].Week = week(1), week(1)
	tv := 100.0
	fixed.Days = append(fixed.Days, model.PlanDay{ID: uuid.New(), Week: week(2), DayIndex: 0, Name: "W2", Exercises: []model.PlanExercise{
		{ID: uuid.New(), ExerciseID: squatID, Sets: 3, RepsMin: 5, RepsMax: 5, TargetKind: model.TargetKG, TargetValue: &tv}}})
	for _, p := range []model.Plan{repeat, fixed} {
		b.want(b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), p), http.StatusCreated, nil)
	}
	enroll := func(p model.Plan, body any, status int) model.Enrollment {
		t.Helper()
		var e model.Enrollment
		rec := b.call(token, http.MethodPost, "/v1/plans/"+p.ID.String()+"/enroll", body)
		if rec.Code != status {
			t.Fatalf("enroll %v: %d %s, want %d", body, rec.Code, rec.Body, status)
		}
		if status < 300 {
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
		}
		return e
	}
	active := func() *model.Enrollment {
		e, err := b.st.ActiveEnrollment(context.Background(), userIDOf(token))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	// No body, an empty one, {} and nulls: the behaviour before T8.
	e0 := enroll(repeat, nil, http.StatusCreated)
	if e0.CurrentWeek != 1 || e0.CurrentDayIndex != 0 || e0.Status != model.EnrollmentActive {
		t.Fatalf("new: %+v", e0)
	}
	for _, body := range []any{"", "{}", `{"current_week":null,"current_day_index":null,"started_at":null}`} {
		if e := enroll(repeat, body, http.StatusOK); !sameEnrollment(e, e0) {
			t.Fatalf("%q returns the active enrolment as is: %+v", body, e)
		}
	}
	// A field set: the active enrolment moves in place.
	e1 := enroll(repeat, map[string]any{"current_day_index": 1}, http.StatusOK)
	if e1.ID != e0.ID || e1.CurrentDayIndex != 1 || e1.CurrentWeek != 1 || !e1.StartedAt.Equal(e0.StartedAt) {
		t.Fatalf("moved: %+v", e1)
	}
	began := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	e2 := enroll(repeat, map[string]any{"current_week": 7, "started_at": began}, http.StatusOK)
	if e2.ID != e0.ID || e2.CurrentWeek != 7 || e2.CurrentDayIndex != 1 || !e2.StartedAt.Equal(began) {
		t.Fatalf("a plan without weeks takes any week, the index stays: %+v", e2)
	}
	for _, body := range []any{
		map[string]any{"current_day_index": 2}, // days_per_week 2
		map[string]any{"current_week": 0},      // 1-based
		map[string]any{"current_week": 1001},   // 1–1000 without weeks
		map[string]any{"started_at": "1999-12-31T00:00:00Z"},
		map[string]any{"started_at": time.Now().Add(48 * time.Hour)},
	} {
		enroll(repeat, body, http.StatusUnprocessableEntity)
	}
	enroll(repeat, `{"current_week":2,"week":2}`, http.StatusBadRequest)
	if e := active(); e == nil || !sameEnrollment(*e, e2) {
		t.Fatalf("a refused body changes nothing: %+v", e)
	}

	// Another plan: no day at week 2, index 1 → 422 and the active one is not paused.
	enroll(fixed, map[string]any{"current_week": 2, "current_day_index": 1}, http.StatusUnprocessableEntity)
	enroll(fixed, map[string]any{"current_week": 3}, http.StatusUnprocessableEntity) // weeks 2
	if e := active(); e == nil || e.ID != e0.ID {
		t.Fatalf("the refused position paused the active plan: %+v", e)
	}
	// A new enrolment at a position: 201, the other one paused.
	f := enroll(fixed, map[string]any{"current_week": 2}, http.StatusCreated)
	if f.CurrentWeek != 2 || f.CurrentDayIndex != 0 || f.PlanID != fixed.ID {
		t.Fatalf("new at week 2: %+v", f)
	}
	var today model.PlanToday
	b.want(b.call(token, http.MethodGet, "/v1/plans/today", nil), http.StatusOK, &today)
	if today.Day.Name != "W2" {
		t.Fatalf("today is week 2's day: %+v", today.Day)
	}
	// The paused enrolment of the first plan resumes and moves in place.
	r := enroll(repeat, map[string]any{"current_day_index": 0}, http.StatusOK)
	if r.ID != e0.ID || r.Status != model.EnrollmentActive || r.CurrentDayIndex != 0 || r.CurrentWeek != 7 {
		t.Fatalf("resumed and moved: %+v", r)
	}
	if e := active(); e == nil || e.ID != e0.ID {
		t.Fatalf("active: %+v", e)
	}
	enroll(model.Plan{ID: uuid.New()}, map[string]any{"current_week": 1}, http.StatusNotFound)
	enroll(model.Plan{ID: uuid.New()}, nil, http.StatusNotFound)
}

// sameEnrollment compares enrolments as values (the times as instants).
func sameEnrollment(a, b model.Enrollment) bool {
	return a.ID == b.ID && a.PlanID == b.PlanID && a.StartedAt.Equal(b.StartedAt) && a.CurrentWeek == b.CurrentWeek &&
		a.CurrentDayIndex == b.CurrentDayIndex && a.Status == b.Status
}

func userIDOf(token string) uuid.UUID {
	_, id, _ := strings.Cut(token, ":")
	return uuid.MustParse(id)
}
