package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

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
