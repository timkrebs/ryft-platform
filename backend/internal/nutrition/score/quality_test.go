package score

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func pointer[T any](x T) *T { return &x }

func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > 1e-9 {
		t.Errorf("got %g, want %g", got, want)
	}
}

func resultValue(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("got null, want %g", want)
	}
	closeTo(t, *got, want)
}

func value(amount float64, unit string) model.NutritionSnapValue {
	return model.NutritionSnapValue{Amount: amount, Unit: unit}
}

func perfectMeal() model.NutritionMeal {
	return model.NutritionMeal{
		ID: uuid.New(), Timezone: "UTC", ConsumedAt: time.Date(2026, 11, 24, 21, 30, 0, 0, time.UTC),
		Items: []model.NutritionMealItem{{
			WeightG: pointer(1000.0), QualityProfile: &model.NutritionQualityProfile{
				NonStarchyVegetableFraction: pointer(.3), WholeFruitFraction: pointer(.2),
				NutsLegumesFraction: pointer(.03), RedProcessedMeatFraction: pointer(0.0), SugaryDrink: pointer(false),
			},
			Nutrients: map[string]model.NutritionSnapValue{
				"energy_kcal": value(900, "kcal"), "energy_kj": value(9999, "kJ"),
				"fiber": value(30, "g"), "epa": value(.1, "g"), "dha": value(.15, "g"),
				"monounsaturated_fat": value(6, "g"), "polyunsaturated_fat": value(4, "g"),
				"added_sugar": value(10, "g"), "total_sugar": value(60, "g"),
				"sodium": value(1500, "mg"), "trans_fat": value(.5, "g"),
			},
		}},
	}
}

func TestQualityGateAndPerfectDay(t *testing.T) {
	m := perfectMeal()
	for _, target := range []float64{3001, 0, -1, math.NaN()} {
		got := Daily([]model.NutritionMeal{m}, target, nil, nil)
		if got.QualityScore != nil || got.NutritionScore != nil || got.Breakdown.FiberG != nil {
			t.Fatalf("unlocked for target %v: %+v", target, got)
		}
	}
	got := Daily([]model.NutritionMeal{m}, 3000, nil, nil)
	resultValue(t, got.QualityScore, 100)
	resultValue(t, got.NutritionScore, 100)
	if got.GlucoseScore != nil || len(got.MealGlucoseScores) != 0 {
		t.Fatalf("CGM data invented: %+v", got)
	}
	delete(m.Items[0].Nutrients, "energy_kcal")
	m.Items[0].Nutrients["energy_kj"] = value(900*kilojoulesPerKcal, "kJ")
	resultValue(t, Daily([]model.NutritionMeal{m}, 3000, nil, nil).QualityScore, 100)
}

func TestQualityPenaltiesAndUnknowns(t *testing.T) {
	m := perfectMeal()
	item := &m.Items[0]
	item.Nutrients["added_sugar"] = value(30, "g")
	item.Nutrients["sodium"] = value(2500, "mg")
	got := Daily([]model.NutritionMeal{m}, 3000, nil, nil)
	resultValue(t, got.Breakdown.AddedSugarG, 5)
	resultValue(t, got.Breakdown.SodiumMg, 5)
	resultValue(t, got.QualityScore, 90)

	item.QualityProfile.SugaryDrink = pointer(true)
	item.QualityProfile.WholeFruitFraction = pointer(0.0)
	got = Daily([]model.NutritionMeal{m}, 3000, nil, nil)
	resultValue(t, got.Breakdown.AddedSugarG, 0)
	resultValue(t, got.Breakdown.WholeFruitG, 0)

	item.Nutrients["monounsaturated_fat"] = value(2, "g")
	item.Nutrients["polyunsaturated_fat"] = value(0, "g")
	resultValue(t, Daily([]model.NutritionMeal{m}, 3000, nil, nil).Breakdown.UnsaturatedFatKcalPct, 2)
	item.Nutrients["trans_fat"] = value(2.25, "g")
	item.QualityProfile.RedProcessedMeatFraction = pointer(.055)
	got = Daily([]model.NutritionMeal{m}, 3000, nil, nil)
	resultValue(t, got.Breakdown.TransFatKcalPct, 5)
	resultValue(t, got.Breakdown.RedProcessedMeatG, 5)

	delete(item.Nutrients, "sodium")
	got = Daily([]model.NutritionMeal{m}, 3000, nil, nil)
	if got.Breakdown.SodiumMg != nil || got.QualityScore != nil || got.NutritionScore != nil || got.Breakdown.FiberG == nil {
		t.Fatalf("missing sodium became a score: %+v", got)
	}
}

func TestWeightedGlucoseAndChrononutrition(t *testing.T) {
	a := perfectMeal()
	a.ConsumedAt = a.ConsumedAt.Add(-time.Hour)
	b := perfectMeal()
	b.Items[0].Nutrients["energy_kcal"] = value(300, "kcal")
	for _, m := range []model.NutritionMeal{a, b} {
		m.Items[0].Nutrients["sodium"] = value(500, "mg")
		m.Items[0].Nutrients["added_sugar"] = value(5, "g")
		m.Items[0].Nutrients["trans_fat"] = value(.1, "g")
	}
	c := perfectMeal()
	delete(c.Items[0].Nutrients, "energy_kcal")
	delete(c.Items[0].Nutrients, "energy_kj")
	g := map[uuid.UUID]model.NutritionMealGlucoseScore{
		a.ID: {MealID: a.ID, GlucoseScore: 100}, b.ID: {MealID: b.ID, GlucoseScore: 20}, c.ID: {MealID: c.ID, GlucoseScore: 0},
	}
	timing := &Timing{LocalTimes: map[uuid.UUID]time.Time{a.ID: a.ConsumedAt, b.ID: b.ConsumedAt, c.ID: c.ConsumedAt}}
	got := Daily([]model.NutritionMeal{b, a}, 2500, g, timing)
	resultValue(t, got.GlucoseScore, 80)
	resultValue(t, got.NutritionScore, 94)
	closeTo(t, got.ChrononutritionPenalty, 0) // exactly 25%
	if len(got.MealGlucoseScores) != 2 || got.MealGlucoseScores[0].MealID != a.ID {
		t.Fatalf("meal scores out of sequence: %+v", got.MealGlucoseScores)
	}
	got = Daily([]model.NutritionMeal{a, b, c}, 2500, g, timing)
	resultValue(t, got.GlucoseScore, 80)
	if got.QualityScore != nil || len(got.MealGlucoseScores) != 3 {
		t.Fatalf("coverage: %+v", got)
	}
	b.Items[0].Nutrients["energy_kcal"] = value(301, "kcal")
	got = Daily([]model.NutritionMeal{a, b}, 2500, nil, timing)
	resultValue(t, got.NutritionScore, 95)
	closeTo(t, got.ChrononutritionPenalty, 5)

	bed := b.ConsumedAt.Add(2 * time.Hour)
	timing.Bedtime = &bed
	got = Daily([]model.NutritionMeal{a, b}, 2500, nil, timing)
	resultValue(t, got.NutritionScore, 100)
	bed = bed.Add(-time.Minute)
	resultValue(t, Daily([]model.NutritionMeal{a, b}, 2500, nil, timing).NutritionScore, 95)
	resultValue(t, Daily([]model.NutritionMeal{a, b}, 2500, nil, nil).NutritionScore, 100)
}

func TestLateUsesCallerSuppliedLocalTime(t *testing.T) {
	zone := time.FixedZone("test timezone", 2*int(time.Hour/time.Second))
	at := time.Date(2026, 11, 24, 19, 0, 0, 0, time.UTC)
	if isLate(at, at.In(zone), nil) || !isLate(at.Add(time.Second), at.Add(time.Second).In(zone), nil) {
		t.Fatal("local cutoff must be strictly after 21:00")
	}
}
