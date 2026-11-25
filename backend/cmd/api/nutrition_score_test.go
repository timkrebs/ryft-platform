package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestNutritionScoreAPIWithAccountScopedCGM(t *testing.T) {
	rig := newNutritionRig(t)
	ctx := context.Background()
	a, b, noConsent := rig.account(), rig.account(), rig.account(true)
	if _, _, err := rig.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagNutrition, Enabled: true,
		AllowUserIDs: []uuid.UUID{a, b, noConsent}}); err != nil {
		t.Fatal(err)
	}

	check := func(user uuid.UUID, method, path string, payload any, want int, out any) {
		t.Helper()
		response := rig.do(user, method, nutritionPrefix+path, payload)
		if response.Code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, response.Code, want, response.Body)
		}
		if out != nil {
			if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	at := time.Date(2026, 11, 24, 21, 30, 0, 0, time.UTC)
	reading := func(offset time.Duration, v float64) model.NutritionGlucoseSample {
		return model.NutritionGlucoseSample{Timestamp: at.Add(offset), GlucoseMgDL: v}
	}
	upload := model.NutritionGlucoseUpload{Samples: []model.NutritionGlucoseSample{
		reading(0, 100), reading(30*time.Minute, 100), reading(time.Hour, 100),
	}}
	check(uuid.Nil, http.MethodPut, "/glucose", upload, http.StatusUnauthorized, nil)
	check(noConsent, http.MethodPut, "/glucose", upload, http.StatusForbidden, nil)
	check(a, http.MethodPut, "/glucose",
		model.NutritionGlucoseUpload{Samples: []model.NutritionGlucoseSample{reading(0, 100), reading(0, 120)}},
		http.StatusUnprocessableEntity, nil)
	check(a, http.MethodPut, "/glucose", upload, http.StatusNoContent, nil)
	check(a, http.MethodPut, "/glucose", upload, http.StatusNoContent, nil) // cached resend

	custom := map[string]any{
		"client_request_id": uuid.New(), "name": "Quality fixture",
		"quality_profile": map[string]any{
			"non_starchy_vegetable_fraction": .3, "whole_fruit_fraction": .2,
			"nuts_legumes_fraction": .03, "red_processed_meat_fraction": 0, "sugary_drink": false,
		},
	}
	values := map[string]struct {
		amount float64
		unit   string
	}{
		"energy_kcal": {90, "kcal"}, "fiber": {3, "g"}, "epa": {.01, "g"}, "dha": {.015, "g"},
		"monounsaturated_fat": {.6, "g"}, "polyunsaturated_fat": {.4, "g"}, "added_sugar": {1, "g"},
		"sodium": {150, "mg"}, "trans_fat": {.05, "g"},
	}
	nutrients := make([]map[string]any, 0, len(values))
	for code, v := range values {
		nutrients = append(nutrients, map[string]any{
			"code": code, "amount": v.amount, "unit": v.unit, "reference_amount": 100, "reference_unit": "g",
		})
	}
	custom["nutrients"] = nutrients
	var food model.FoodDetail
	check(a, http.MethodPost, "/foods/custom", custom, http.StatusOK, &food)
	if food.QualityProfile == nil || food.QualityProfile.SugaryDrink == nil || *food.QualityProfile.SugaryDrink {
		t.Fatalf("profile missing or inconsistent: %+v", food.QualityProfile)
	}
	in := model.NutritionMealInput{ClientRequestID: uuid.New(), ConsumedAt: at, Timezone: "UTC",
		MealType: "dinner", InputMethod: "manual_entry",
		Items: []model.NutritionMealItemInput{{FoodID: food.ID, Quantity: 1000, Unit: "g"}}}
	var meal model.NutritionMeal
	check(a, http.MethodPost, "/meals", in, http.StatusCreated, &meal)
	if meal.Items[0].QualityProfile == nil {
		t.Fatal("quality profile not snapshotted")
	}
	// A catalog edit after logging must not change the previous meal.
	if _, err := rig.pool.Exec(ctx, `DELETE FROM nutrition.classifications WHERE food_id=$1`, food.ID); err != nil {
		t.Fatal(err)
	}

	day := "/daily?date=2026-11-24"
	var out model.NutritionDaily
	check(a, http.MethodGet, day, nil, http.StatusOK, &out)
	if out.QualityScore != nil || out.NutritionScore != nil || out.GlucoseScore == nil || *out.GlucoseScore != 100 {
		t.Fatalf("missing goal or valid CGM: %+v", out.NutritionScores)
	}
	check(a, http.MethodPut, "/goals", model.NutritionGoalInput{Targets: map[string]float64{"energy_kcal": 3000}}, http.StatusOK, nil)
	check(b, http.MethodDelete, "/glucose", nil, http.StatusNoContent, nil)
	check(a, http.MethodGet, day+"&chrononutrition=true", nil, http.StatusOK, &out)
	if out.QualityScore == nil || *out.QualityScore != 100 || out.GlucoseScore == nil || *out.GlucoseScore != 100 ||
		out.NutritionScore == nil || *out.NutritionScore != 95 || len(out.MealGlucoseScores) != 1 ||
		out.MealGlucoseScores[0].MealID != meal.ID || out.ChrononutritionPenalty != 5 {
		t.Fatalf("score: %+v", out.NutritionScores)
	}
	check(b, http.MethodGet, day, nil, http.StatusOK, &out)
	if out.GlucoseScore != nil || out.QualityScore != nil || out.NutritionScore != nil || len(out.MealGlucoseScores) != 0 {
		t.Fatalf("scores leaked: %+v", out)
	}
	// The following wake day's sleep start is the bedtime for this meal day.
	if _, err := rig.pool.Exec(ctx, `INSERT INTO daily_scores(user_id,day,sleep_start,sleep_actual_min)
		VALUES($1,'2026-11-25',$2,360)`, a, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(a, http.MethodGet, day+"&chrononutrition=true", nil, http.StatusOK, &out)
	if out.NutritionScore == nil || *out.NutritionScore != 100 || out.ChrononutritionPenalty != 0 {
		t.Fatalf("recorded bedtime: %+v", out.NutritionScores)
	}
	check(a, http.MethodPut, "/n2r/profile", model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "23:00"}, http.StatusOK, nil)
	check(a, http.MethodGet, day, nil, http.StatusOK, &out)
	if out.N2R == nil {
		t.Fatal("current consent should allow opted-in contextual scoring")
	}

	response := rig.do(a, http.MethodDelete, "/v1/me/consent", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("withdraw consent: %d", response.Code)
	}
	assertUnscored := func(result model.NutritionDaily) {
		t.Helper()
		if result.ScoreVersion == "" || result.NutritionScore != nil || result.QualityScore != nil || result.GlucoseScore != nil ||
			result.Breakdown != (model.NutritionQualityBreakdown{}) || result.MealGlucoseScores == nil ||
			len(result.MealGlucoseScores) != 0 || result.ChrononutritionPenalty != 0 || result.N2R != nil {
			t.Fatalf("fresh score without consent: %+v", result)
		}
	}
	out = model.NutritionDaily{} // n2r is omitted when unavailable.
	check(a, http.MethodGet, day+"&chrononutrition=true", nil, http.StatusOK, &out)
	assertUnscored(out)
	if out.Meals != 1 || out.Items != 1 || out.Nutrients["energy_kcal"].KnownSubtotal != 900 {
		t.Fatalf("withdrawal hid meal totals: %+v", out)
	}
	const summary = "/summary?from=2026-11-24&to=2026-11-25"
	var result []model.NutritionDaily
	check(a, http.MethodGet, summary+"&chrononutrition=true", nil, http.StatusOK, &result)
	if len(result) != 2 || result[0].Meals != 1 || result[1].Meals != 0 {
		t.Fatalf("withdrawal hid range totals: %+v", result)
	}
	for _, daily := range result {
		assertUnscored(daily)
	}
	var kept model.NutritionMeal
	check(a, http.MethodGet, "/meals/"+meal.ID.String(), nil, http.StatusOK, &kept)
	if kept.ID != meal.ID {
		t.Fatal("withdrawal hid the caller's meal")
	}
	check(a, http.MethodPut, "/glucose", upload, http.StatusForbidden, nil)
	response = rig.do(a, http.MethodPost, "/v1/me/consent", map[string]string{"policy_version": nutritionPolicy})
	if response.Code != http.StatusNoContent {
		t.Fatalf("restore consent: %d", response.Code)
	}
	check(a, http.MethodGet, day, nil, http.StatusOK, &out)
	if out.GlucoseScore == nil || *out.GlucoseScore != 100 || out.N2R == nil {
		t.Fatalf("current consent did not restore scoring from retained inputs: %+v", out)
	}

	check(a, http.MethodDelete, "/glucose", nil, http.StatusNoContent, nil)
	check(a, http.MethodGet, summary, nil, http.StatusOK, &result)
	if len(result) != 2 || result[0].GlucoseScore != nil || result[0].NutritionScore == nil ||
		*result[0].NutritionScore != 100 || result[1].NutritionScore != nil || result[1].ScoreVersion == "" {
		t.Fatalf("summary fallback: %+v", result)
	}
	if response := rig.do(a, http.MethodDelete, "/v1/me/consent", nil); response.Code != http.StatusNoContent {
		t.Fatalf("withdraw consent before erasure: %d", response.Code)
	}
	check(a, http.MethodDelete, "/meals/"+meal.ID.String(), nil, http.StatusNoContent, nil)
	check(a, http.MethodGet, "/meals/"+meal.ID.String(), nil, http.StatusNotFound, nil)
}
