package main

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// The import's dependent days (D135): store.DependentDays is scoring.DependentScope.Covers
// in SQL. On a fixture with a session on each side of every clause it returns exactly the
// days of the sessions Covers names (and the changed sessions' own), never fewer.
func TestDependentDaysIsDependentScopeCovers(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	ctx := context.Background()
	deadlift := train.BuiltinExerciseID("deadlift")
	customBW := uuid.New() // the user's own bodyweight exercise
	b.want(b.call(token, http.MethodPost, "/v1/exercises", map[string]any{"id": customBW, "name": "Ring row", "kind": "bodyweight",
		"primary_muscles": []string{"upper_back"}, "load_type": "bodyweight"}), http.StatusCreated, nil)
	from, to := train.ImportWindow(time.Now(), time.UTC)
	window := store.DayWindow{From: from, To: to}
	put := func(s model.ImportSession, bw *float64) model.Session {
		t.Helper()
		x := s.ToSession()
		x.BodyweightKG = bw
		if out, err := b.st.ImportSession(ctx, user, model.ImportSourceAlphaProgression, x, time.UTC, window); err != nil || out.Status != store.ImportCreated {
			t.Fatalf("fixture: %+v %v", out, err)
		}
		stored, err := b.st.Session(ctx, user, x.ID)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	bw := func(kg float64) *float64 { return &kg }
	// The changed sessions: squat with a body weight 300 days back, bench without one 250 back.
	// Since = -300, Through = -250 + 1 h + 90 days (about -160), BodyweightSince = -300.
	changed := []model.Session{put(done(daysAgo(300), squatID, 100), bw(80)), put(done(daysAgo(250), benchID, 60), nil)}
	others := map[string]model.Session{
		"squat before Through":                  put(done(daysAgo(200), squatID, 105), bw(75)),
		"squat after Through":                   put(done(daysAgo(100), squatID, 110), bw(75)),
		"pull-up without a body weight":         put(done(daysAgo(120), pullUpID, 0), nil),
		"pull-up with its own body weight":      put(done(daysAgo(90), pullUpID, 0), bw(70)),
		"own bodyweight exercise, no weight":    put(done(daysAgo(80), customBW, 0), nil),
		"deadlift without a body weight":        put(done(daysAgo(70), deadlift, 140), nil),
		"bench before the oldest changed one":   put(done(daysAgo(310), benchID, 55), nil),
		"bench between the changed ones":        put(done(daysAgo(255), benchID, 58), bw(78)),
		"pull-up long after, no body weight":    put(done(daysAgo(20), pullUpID, 0), nil),
		"deadlift and squat, after Through":     put(done(daysAgo(60), deadlift, 150), bw(76)),
		"squat on the day of the newest change": put(done(daysAgo(250).Add(2*time.Hour), squatID, 102), nil),
	}
	// A squat session still in progress is no completed session: never a dependent.
	live := done(daysAgo(150), squatID, 100).ToSession()
	live.Status, live.EndedAt = model.SessionInProgress, nil
	if _, err := b.st.SaveSession(ctx, user, live); err != nil {
		t.Fatal(err)
	}

	scope, ok := scoring.ScopeOf(changed)
	if !ok {
		t.Fatal("no scope")
	}
	got, err := b.st.DependentDays(ctx, user, store.DependentScope{Since: scope.Since, Exercises: scope.Exercises,
		Through: scope.Through, BodyweightSince: scope.BodyweightSince}, window, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, s := range others {
		for _, e := range s.Exercises {
			ids = append(ids, e.ExerciseID)
		}
	}
	exercises, err := b.st.VisibleExercises(ctx, user, ids)
	if err != nil {
		t.Fatal(err)
	}
	ownDays := []string{day(changed[0].StartedAt), day(changed[1].StartedAt)}
	covered := 0
	for name, s := range others {
		in := slices.Contains(got, day(s.StartedAt))
		if scope.Covers(s, exercises) {
			covered++
			if !in {
				t.Errorf("%s: Covers it, DependentDays misses %s", name, day(s.StartedAt))
			}
		} else if in && !slices.Contains(ownDays, day(s.StartedAt)) {
			t.Errorf("%s: not covered, but DependentDays returns %s", name, day(s.StartedAt))
		}
	}
	if slices.Contains(got, day(live.StartedAt)) {
		t.Error("the session in progress is a dependent")
	}
	// The fixture exercises every clause: what Covers says, by hand.
	for name, want := range map[string]bool{
		"squat before Through": true, "squat after Through": false, "pull-up without a body weight": true,
		"pull-up with its own body weight": false, "own bodyweight exercise, no weight": true,
		"deadlift without a body weight": false, "bench before the oldest changed one": false,
		"bench between the changed ones": true, "pull-up long after, no body weight": true,
		"deadlift and squat, after Through": false, "squat on the day of the newest change": true,
	} {
		if scope.Covers(others[name], exercises) != want {
			t.Errorf("%s: Covers = %v, want %v", name, !want, want)
		}
	}
	if covered != 6 {
		t.Fatalf("%d covered, want 6", covered)
	}
}

// The finish passes the scope (follow-up of D135): an imported session with a body weight
// queues a later pull-up session without one, and a later squat session only within 90
// days after the import's newest session.
func TestTrainImportQueuesTheDependentScope(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	near, far := done(daysAgo(150), squatID, 120), done(daysAgo(10), squatID, 125)
	pull := done(daysAgo(20), pullUpID, 0)
	b.scored(user, b.imports(token, request(nil, near, far, pull)).DaysTouched...)

	x := done(daysAgo(200), squatID, 100)
	x.BodyweightKG = ptrTo(82.0)
	res := b.imports(token, request(nil, x))
	if want := []string{day(daysAgo(200)), day(daysAgo(150)), day(daysAgo(20))}; !slices.Equal(res.DaysTouched, want) {
		t.Fatalf("days %v, want the import's, the squat within 90 days and the pull-up without a body weight %v", res.DaysTouched, want)
	}
	b.scored(user, res.DaysTouched...)
	// An update that moves the session: the replaced version (200 days back) still bounds the scope.
	moved := x
	moved.StartedAt, moved.BodyweightKG = daysAgo(40), nil
	end := moved.StartedAt.Add(time.Hour)
	moved.EndedAt = &end
	for k := range moved.Exercises[0].Sets {
		at := moved.StartedAt.Add(time.Duration(10+3*k) * time.Minute)
		moved.Exercises[0].Sets[k].CompletedAt, moved.Exercises[0].Sets[k].UpdatedAt = &at, at
	}
	res = b.imports(token, request(nil, moved))
	if want := []string{day(daysAgo(200)), day(daysAgo(150)), day(daysAgo(40)), day(daysAgo(20)), day(daysAgo(10))}; !slices.Equal(res.DaysTouched, want) {
		t.Fatalf("moved: %v, want %v", res.DaysTouched, want)
	}
}
