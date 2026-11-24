package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/wearables"
)

func coachingNumber(v float64) *float64 { return &v }

func TestCoachingReplayAndEvaluationWithAPIRole(t *testing.T) {
	st, root, athlete, other := nutritionTestStore(t)
	ctx := context.Background()
	const policy = "2026-10-26"
	now := time.Date(2026, 11, 24, 19, 30, 0, 0, time.UTC)
	start := now.AddDate(0, 0, -15)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := root.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.GrantConsent(ctx, athlete, policy, nil))
	exec("UPDATE users SET birth_year=1990,tz='UTC' WHERE id=$1", athlete)
	_, err := st.PutN2RProfile(ctx, athlete, model.N2RProfile{Sex: "female", BodyMassKG: 70, ExpectedBedtime: "23:00"})
	check(err)
	race := now.AddDate(0, 0, 2).Format(time.DateOnly)
	goal := model.GoalProfile{Goal: periodization.CyclingRace, HeightCM: 172, BodyFatPct: coachingNumber(20), RaceDate: &race,
		FTP: coachingNumber(245), HRMax: coachingNumber(190), HRRest: coachingNumber(52)}
	if _, err := st.PutCoachingGoal(ctx, other, goal, start); !errors.Is(err, nutrition.ErrInvalid) {
		t.Fatalf("account without adult birth year: %v", err)
	}
	original := goal
	original.Goal = periodization.FatLoss
	original.RaceDate = nil
	_, err = st.PutCoachingGoal(ctx, athlete, original, start)
	check(err)
	_, err = st.PutCoachingGoal(ctx, athlete, goal, start.AddDate(0, 0, 1))
	check(err)
	got, err := st.CoachingGoal(ctx, athlete)
	if err != nil || got == nil || got.Goal != goal.Goal {
		t.Fatalf("persisted goal: %+v %v", got, err)
	}
	if got, err := st.CoachingGoal(ctx, other); err != nil || got != nil {
		t.Fatalf("isolated goal: %+v %v", got, err)
	}

	exec("INSERT INTO nutrition.wearable_links(provider,provider_user,user_id) VALUES('suunto','coaching-fixture',$1)", athlete)
	exerciseStart := now.Add(-2 * time.Hour)
	workout := wearables.Workout{ExternalID: "exercise", Provider: wearables.SuuntoFIT, Sport: "cycling", Start: exerciseStart,
		End: now.Add(-20 * time.Minute), WorkKJ: 710, AvgHR: 158, FTP: 245, NormalizedPower: 210,
		HasRR: true, SuuntoPlus: map[string]float64{"running_power": 190}}
	delivery := wearables.Delivery{User: "coaching-fixture", RemoteID: "exercise",
		Workouts: []wearables.Workout{workout, {}},
		Nights: []wearables.Overnight{{}, {WakeAt: now.Add(-12 * time.Hour), RMSSD: coachingNumber(34), RHR: coachingNumber(56),
			Deep: coachingNumber(75), REM: coachingNumber(90)}},
		Weights:  []wearables.WeighIn{{At: now.Add(-10 * time.Hour), Mass: 70.4}, {At: now, Mass: 1}},
		Activity: []wearables.ActivitySample{{At: now.Add(-time.Hour), Kcal: 14}, {At: now.Add(time.Hour), Kcal: 30}}}
	check(st.EnqueueWearable(ctx, "suunto", policy, []wearables.Delivery{delivery, delivery}))
	exec("UPDATE nutrition.wearable_inbox SET due_at=$1", now.Add(-time.Minute))
	job, err := st.ClaimWearable(ctx, now, policy)
	if err != nil || job == nil || job.User != athlete || job.Delivery.RemoteID != delivery.RemoteID {
		t.Fatalf("claimed wearable: %+v %v", job, err)
	}
	if other, err := st.ClaimWearable(ctx, now, policy); err != nil || other != nil {
		t.Fatalf("duplicate job: %+v %v", other, err)
	}
	check(st.FinishWearable(ctx, *job, now, false))
	if early, err := st.ClaimWearable(ctx, now, policy); err != nil || early != nil {
		t.Fatalf("retry was due immediately: %+v %v", early, err)
	}
	retryAt := now.Add(16 * time.Minute)
	job, err = st.ClaimWearable(ctx, retryAt, policy)
	if err != nil || job == nil || job.Attempt != 2 {
		t.Fatalf("retry: %+v %v", job, err)
	}
	check(st.ApplyWearable(ctx, *job, job.Delivery, policy, retryAt))
	check(st.FinishWearable(ctx, *job, retryAt, true))
	check(st.PurgeWearableInbox(ctx, retryAt))
	if n := countRows(t, root, "SELECT count(*) FROM nutrition.canonical_workouts WHERE user_id=$1 AND rr_intervals_available", athlete); n != 1 {
		t.Fatalf("canonical workouts: %d", n)
	}
	if n := countRows(t, root, "SELECT count(*) FROM nutrition.wearable_activity WHERE user_id=$1", athlete); n != 1 {
		t.Fatalf("validated activity: %d", n)
	}

	for day := 0; day <= 15; day++ {
		at := start.AddDate(0, 0, day)
		exec(`INSERT INTO nutrition.wearable_weights(user_id,provider,at_time,mass_kg) VALUES($1,'suunto',$2,$3)`, athlete, at.Add(-5*time.Hour), 70+float64(day)*.02)
		meal, _, err := st.CreateNutritionMeal(ctx, athlete, model.NutritionMealInput{
			ClientRequestID: uuid.New(), ConsumedAt: at.Add(-6 * time.Hour), Timezone: "UTC", MealType: "lunch", InputMethod: "manual_search",
			Items: []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: 200, Unit: "g"}},
		})
		check(err)
		exec(`UPDATE nutrition.meal_items SET nutrients='{"energy_kcal":{"amount":2500},"carbohydrates":{"amount":300},"protein":{"amount":130},"fat":{"amount":85}}'
			WHERE meal_id=$1`, meal.ID)
		feedback := model.CoachingFeedback{IntakeComplete: true, TrackedCycles: 2, CycleWaterKG: coachingNumber(.2)}
		if day == 15 {
			feedback.PreMass, feedback.PostMass = coachingNumber(71), coachingNumber(70)
			feedback.WorkoutMinutes, feedback.SweatPatch = coachingNumber(100), coachingNumber(850)
			feedback.RPE, feedback.GIDistress, feedback.IntraCarbs = coachingNumber(7), coachingNumber(2), coachingNumber(50)
			feedback.MeasuredRMR = coachingNumber(1500)
		}
		_, err = st.PutCoachingFeedback(ctx, athlete, at.Format(time.DateOnly), feedback, now)
		check(err)
	}
	if _, err := st.PutCoachingFeedback(ctx, other, now.Format(time.DateOnly), model.CoachingFeedback{}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("feedback without enrollment: %v", err)
	}
	if _, err := st.PutCoachingFeedback(ctx, athlete, "invalid", model.CoachingFeedback{}, now); !errors.Is(err, nutrition.ErrInvalid) {
		t.Fatalf("invalid feedback date: %v", err)
	}
	briefing, err := st.CoachingBriefing(ctx, athlete, now)
	if err != nil || briefing.Plan == nil || briefing.Plan.Fuel == nil || briefing.Plan.NetExercise <= 0 ||
		briefing.N2R == nil || briefing.N2R.Budget.EnergyTarget != briefing.Plan.EnergyKcal || briefing.Yesterday == nil {
		t.Fatalf("briefing: %+v %v", briefing, err)
	}
	if !slices.ContainsFunc(briefing.Cards, func(c model.N2RGuidanceCard) bool { return c.Code == "EVENING_FUEL" }) {
		t.Fatalf("evening guidance: %+v", briefing.Cards)
	}
	enabled := func(id uuid.UUID) bool { return id == athlete }
	check(st.RefreshCoaching(ctx, now, policy, enabled))
	tomorrow := now.AddDate(0, 0, 1)
	_, err = st.PutCoachingFeedback(ctx, athlete, tomorrow.Format(time.DateOnly), model.CoachingFeedback{
		Session: &model.PlannedSession{Start: tomorrow.Add(2 * time.Hour), Sport: "cycling", Minutes: 100, Class: periodization.Moderate},
	}, now)
	check(err)
	check(st.RefreshCoaching(ctx, tomorrow, policy, enabled))
	gotBriefing, err := st.CoachingBriefing(ctx, athlete, tomorrow)
	if err != nil || !slices.ContainsFunc(gotBriefing.Cards, func(c model.N2RGuidanceCard) bool { return c.Code == "PLANNED_SESSION_FUEL" }) {
		t.Fatalf("planned fueling guidance: %+v %v", gotBriefing.Cards, err)
	}
	if n := countRows(t, root, `SELECT count(*) FROM nutrition.model_evaluation_logs
		WHERE user_id=$1 AND workout_source='SUUNTO_FIT' AND adherence?'gi_distress' AND sws_min IS NULL`, athlete); n != 1 {
		t.Fatalf("evaluation with adherence and unknown next-morning stages: %d", n)
	}
	if cohorts, err := st.CoachingCohorts(ctx, start.Format(time.DateOnly)); err != nil || len(cohorts) != 0 {
		t.Fatalf("cohort privacy threshold: %+v %v", cohorts, err)
	}
	check(st.DeleteCoachingGoal(ctx, athlete))
	if n := countRows(t, root, "SELECT count(*) FROM nutrition.model_evaluation_logs WHERE user_id=$1", athlete); n != 0 {
		t.Fatalf("opt-out evaluation rows: %d", n)
	}
}
