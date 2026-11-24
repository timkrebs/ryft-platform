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

func TestN2RProfileGuidanceAndDailyContract(t *testing.T) {
	rig := newNutritionRig(t)
	ctx := context.Background()
	a, b, withoutConsent := rig.account(), rig.account(), rig.account(true)
	if _, _, err := rig.st.PutFlag(ctx, model.FeatureFlag{Key: model.FlagNutrition, Enabled: true, AllowUserIDs: []uuid.UUID{a, b, withoutConsent}}); err != nil {
		t.Fatal(err)
	}
	check := func(user uuid.UUID, method, path string, payload any, status int, out any) {
		t.Helper()
		r := rig.do(user, method, nutritionPrefix+path, payload)
		if r.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, r.Code, r.Body)
		}
		if out != nil {
			if err := json.Unmarshal(r.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	profile := model.N2RProfile{BodyMassKG: 65, ExpectedBedtime: "23:30"}
	check(withoutConsent, http.MethodPut, "/n2r/profile", profile, http.StatusForbidden, nil)
	check(withoutConsent, http.MethodGet, "/guidance/current", nil, http.StatusForbidden, nil)
	check(a, http.MethodPut, "/n2r/profile", model.N2RProfile{BodyMassKG: 0}, http.StatusUnprocessableEntity, nil)
	check(a, http.MethodPut, "/n2r/profile", profile, http.StatusOK, nil)
	check(a, http.MethodPut, "/goals", model.NutritionGoalInput{Targets: map[string]float64{"energy_kcal": 2000}}, http.StatusOK, nil)
	var guidance model.N2RGuidance
	check(a, http.MethodGet, "/guidance/current", nil, http.StatusOK, &guidance)
	if guidance.Score == nil || guidance.Score.Budget.EnergyTarget != 2000 || guidance.Cards == nil {
		t.Fatalf("guidance: %+v", guidance)
	}
	day := time.Now().UTC().Format(time.DateOnly)
	var summary model.NutritionDaily
	check(a, http.MethodGet, "/daily?date="+day, nil, http.StatusOK, &summary)
	if summary.N2R == nil || summary.N2R.Score != nil || summary.NutritionScore != nil {
		t.Fatalf("daily: %+v", summary)
	}
	var other model.N2RGuidance
	check(b, http.MethodGet, "/guidance/current", nil, http.StatusOK, &other)
	if other.Score != nil || len(other.Cards) > 0 {
		t.Fatalf("other account: %+v", other)
	}
	check(a, http.MethodDelete, "/n2r/profile", nil, http.StatusNoContent, nil)
	var deleted *model.N2RProfile
	check(a, http.MethodGet, "/n2r/profile", nil, http.StatusOK, &deleted)
	if deleted != nil {
		t.Fatal(deleted)
	}
}
