package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

func TestDayMuscleFreshness(t *testing.T) {
	rig := newRecoveryRig(t)
	user := rig.user()
	ctx := context.Background()
	if _, err := rig.pool.Exec(ctx, `UPDATE users SET tz='Europe/Berlin' WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}

	const pastDay = "2026-03-29"
	var initial map[string]json.RawMessage
	rig.call(user, http.MethodGet, "/v1/days/"+pastDay, nil, http.StatusOK, &initial)
	if got := string(initial["muscles"]); got != "null" {
		t.Fatalf("muscles before any completed session = %q, want null", got)
	}

	// March 29 is 23 hours long in Berlin. The next local midnight is still March 29 in UTC.
	end := time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC)
	last := end.Add(-time.Second)
	sets := make([]model.Set, 0, 3)
	for i, at := range []time.Time{last, end, end.Add(time.Hour)} {
		sets = append(sets, model.Set{ID: uuid.New(), SetIndex: i, CompletedAt: &at, UpdatedAt: at})
	}
	session := model.Session{
		ID: uuid.New(), Status: model.SessionInProgress, StartedAt: end.Add(-time.Hour),
		Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: train.BuiltinExerciseID("bench-press"), Sets: sets}},
	}
	if _, err := rig.st.SaveSession(ctx, user, session); err != nil {
		t.Fatal(err)
	}
	if err := rig.st.CompleteSession(ctx, user, session.ID, store.Completion{EndedAt: end.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	for _, day := range []string{"2026-03-28", pastDay} {
		t.Run(day, func(t *testing.T) {
			var detail model.DayDetail
			rig.call(user, http.MethodGet, "/v1/days/"+day, nil, http.StatusOK, &detail)
			if !detail.Score.ComputedAt.IsZero() {
				t.Fatalf("expected a placeholder score, got computed_at %v", detail.Score.ComputedAt)
			}
			if len(detail.Muscles) != 18 {
				t.Fatalf("got %d muscles after session completion, want 18", len(detail.Muscles))
			}
			for i, muscle := range model.Muscles {
				got := detail.Muscles[i]
				if got.Muscle != muscle || got.State != "fresh" {
					t.Errorf("entry %d = %+v, want %s in fresh state", i, got, muscle)
				}
				if day == pastDay && (muscle == "chest" || muscle == "triceps" || muscle == "front_delts") {
					if got.Freshness != 91 || got.Sets7d != 1 || got.LastTrainedAt == nil || !got.LastTrainedAt.Equal(last) {
						t.Errorf("%s = %+v, want freshness 91 and one set ending at %v", muscle, got, last)
					}
				} else if got.Freshness != 100 || got.Sets7d != 0 || got.LastTrainedAt != nil {
					t.Errorf("%s = %+v, want freshness 100 and no training history", muscle, got)
				}
			}
		})
	}

	// Today is evaluated at the request time (the api clock): a set 1 s before it counts, one 1 s after does not.
	rig.now = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	today := rig.now.In(time.FixedZone("CET", 3600)).Format(dayLayout)
	before, after := rig.now.Add(-time.Second), rig.now.Add(time.Second)
	todaySession := model.Session{
		ID: uuid.New(), Status: model.SessionInProgress, StartedAt: rig.now.Add(-time.Hour),
		Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: train.BuiltinExerciseID("bench-press"), Sets: []model.Set{
			{ID: uuid.New(), SetIndex: 0, CompletedAt: &before, UpdatedAt: before},
			{ID: uuid.New(), SetIndex: 1, CompletedAt: &after, UpdatedAt: after},
		}}},
	}
	if _, err := rig.st.SaveSession(ctx, user, todaySession); err != nil {
		t.Fatal(err)
	}
	if err := rig.st.CompleteSession(ctx, user, todaySession.ID, store.Completion{EndedAt: after}); err != nil {
		t.Fatal(err)
	}
	var detail model.DayDetail
	rig.call(user, http.MethodGet, "/v1/days/"+today, nil, http.StatusOK, &detail)
	if len(detail.Muscles) != 18 {
		t.Fatalf("today: got %d muscles, want 18", len(detail.Muscles))
	}
	chest := detail.Muscles[0]
	if chest.Muscle != "chest" || chest.Freshness != 91 || chest.Sets7d != 1 || chest.LastTrainedAt == nil || !chest.LastTrainedAt.Equal(before) {
		t.Errorf("today's chest = %+v, want freshness 91, one set, last trained at %v", chest, before)
	}
}
