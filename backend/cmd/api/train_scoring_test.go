package main

import (
	"math"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

// Model 2026.10.2 through the api (docs/SCORING.md, D134, D135).

// D134 on the api's paths: an assisted set moves max(0, body weight × factor + added −
// assist_kg) (volume), holds no record however many reps, and gives no e1RM chart point;
// an unassisted set of the same exercise holds the record. Completing (the load the api
// answers with) and importing (the records after the batch) agree.
func TestAssistedSetsThroughTheAPI(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	// complete logs a pull-up session `back` days ago at 80 kg body weight and completes it.
	complete := func(back int, reps []int, assist *float64) model.Session {
		t.Helper()
		start := daysAgo(back)
		id := uuid.New()
		var sets []any
		for k, r := range reps {
			at := start.Add(time.Duration(5+3*k) * time.Minute)
			sets = append(sets, map[string]any{"id": uuid.NewString(), "set_index": k, "reps": r, "weight_kg": nil, "assist_kg": assist,
				"completed_at": at, "updated_at": at})
		}
		body := map[string]any{"id": id, "title": "Pull", "started_at": start, "status": "in_progress", "bodyweight_kg": 80,
			"exercises": []any{map[string]any{"id": uuid.NewString(), "exercise_id": pullUpID, "position": 0, "notes": "", "sets": sets}}}
		b.want(b.call(token, http.MethodPut, "/v1/sessions/"+id.String(), body), http.StatusCreated, nil)
		var s model.Session
		b.want(b.call(token, http.MethodPost, "/v1/sessions/"+id.String()+"/complete",
			map[string]any{"ended_at": start.Add(time.Hour)}), http.StatusOK, &s)
		return s
	}
	assisted := complete(20, []int{8, 8}, ptrTo(30.0))
	if assisted.VolumeKG == nil || *assisted.VolumeKG != 2*8*(80*1.0-30) || len(assisted.PRs) != 0 ||
		assisted.MuscularTRIMP == nil || *assisted.MuscularTRIMP <= 0 {
		t.Fatalf("assisted: volume %v (want %v: the support off the moved weight), load %v, records %+v",
			assisted.VolumeKG, 2*8*(80*1.0-30), assisted.MuscularTRIMP, assisted.PRs)
	}
	plain := complete(15, []int{5, 5}, nil)
	if *plain.VolumeKG != 2*5*80 || !slices.ContainsFunc(plain.PRs, func(p model.PR) bool { return p.Kind == model.PRMaxReps && p.Value == 5 }) {
		t.Fatalf("unassisted: volume %v, records %+v", *plain.VolumeKG, plain.PRs)
	}
	more := complete(10, []int{12}, ptrTo(30.0))
	overdone := complete(9, []int{4}, ptrTo(120.0)) // more support than the moved weight: 0, not negative
	if len(more.PRs) != 0 || *more.VolumeKG != 12*50 || *overdone.VolumeKG != 0 {
		t.Fatalf("twelve assisted reps take no record: %+v, volume %v; over-supported volume %v", more.PRs, *more.VolumeKG, *overdone.VolumeKG)
	}
	// An imported assisted set takes no record either.
	imp := done(daysAgo(5), pullUpID, 0)
	imp.BodyweightKG = ptrTo(80.0)
	imp.Exercises[0].Sets[0].WeightKG, imp.Exercises[0].Sets[0].AssistKG = nil, ptrTo(20.0)
	imp.Exercises[0].Sets[0].Reps = ptrTo(15)
	b.imports(token, request(nil, imp))

	var hist model.ExerciseHistory
	b.want(b.call(token, http.MethodGet, "/v1/exercises/"+pullUpID.String()+"/history", nil), http.StatusOK, &hist)
	if len(hist.Sessions) != 5 || len(hist.E1RM) != 0 {
		t.Fatalf("history: %d sessions, e1RM points %+v (unloaded bodyweight sets give none)", len(hist.Sessions), hist.E1RM)
	}
	if len(hist.PRs) != 1 || hist.PRs[0].Kind != model.PRMaxReps || hist.PRs[0].Value != 5 || hist.PRs[0].SessionID != plain.ID {
		t.Fatalf("the only record is the unassisted session's: %+v", hist.PRs)
	}
}

// D135: a pct_1rm target is a share of the best e1RM of the last 90 days; a best from
// before them gives no target (target_kg null) rather than 80 % of an old max.
func TestPct1RMTargetUsesTheLast90Days(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	pct := 80.0
	p := model.Plan{ID: uuid.New(), Name: "Percent", Goal: "strength", DaysPerWeek: 1, Days: []model.PlanDay{{ID: uuid.New(), Name: "A",
		Exercises: []model.PlanExercise{{ID: uuid.New(), ExerciseID: squatID, Sets: 5, RepsMin: 5, RepsMax: 5,
			TargetKind: model.TargetPct1RM, TargetValue: &pct, RestSec: 180}}}}}
	b.want(b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), p), http.StatusCreated, nil)
	b.want(b.call(token, http.MethodPost, "/v1/plans/"+p.ID.String()+"/enroll", nil), http.StatusCreated, nil)
	target := func() *float64 {
		t.Helper()
		var today model.PlanToday
		b.want(b.call(token, http.MethodGet, "/v1/plans/today", nil), http.StatusOK, &today)
		if len(today.Targets) != 1 || today.Targets[0].TargetKind != model.TargetPct1RM {
			t.Fatalf("targets: %+v", today.Targets)
		}
		return today.Targets[0].TargetKG
	}
	if kg := target(); kg != nil {
		t.Fatalf("no sets at all: %v", *kg)
	}
	b.imports(token, request(nil, done(daysAgo(120), squatID, 140, 140)))
	if kg := target(); kg != nil {
		t.Fatalf("a best older than 90 days gives no target: %v", *kg)
	}
	b.imports(token, request(nil, done(daysAgo(10), squatID, 100, 100)))
	want := math.Round(pct/100*scoring.E1RM(100, 5)/2.5) * 2.5
	if kg := target(); kg == nil || *kg != want {
		t.Fatalf("80 %% of the last 90 days' best (5 × 100 kg): %v, want %v (not of the old 140 kg)", kg, want)
	}
}
