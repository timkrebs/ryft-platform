package nutrition

import (
	"slices"
	"strings"

	"github.com/timkrebs/ryft/internal/model"
)

// CatalogLabel prefers the requested regional language, its base language, then English.
func CatalogLabel(labels map[string]string, language string) string {
	base, _, _ := strings.Cut(strings.ToLower(language), "-")
	for _, key := range []string{strings.ToLower(language), base, "en"} {
		if label := labels[key]; label != "" {
			return label
		}
	}
	return ""
}

// CatalogFoodName resolves an explicit display language against non-alias source names.
func CatalogFoodName(food model.FoodDetail, language string) string {
	if language == "" {
		return food.Name
	}
	language = strings.ToLower(language)
	base, _, _ := strings.Cut(language, "-")
	for _, code := range []string{language, base} {
		for _, name := range food.Names {
			if name.Language == code && !name.Alias {
				return name.Label
			}
		}
	}
	return food.Name
}

func portionLabels(p model.FoodPortion, language string) (string, string) {
	name := CatalogLabel(p.Labels, language)
	if name == "" {
		name = p.Label
	}
	short := CatalogLabel(map[string]string{"de": p.ShortLabelDE, "en": p.ShortLabelEN}, language)
	return name, short
}

func savedAmount(values map[string]model.NutritionSnapValue, code string) *float64 {
	if v, ok := values[code]; ok {
		return &v.Amount
	}
	return nil
}

func portionSelection(food model.FoodDetail, in model.NutritionMealItemInput) *model.FoodPortionSelection {
	item, err := Calculate(food, in)
	if err != nil {
		return nil
	}
	selection := &model.FoodPortionSelection{
		PortionID: in.Portion, Quantity: in.Quantity, Unit: in.Unit, GramWeight: item.WeightG, VolumeML: item.VolumeML,
		Kcal: savedAmount(item.Nutrients, "energy_kcal"), ProteinG: savedAmount(item.Nutrients, "protein"),
		CarbsG: savedAmount(item.Nutrients, "carbohydrates"), FatG: savedAmount(item.Nutrients, "fat"),
	}
	if selection.CarbsG == nil {
		selection.CarbsG = savedAmount(item.Nutrients, "carbohydrates_total")
	}
	if item.PortionLabel != nil {
		selection.Label = *item.PortionLabel
	}
	if item.PortionShortLabel != nil {
		selection.ShortLabel = *item.PortionShortLabel
	}
	return selection
}

// CatalogSelections calculates previews using the same conversion rules as meal writes.
// A retired or altered portion can still be reused by its saved compatible mass or volume.
func CatalogSelections(food model.FoodDetail, last model.NutritionMealItem, language string) (standard, previous, reference *model.FoodPortionSelection) {
	selection := func(quantity float64, unit string, portion *model.FoodPortion) *model.FoodPortionSelection {
		in := model.NutritionMealItemInput{FoodID: food.ID, Quantity: quantity, Unit: unit, Language: language}
		if portion != nil {
			in.Portion = &portion.ID
		}
		return portionSelection(food, in)
	}
	for i := range food.Portions {
		p := &food.Portions[i]
		if p.IsDefault && !p.Estimated {
			standard = selection(p.Quantity, p.Unit, p)
			if standard != nil {
				break
			}
		}
	}
	if last.Unit == "g" || last.Unit == "ml" {
		previous = selection(last.Quantity, last.Unit, nil)
	}
	if last.Portion != nil {
		i := slices.IndexFunc(food.Portions, func(p model.FoodPortion) bool { return p.ID == *last.Portion })
		if i >= 0 {
			p := &food.Portions[i]
			candidate := selection(last.Quantity, last.Unit, p)
			if candidate != nil && sameSavedMeasure(last.WeightG, candidate.GramWeight) && sameSavedMeasure(last.VolumeML, candidate.VolumeML) {
				previous = candidate
			}
		}
		if previous == nil && last.WeightG != nil {
			previous = selection(*last.WeightG, "g", nil)
		}
		if previous == nil && last.VolumeML != nil {
			previous = selection(*last.VolumeML, "ml", nil)
		}
	}
	if len(food.Nutrients) > 0 {
		// Prefer the represented nutrient basis over a converted amount.
		preferVolume := slices.ContainsFunc(food.Nutrients, func(n model.FoodNutrient) bool { return n.ReferenceUnit == "ml" }) &&
			!slices.ContainsFunc(food.Nutrients, func(n model.FoodNutrient) bool { return n.ReferenceUnit == "g" })
		units := [2]string{"g", "ml"}
		if preferVolume {
			units = [2]string{"ml", "g"}
		}
		for _, unit := range units {
			if reference = selection(100, unit, nil); reference != nil {
				break
			}
		}
	}
	return standard, previous, reference
}

func sameSavedMeasure(saved, current *float64) bool {
	return saved == nil || current != nil && *saved == *current
}
