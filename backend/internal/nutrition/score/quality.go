package score

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	// Version identifies the thresholds and rules used for a Nutrition Score.
	Version = "1.0.0"

	qualityComponentMax   = 10
	dayScoreMax           = 100
	minimumLoggedFraction = 0.30
	gramsToMilligrams     = 1000
	kilojoulesPerKcal     = 4.184
	fatKcalPerGram        = 9

	vegetableTargetG     = 300
	fruitTargetG         = 200
	fiberTargetG         = 30
	nutsLegumesTargetG   = 30
	epaDHATargetMg       = 250
	unsaturatedTargetPct = 10
	addedSugarOptimalG   = 10
	addedSugarPoorG      = 50
	meatOptimalG         = 0
	meatPoorG            = 110
	sodiumOptimalMg      = 1500
	sodiumPoorMg         = 3500
	transFatOptimalPct   = 0.5
	transFatPoorPct      = 4

	qualityWeight       = 0.70
	glucoseWeight       = 0.30
	lateHour            = 21
	lateBeforeBed       = 2 * time.Hour
	lateCalorieFraction = 0.25
	latePenalty         = 5
)

type total struct {
	amount  float64
	missing bool
}

func (t *total) add(v float64, known bool) {
	t.missing = t.missing || !known || !validAmount(v)
	if known && validAmount(v) {
		t.amount += v
	}
}

// Match the six decimal places used for nutrient snapshots at threshold boundaries.
func roundCalories(kcal float64) float64 {
	const microKcal = 1_000_000
	return math.Round(kcal*microKcal) / microKcal
}

func validAmount(v float64) bool { return v >= 0 && !math.IsInf(v, 0) }

func nutrient(i model.NutritionMealItem, code, unit string) (float64, bool) {
	n, ok := i.Nutrients[code]
	return n.Amount, ok && n.Unit == unit && validAmount(n.Amount)
}

func energy(i model.NutritionMealItem) (float64, bool) {
	if kcal, ok := nutrient(i, "energy_kcal", "kcal"); ok {
		return kcal, true
	}
	kj, ok := nutrient(i, "energy_kj", "kJ")
	return kj / kilojoulesPerKcal, ok
}

func combined(i model.NutritionMealItem, first, second string) (float64, bool) {
	a, knownA := nutrient(i, first, "g")
	b, knownB := nutrient(i, second, "g")
	return a + b, knownA && knownB
}

func classifiedMass(weight, fraction *float64) (float64, bool) {
	if fraction == nil || !validAmount(*fraction) || *fraction > 1 {
		return 0, false
	}
	if *fraction == 0 {
		return 0, true
	}
	if weight == nil || !validAmount(*weight) {
		return 0, false
	}
	return *weight * *fraction, true
}

type qualityTotals struct {
	energy, vegetables, fruit, fiber, nutsLegumes, epaDHA, unsaturated, sugar, meat, sodium, transFat total
}

func (q *qualityTotals) add(i model.NutritionMealItem) {
	q.energy.add(energy(i))
	q.fiber.add(nutrient(i, "fiber", "g"))
	q.epaDHA.add(combined(i, "epa", "dha"))
	q.unsaturated.add(combined(i, "monounsaturated_fat", "polyunsaturated_fat"))
	q.sodium.add(nutrient(i, "sodium", "mg"))
	q.transFat.add(nutrient(i, "trans_fat", "g"))

	profile := model.NutritionQualityProfile{}
	if i.QualityProfile != nil {
		profile = *i.QualityProfile
	}
	q.vegetables.add(classifiedMass(i.WeightG, profile.NonStarchyVegetableFraction))
	q.fruit.add(classifiedMass(i.WeightG, profile.WholeFruitFraction))
	q.nutsLegumes.add(classifiedMass(i.WeightG, profile.NutsLegumesFraction))
	q.meat.add(classifiedMass(i.WeightG, profile.RedProcessedMeatFraction))

	q.sugar.add(addedSugar(i))
}

func addedSugar(i model.NutritionMealItem) (float64, bool) {
	added, addedKnown := nutrient(i, "added_sugar", "g")
	sugar, sugarKnown := nutrient(i, "total_sugar", "g")
	var sugary *bool
	if i.QualityProfile != nil {
		sugary = i.QualityProfile.SugaryDrink
	}
	switch {
	case sugary != nil && *sugary:
		return sugar, sugarKnown
	case sugary != nil:
		return added, addedKnown
	default:
		// This amount holds under either classification.
		return added, addedKnown && sugarKnown && added == sugar
	}
}

func clamp(x, limit float64) float64 { return min(limit, max(0, x)) }

func positive(v total, target float64) *float64 {
	if v.missing || !validAmount(v.amount) {
		return nil
	}
	result := clamp(qualityComponentMax*v.amount/target, qualityComponentMax)
	return &result
}

func negative(v total, optimal, poor float64) *float64 {
	if v.missing || !validAmount(v.amount) {
		return nil
	}
	result := clamp(qualityComponentMax*(poor-v.amount)/(poor-optimal), qualityComponentMax)
	return &result
}

func fatPct(fat, kcal total) total {
	if kcal.missing || kcal.amount <= 0 || !validAmount(kcal.amount) {
		return total{missing: true}
	}
	return total{amount: fat.amount * fatKcalPerGram * dayScoreMax / kcal.amount, missing: fat.missing}
}

func (q qualityTotals) breakdown() model.NutritionQualityBreakdown {
	return model.NutritionQualityBreakdown{
		NonStarchyVegG:        positive(q.vegetables, vegetableTargetG),
		WholeFruitG:           positive(q.fruit, fruitTargetG),
		FiberG:                positive(q.fiber, fiberTargetG),
		NutsLegumesG:          positive(q.nutsLegumes, nutsLegumesTargetG),
		Omega3EPADHAMg:        positive(total{amount: q.epaDHA.amount * gramsToMilligrams, missing: q.epaDHA.missing}, epaDHATargetMg),
		UnsaturatedFatKcalPct: positive(fatPct(q.unsaturated, q.energy), unsaturatedTargetPct),
		AddedSugarG:           negative(q.sugar, addedSugarOptimalG, addedSugarPoorG),
		RedProcessedMeatG:     negative(q.meat, meatOptimalG, meatPoorG),
		SodiumMg:              negative(q.sodium, sodiumOptimalMg, sodiumPoorMg),
		TransFatKcalPct:       negative(fatPct(q.transFat, q.energy), transFatOptimalPct, transFatPoorPct),
	}
}

func qualityScore(b model.NutritionQualityBreakdown) *float64 {
	values := []*float64{b.NonStarchyVegG, b.WholeFruitG, b.FiberG, b.NutsLegumesG, b.Omega3EPADHAMg,
		b.UnsaturatedFatKcalPct, b.AddedSugarG, b.RedProcessedMeatG, b.SodiumMg, b.TransFatKcalPct}
	var sum float64
	for _, value := range values {
		if value == nil {
			return nil
		}
		sum += *value
	}
	score := sum / float64(len(values)) * qualityComponentMax
	return &score
}

func compareMeals(a, b model.NutritionMeal) int {
	if c := a.ConsumedAt.Compare(b.ConsumedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID.String(), b.ID.String())
}

// Timing supplies an optional measured bedtime and the captured meal clock times.
type Timing struct {
	Bedtime    *time.Time
	LocalTimes map[uuid.UUID]time.Time
}

// UnscoredDaily retains the score response shape when there are no available scores.
func UnscoredDaily() model.NutritionScores {
	return model.NutritionScores{ScoreVersion: Version, MealGlucoseScores: []model.NutritionMealGlucoseScore{}}
}

// Daily computes the three day scores and the points available for display.
func Daily(meals []model.NutritionMeal, calorieTarget float64, glucose map[uuid.UUID]model.NutritionMealGlucoseScore,
	timing *Timing) model.NutritionScores {
	result := UnscoredDaily()
	ordered := slices.Clone(meals)
	slices.SortFunc(ordered, compareMeals)
	var q qualityTotals
	var weighted, coveredKcal, lateKcal float64
	for _, m := range ordered {
		var mealEnergy total
		for _, item := range m.Items {
			q.add(item)
			mealEnergy.add(energy(item))
		}
		if g, ok := glucose[m.ID]; ok {
			result.MealGlucoseScores = append(result.MealGlucoseScores, g)
			if !mealEnergy.missing && mealEnergy.amount > 0 {
				weighted += mealEnergy.amount * g.GlucoseScore
				coveredKcal += mealEnergy.amount
			}
		}
		if timing != nil && isLate(m.ConsumedAt, timing.LocalTimes[m.ID], timing.Bedtime) {
			lateKcal += mealEnergy.amount
		}
	}
	if coveredKcal > 0 {
		day := clamp(weighted/coveredKcal, dayScoreMax)
		result.GlucoseScore = &day
	}
	if calorieTarget <= 0 || !validAmount(calorieTarget) || roundCalories(q.energy.amount) < roundCalories(minimumLoggedFraction*calorieTarget) {
		return result
	}
	result.Breakdown = q.breakdown()
	result.QualityScore = qualityScore(result.Breakdown)
	if result.QualityScore == nil {
		return result
	}
	final := *result.QualityScore
	if result.GlucoseScore != nil {
		final = qualityWeight*final + glucoseWeight*(*result.GlucoseScore)
	}
	if timing != nil && !q.energy.missing && roundCalories(lateKcal) > roundCalories(lateCalorieFraction*q.energy.amount) {
		result.ChrononutritionPenalty = latePenalty
		final -= latePenalty
	}
	final = clamp(final, dayScoreMax)
	result.NutritionScore = &final
	return result
}

func isLate(consumed, local time.Time, bedtime *time.Time) bool {
	if bedtime != nil {
		untilBed := bedtime.Sub(consumed)
		return untilBed > 0 && untilBed < lateBeforeBed
	}
	return !local.IsZero() && local.After(time.Date(local.Year(), local.Month(), local.Day(), lateHour, 0, 0, 0, local.Location()))
}
