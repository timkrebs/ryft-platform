package nutrition

import (
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestCatalogSelectionsKeepMeasureAndReferenceBasis(t *testing.T) {
	id, source, serving := uuid.New(), uuid.New(), uuid.New()
	volume, grams, density := 200., 206.8, 1.034
	food := model.FoodDetail{FoodSummary: model.FoodSummary{ID: id, Name: "Milk"}, Density: &density,
		Portions: []model.FoodPortion{{ID: serving, SourceID: source, IsDefault: true, ShortLabelDE: "Glas",
			FoodPortionInput: model.FoodPortionInput{Quantity: 1, Unit: "serving", Label: "Glass", VolumeML: &volume, WeightG: &grams}}},
		Nutrients: []model.FoodNutrient{
			{SourceID: source, FoodNutrientInput: model.FoodNutrientInput{Code: "energy_kcal", Amount: 46, ReferenceUnit: "g", ReferenceAmount: 100}},
			{SourceID: source, FoodNutrientInput: model.FoodNutrientInput{Code: "fat", Amount: 0, ReferenceUnit: "g", ReferenceAmount: 100}},
		},
	}
	standard, _, reference := CatalogSelections(food, model.NutritionMealItem{}, "de-DE")
	if standard == nil || standard.GramWeight == nil || *standard.GramWeight != grams || standard.VolumeML == nil ||
		*standard.VolumeML != volume || standard.Kcal == nil || *standard.Kcal != 95.128 ||
		standard.FatG == nil || *standard.FatG != 0 || standard.CarbsG != nil || standard.ShortLabel != "Glas" {
		t.Fatalf("wrong measured selection %+v", standard)
	}
	if reference == nil || reference.Unit != "g" || reference.GramWeight == nil || *reference.GramWeight != 100 {
		t.Fatalf("wrong mass reference %+v", reference)
	}
	food.Density, food.Portions = nil, nil
	food.Nutrients[0].ReferenceUnit = "ml"
	food.Nutrients = food.Nutrients[:1]
	_, previous, reference := CatalogSelections(food, model.NutritionMealItem{Quantity: 150, Unit: "g"}, "de")
	if previous != nil || reference == nil || reference.Unit != "ml" || reference.GramWeight != nil {
		t.Fatalf("unsafe conversion or wrong volume reference: previous=%+v reference=%+v", previous, reference)
	}
}
