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

func TestOwnerMealExportAfterWithdrawalAndFlagDisable(t *testing.T) {
	rg := newNutritionRig(t)
	ctx := context.Background()
	a, b := rg.account(), rg.account()
	at := time.Date(2021, 1, 1, 12, 30, 0, 0, time.UTC)
	seed := func(user, request uuid.UUID, name string) model.NutritionMeal {
		t.Helper()
		energy := 200.
		food, err := rg.st.CreateCustomFood(ctx, user, model.FoodCustomInput{ClientRequestID: uuid.New(), Name: name,
			Nutrients: []model.FoodNutrientWrite{{Code: "energy_kcal", Amount: &energy, Unit: "kcal", ReferenceAmount: 100, ReferenceUnit: "g"}}})
		if err != nil {
			t.Fatal(err)
		}
		meal, _, err := rg.st.CreateNutritionMeal(ctx, user, model.NutritionMealInput{
			ClientRequestID: request, ConsumedAt: at, Timezone: "UTC", MealType: "snack", InputMethod: "manual_entry",
			Items: []model.NutritionMealItemInput{{FoodID: food.ID, Quantity: 100, Unit: "g"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return meal
	}
	first := seed(a, uuid.MustParse("00000000-0000-4000-9000-000000000001"), "First fixture meal")
	second := seed(a, uuid.MustParse("20000000-0000-4000-9000-000000000001"), "Second fixture meal")
	other := seed(b, uuid.MustParse("10000000-0000-4000-9000-000000000001"), "Other fixture meal")
	if err := rg.st.RevokeConsent(ctx, a); err != nil {
		t.Fatal(err)
	}
	check := func(user uuid.UUID, query string, status int) model.NutritionMealExportPage {
		t.Helper()
		r := rg.do(user, http.MethodGet, nutritionDiaryExportPath+query, nil)
		if r.Code != status || !strings.Contains(r.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("status=%d, cache=%q want %d no-store", r.Code, r.Header().Get("Cache-Control"), status)
		}
		var page model.NutritionMealExportPage
		if status == 200 {
			if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page
	}
	check(uuid.Nil, "", 401)
	for _, query := range []string{"?limit=26", "?after=nope", "?limit=1&limit=2", "?user_id=" + b.String()} {
		check(a, query, 422)
	}
	page := check(a, "?limit=1", 200)
	if len(page.Meals) != 1 || page.Meals[0].ID != first.ID || page.NextAfter == nil {
		t.Fatal("missing or foreign first meal, or missing next cursor")
	}
	if err := rg.st.DeleteNutritionMeal(ctx, a, first.ID); err != nil {
		t.Fatal(err)
	}
	rest := check(a, "?limit=1&after="+page.NextAfter.String(), 200)
	if len(rest.Meals) != 1 || rest.Meals[0].ID != second.ID || rest.NextAfter != nil || len(rest.Meals[0].Items) != 1 {
		t.Fatal("cursor failed after erasure or meal snapshot was incomplete")
	}
	if got := rest.Meals[0].Items[0].Nutrients["energy_kcal"].Amount; got != 200 {
		t.Fatalf("snapshot energy=%g", got)
	}
	foreign := check(b, "", 200)
	if len(foreign.Meals) != 1 || foreign.Meals[0].ID != other.ID {
		t.Fatal("another account's export did not stay private")
	}
}
