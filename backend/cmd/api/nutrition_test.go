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

func TestNutritionDiaryCohortAndIndependentErasure(t *testing.T) {
	rg := newNutritionRig(t)
	ctx := context.Background()
	pilot, diary, outsider := rg.account(), rg.account(), rg.account()
	put := func(f model.FeatureFlag) {
		t.Helper()
		if _, _, err := rg.st.PutFlag(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	put(model.FeatureFlag{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: []uuid.UUID{pilot}})
	put(model.FeatureFlag{Key: model.FlagNutritionDiary, Enabled: true, AllowUserIDs: []uuid.UUID{diary}})
	check := func(user uuid.UUID, method, path string, payload any, want int, out any) {
		t.Helper()
		r := rg.do(user, method, path, payload)
		if r.Code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, r.Code, want)
		}
		if out != nil {
			if err := json.Unmarshal(r.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var cfg model.RemoteConfig
	check(diary, http.MethodGet, "/v1/config", nil, 200, &cfg)
	if !cfg.Flags[model.FlagNutritionDiary] || cfg.Flags[model.FlagNutrition] {
		t.Fatalf("wrong diary flags: %v", cfg.Flags)
	}
	var food model.FoodDetail
	check(diary, http.MethodPost, nutritionPrefix+"/foods/custom", map[string]any{
		"client_request_id": uuid.New(), "name": "Diary fixture",
		"nutrients": []map[string]any{{"code": "energy_kcal", "amount": 200, "unit": "kcal", "reference_amount": 100, "reference_unit": "g"}},
	}, 200, &food)
	at := time.Now().UTC().Truncate(time.Second)
	day := at.Format(time.DateOnly)
	var meal model.NutritionMeal
	check(diary, http.MethodPost, nutritionPrefix+"/meals", model.NutritionMealInput{ClientRequestID: uuid.New(), ConsumedAt: at,
		Timezone: "UTC", MealType: "snack", InputMethod: "manual_entry",
		Items: []model.NutritionMealItemInput{{FoodID: food.ID, Quantity: 100, Unit: "g"}}}, 201, &meal)
	var total model.NutritionDaily
	check(diary, http.MethodGet, nutritionPrefix+"/daily?date="+day, nil, 200, &total)
	if total.Meals != 1 || total.Nutrients["energy_kcal"].KnownSubtotal != 200 || total.N2R != nil || total.GlucoseScore != nil || total.NutritionScore != nil {
		t.Fatalf("diary returned wrong totals or advanced scores: %+v", total)
	}
	check(outsider, http.MethodGet, nutritionPrefix+"/daily?date="+day, nil, 404, nil)
	check(diary, http.MethodGet, nutritionPrefix+"/n2r/profile", nil, 404, nil)
	check(diary, http.MethodPut, nutritionPrefix+"/glucose", `{}`, 404, nil)
	check(diary, http.MethodGet, nutritionPrefix+"/guidance/current", nil, 404, nil)
	check(diary, http.MethodGet, "/v1/coaching/daily-briefing", nil, 404, nil)
	check(pilot, http.MethodGet, nutritionPrefix+"/n2r/profile", nil, 200, nil)
	check(pilot, http.MethodGet, nutritionPrefix+"/daily?date="+day, nil, 200, nil)
	put(model.FeatureFlag{Key: model.FlagNutritionDiary})
	path := nutritionPrefix + "/meals/" + meal.ID.String()
	check(uuid.Nil, http.MethodDelete, path, nil, 401, nil)
	check(outsider, http.MethodDelete, path, nil, 204, nil) // idempotent and conceals ownership
	if _, err := rg.st.NutritionMeal(ctx, diary, meal.ID); err != nil {
		t.Fatalf("another account removed the diary meal: %v", err)
	}
	check(diary, http.MethodDelete, path, nil, 204, nil)
	check(diary, http.MethodDelete, nutritionPrefix+"/glucose", nil, 204, nil)
	check(diary, http.MethodDelete, nutritionPrefix+"/n2r/profile", nil, 204, nil)
	check(diary, http.MethodDelete, "/v1/users/goal-profile", nil, 204, nil)
	check(diary, http.MethodDelete, "/v1/me/consent", nil, 204, nil)
}

func TestNutritionRoutesUseBearerFlagsConsentAndPrivateResponses(t *testing.T) {
	rg := newNutritionRig(t)
	a, b, noConsent := rg.account(), rg.account(), rg.account(true)
	body := map[string]any{"client_request_id": uuid.New(), "name": "private marker 12942"}
	path := nutritionPrefix + "/foods/custom"
	expect := func(user uuid.UUID, method, path string, body any, status int) string {
		t.Helper()
		r := rg.do(user, method, path, body)
		if r.Code != status {
			t.Fatalf("%s %s: got %d expected %d: %s", method, path, r.Code, status, r.Body)
		}
		return r.Body.String()
	}
	expect(uuid.Nil, http.MethodPost, path, body, http.StatusUnauthorized)
	expect(a, http.MethodPost, path, body, http.StatusNotFound)
	if _, _, err := rg.st.PutFlag(context.Background(), model.FeatureFlag{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: []uuid.UUID{a, b, noConsent}}); err != nil {
		t.Fatal(err)
	}
	expect(noConsent, http.MethodPost, path, body, http.StatusForbidden)
	encoded, _ := json.Marshal(body)
	injected := strings.TrimSuffix(string(encoded), "}") + `,"user_id":"` + b.String() + `"}`
	expect(a, http.MethodPost, path, injected, http.StatusBadRequest)
	expect(a, http.MethodPost, path, string(encoded)+` {}`, http.StatusBadRequest)
	expect(a, http.MethodPost, path, string(encoded)+strings.Repeat(" ", 64<<10), http.StatusRequestEntityTooLarge)
	var food model.FoodDetail
	raw := expect(a, http.MethodPost, path, body, http.StatusOK)
	if err := json.Unmarshal([]byte(raw), &food); err != nil {
		t.Fatal(err)
	}
	expect(b, http.MethodGet, nutritionPrefix+"/foods/"+food.ID.String(), nil, http.StatusNotFound)
	search := model.FoodSearch{Query: "private marker 12942"}
	var page model.FoodSearchPage
	raw = expect(b, http.MethodPost, nutritionPrefix+"/foods/search", search, http.StatusOK)
	if err := json.Unmarshal([]byte(raw), &page); err != nil || len(page.Foods) != 0 {
		t.Fatalf("search leak: %s %v", raw, err)
	}
	raw = expect(a, http.MethodPost, nutritionPrefix+"/foods/barcode", model.FoodBarcodeQuery{Barcode: "012345678905"}, http.StatusOK)
	var barcode model.FoodBarcodeResult
	if err := json.Unmarshal([]byte(raw), &barcode); err != nil || barcode.GTIN != "00012345678905" || len(barcode.Matches) != 0 {
		t.Fatalf("structured miss: %s %v", raw, err)
	}
	if logs := rg.logs.String(); strings.Contains(logs, "private marker 12942") || strings.Contains(logs, food.ID.String()) {
		t.Fatal("private nutrition input or identifier in API logs")
	}
}
