package nutrition

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestGTIN(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"012345678905", "00012345678905"},
		{"0012345678905", "00012345678905"},
		{"00012345678905", "00012345678905"},
		{"96385074", "00000096385074"},
		{"012345678904", ""},
		{"00000000", ""},
		{"01234567890x", ""},
		{" 012345678905", ""},
	} {
		got, err := GTIN(tc.input)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("GTIN(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestNormalizeAndUnits(t *testing.T) {
	if got := Normalize("  Hähnchenbrust,  Süß–Sauér  "); got != "hahnchenbrust suss sauer" {
		t.Fatalf("normalize: %q", got)
	}
	for _, tc := range []struct {
		amount   float64
		from, to string
		want     float64
	}{
		{.12, "g", "mg", 120},
		{1.05, "µg", "mg", .00105},
		{8, "KJ", "kJ", 8},
		{0, "g", "mg", 0},
	} {
		if got, err := Unit(tc.amount, tc.from, tc.to); err != nil || got != tc.want {
			t.Errorf("Unit(%+v) = %g, %v", tc, got, err)
		}
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1), .0000001} {
		if _, err := Unit(bad, "ug", "g"); err == nil {
			t.Errorf("accepted invalid or unrepresentable amount %g", bad)
		}
	}
	if _, err := Unit(1, "IU", "ug"); err == nil {
		t.Fatal("converted IU without a nutrient-specific rule")
	}
}

func TestCalculateSnapshots(t *testing.T) {
	source, secondary, id, portion := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	grams, ml := 240., 200.
	f := model.FoodDetail{FoodSummary: model.FoodSummary{ID: id, Name: "Fixture food"}}
	f.Portions = []model.FoodPortion{{
		ID: portion, SourceID: source,
		FoodPortionInput: model.FoodPortionInput{Label: "2 servings", Quantity: 2, Unit: "serving", WeightG: &grams, VolumeML: &ml},
	}}
	f.Sources = []model.FoodSource{{ID: source}, {ID: secondary}}
	value := func(code, unit, basis string, a, ref float64, src uuid.UUID) model.FoodNutrient {
		return model.FoodNutrient{SourceID: src, Quality: "source_reported", FoodNutrientInput: model.FoodNutrientInput{
			Code: code, Amount: a, Unit: unit, ReferenceUnit: basis, ReferenceAmount: ref}}
	}
	f.Nutrients = []model.FoodNutrient{
		value("protein", "g", "g", 9, 100, source),
		value("protein", "g", "serving", 10.8, 1, source),
		value("fat", "g", "g", 0, 100, source),
		value("vitamin_c", "mg", "ml", 1.5, 100, secondary),
		value("folate", "ug", "serving", 2, 1, secondary),
		value("glycemic_index", "index", "g", 74, 100, source),
	}
	i, err := Calculate(f, model.NutritionMealItemInput{FoodID: id, Unit: "serving", Quantity: 1.5, Portion: &portion})
	if err != nil || i.Nutrients["protein"].Amount != 16.2 || i.Nutrients["vitamin_c"].Amount != 2.25 ||
		i.Nutrients["fat"].Unit != "g" || i.Nutrients["glycemic_index"].Amount != 74 ||
		*i.WeightG != 180 || *i.VolumeML != 150 || len(i.Nutrients) != 4 || len(i.Sources) != 2 {
		t.Fatalf("snapshot: %+v, %v", i, err)
	}
	if _, ok := i.Nutrients["folate"]; ok {
		t.Fatal("used an unrelated source's serving measure")
	}
	f.Nutrients = f.Nutrients[:1]
	in := model.NutritionMealItemInput{FoodID: id, Unit: "ml", Quantity: 100}
	if _, err := Calculate(f, in); !errors.Is(err, ErrConversion) {
		t.Fatalf("implicit density: %v", err)
	}
	density := 1.2
	f.Density = &density
	i, err = Calculate(f, in)
	if err != nil || i.Nutrients["protein"].Amount != 10.8 {
		t.Fatalf("explicit density: %+v, %v", i, err)
	}
	in.Quantity = 0
	if _, err := Calculate(f, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid quantity: %v", err)
	}
}

func TestLocalDate(t *testing.T) {
	for _, tc := range []struct{ at, zone, want string }{
		{"2026-03-28T23:55:00Z", "Europe/Berlin", "2026-03-29"},
		{"2026-03-29T01:15:00Z", "Europe/Berlin", "2026-03-29"},
		{"2026-10-25T00:45:00Z", "Europe/Berlin", "2026-10-25"},
		{"2026-10-25T01:45:00Z", "Europe/Berlin", "2026-10-25"},
		{"2026-03-28T23:55:00Z", "America/New_York", "2026-03-28"},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if date, err := LocalDate(at, tc.zone); date != tc.want || err != nil {
			t.Errorf("%+v: %s, %v", tc, date, err)
		}
	}
	if _, err := LocalDate(time.Now(), "Local"); err == nil {
		t.Fatal("accepted process-dependent timezone")
	}
}
