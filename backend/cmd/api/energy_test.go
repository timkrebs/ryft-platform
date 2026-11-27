package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestEnergyDayProgress(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, at, day string
		partial       bool
		elapsed       float64
	}{
		{"local date differs from UTC", "2026-01-02T23:30:00Z", "2026-01-03", true, 0.5 / 24},
		{"past day", "2026-01-02T23:30:00Z", "2026-01-02", false, 1},
		{"short day", "2026-03-29T10:00:00Z", "2026-03-29", true, 11.0 / 23},
		{"long day", "2026-10-25T11:00:00Z", "2026-10-25", true, 13.0 / 25},
		{"midnight", "2026-10-24T22:00:00Z", "2026-10-25", true, 0},
		{"after today (travelled west)", "2026-01-02T23:30:00Z", "2026-01-04", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if partial, elapsed := energyDayProgress(tc.day, now.In(loc)); partial != tc.partial || elapsed != tc.elapsed {
				t.Fatalf("partial and elapsed = %v, %v, want %v, %v", partial, elapsed, tc.partial, tc.elapsed)
			}
		})
	}
}

func TestDaysServeEnergy(t *testing.T) {
	rig := newNutritionRig(t)
	ctx := context.Background()
	owner, other := rig.account(), rig.account()
	setFlags := func(pilot, diary bool) {
		t.Helper()
		for key, enabled := range map[string]bool{model.FlagNutrition: pilot, model.FlagNutritionDiary: diary} {
			if _, _, err := rig.st.PutFlag(ctx, model.FeatureFlag{Key: key, Enabled: enabled, AllowUserIDs: []uuid.UUID{owner, other}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	setFlags(false, true)
	// Pick a zone away from midnight so the test can finish on the same local date.
	zone := "UTC"
	if hour := time.Now().UTC().Hour(); hour >= 22 || hour < 2 {
		zone = "Pacific/Honolulu"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.st.UpdateUser(ctx, owner, model.UserPatch{TZ: &zone}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	past, private, incomplete := today.AddDate(0, 0, -40), today.AddDate(0, 0, -3), today.AddDate(0, 0, -2)
	placeholder := today.AddDate(0, 0, -1)
	dayKey := func(d time.Time) string { return d.Format(dayLayout) }
	get := func(path string, out any) {
		t.Helper()
		resp := rig.do(owner, http.MethodGet, path, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", path, resp.Code)
		}
		if err := json.Unmarshal(resp.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	food := func(user uuid.UUID, nutrient, unit string, amount float64) uuid.UUID {
		t.Helper()
		resp := rig.do(user, http.MethodPost, nutritionPrefix+"/foods/custom", map[string]any{
			"client_request_id": uuid.New(), "name": "Energy fixture",
			"nutrients": []map[string]any{{"code": nutrient, "amount": amount, "unit": unit, "reference_amount": 100, "reference_unit": "g"}},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create food returned %d", resp.Code)
		}
		var item model.FoodDetail
		if err := json.Unmarshal(resp.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	meal := func(user uuid.UUID, day time.Time, item uuid.UUID, hour int) {
		t.Helper()
		resp := rig.do(user, http.MethodPost, nutritionPrefix+"/meals", model.NutritionMealInput{
			ClientRequestID: uuid.New(), ConsumedAt: day.Add(time.Duration(hour) * time.Hour),
			Timezone: zone, MealType: "snack", InputMethod: "manual_entry",
			Items: []model.NutritionMealItemInput{{FoodID: item, Quantity: 100, Unit: "g"}},
		})
		if resp.Code != http.StatusCreated {
			t.Fatalf("create meal returned %d", resp.Code)
		}
	}
	ownerFood, otherFood := food(owner, "energy_kcal", "kcal", 250.25), food(other, "energy_kcal", "kcal", 300)
	unknownEnergy := food(owner, "protein", "g", 10)
	for _, day := range []time.Time{past, today} {
		meal(owner, day, ownerFood, 0)
		meal(owner, day, ownerFood, 1)
	}
	meal(other, private, otherFood, 12)
	meal(other, past, otherFood, 12)
	meal(owner, incomplete, unknownEnergy, 12)
	meal(owner, incomplete, unknownEnergy, 13)
	meal(owner, placeholder, ownerFood, 12)
	active, resting := 200, 1400
	for _, day := range []time.Time{past, private, incomplete, today} {
		if _, err := rig.st.UpsertDailyScore(ctx, owner, store.ScoreRow{DailyScore: model.DailyScore{
			Day: dayKey(day), ActiveKcal: &active, RestingKcal: &resting,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	checkComplete := func(energy *model.DayEnergy, partial bool, confidence string, intake int) {
		t.Helper()
		if energy == nil || energy.PartialDay != partial || energy.Confidence == nil || *energy.Confidence != confidence ||
			energy.IntakeKcal == nil || *energy.IntakeKcal != intake || energy.ActiveKcal == nil || *energy.ActiveKcal != active ||
			energy.RestingKcal == nil || *energy.RestingKcal != resting || energy.RestingSource == nil || *energy.RestingSource != model.RestingSourceHealthKit ||
			energy.NetKcal == nil || *energy.NetKcal != intake-active-resting || energy.Missing == nil || len(energy.Missing) != 0 {
			t.Fatal("unexpected assembled energy")
		}
	}
	checkNoIntake := func(energy *model.DayEnergy) {
		t.Helper()
		if energy == nil || energy.IntakeKcal != nil || energy.Confidence != nil || energy.NetKcal != nil ||
			!slices.Equal(energy.Missing, []string{model.EnergyPartIntake}) {
			t.Fatal("intake must be unknown")
		}
	}
	rangePath := "/v1/days?from=" + dayKey(past) + "&to=" + dayKey(today)
	var rows []model.DailyScore
	get(rangePath, &rows)
	if len(rows) != 4 {
		t.Fatalf("range returned %d scored days, want 4", len(rows))
	}
	checkComplete(rows[0].Energy, false, model.EnergyConfidenceHigh, 501)
	checkNoIntake(rows[1].Energy)
	checkComplete(rows[2].Energy, false, model.EnergyConfidenceMedium, 0)
	checkComplete(rows[3].Energy, true, model.EnergyConfidenceMedium, 501)
	var detail model.DayDetail
	get("/v1/days/"+dayKey(past), &detail)
	checkComplete(detail.Score.Energy, false, model.EnergyConfidenceHigh, 501)
	setFlags(true, false)
	get("/v1/days/"+dayKey(today), &detail)
	checkComplete(detail.Score.Energy, true, model.EnergyConfidenceMedium, 501)

	// Any attempt to query the diary now fails in this test's disposable DB.
	if _, err := rig.pool.Exec(ctx, `ALTER TABLE nutrition.meals RENAME TO unreadable_meals`); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Score map[string]json.RawMessage `json:"score"`
	}
	get("/v1/days/"+dayKey(placeholder), &wire)
	if string(wire.Score["energy"]) != "null" {
		t.Fatal("the never-scored placeholder must send energy: null")
	}
	const unavailable = "net energy: diary totals unavailable"
	if strings.Contains(rig.logs.String(), unavailable) {
		t.Fatal("the placeholder day must not read the diary")
	}
	// With the diary on, an unreadable diary degrades to unknown intake, never a 500.
	get("/v1/days/"+dayKey(past), &detail)
	checkNoIntake(detail.Score.Energy)
	if n := strings.Count(rig.logs.String(), unavailable); n != 1 {
		t.Fatalf("the failed diary read logged %d warn lines, want 1", n)
	}
	setFlags(false, false)
	get("/v1/days/"+dayKey(past), &detail)
	checkNoIntake(detail.Score.Energy)
	get(rangePath, &rows)
	if len(rows) != 4 {
		t.Fatalf("range returned %d scored days with the diary off, want 4", len(rows))
	}
	for _, row := range rows {
		checkNoIntake(row.Energy)
	}
	// The diary was not read with both flags off: no further failed read.
	if n := strings.Count(rig.logs.String(), unavailable); n != 1 {
		t.Fatalf("the diary was read with both flags off (%d warn lines)", n)
	}
}
