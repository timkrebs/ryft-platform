package score

import (
	"math"
	"slices"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	omegaDays             = 14
	omegaBaseline         = 250.0
	omegaOptimum          = 1000.0
	omegaBaselinePoints   = 7.0
	proteinKcalPerGram    = 4
	carbKcalPerGram       = 4
	adequateFuel          = .75
	lowFuel               = .50
	minimumFuelMultiplier = .85
	highGI                = 70.0
)

// OmegaDay carries a calorie-qualified, complete intake observation and its age in calendar days.
type OmegaDay struct {
	Age int
	Mg  float64
}

func omegaEWMA(days []OmegaDay) *float64 {
	var weighted, weights float64
	for _, d := range days {
		if d.Age < 0 || d.Age >= omegaDays || !validAmount(d.Mg) {
			continue
		}
		weight := math.Pow(1-2.0/(omegaDays+1), float64(d.Age))
		weighted += weight * d.Mg
		weights += weight
	}
	if weights == 0 {
		return nil
	}
	return numberPtr(weighted / weights)
}

func omegaPoints(mg *float64) *float64 {
	if mg == nil {
		return nil
	}
	if *mg <= omegaBaseline {
		return numberPtr(omegaBaselinePoints * *mg / omegaBaseline)
	}
	return numberPtr(min(qualityComponentMax, omegaBaselinePoints+(qualityComponentMax-omegaBaselinePoints)*(*mg-omegaBaseline)/(omegaOptimum-omegaBaseline)))
}

// EPAIntake excludes under-logged days and days with incomplete EPA or DHA snapshots.
func EPAIntake(meals []model.NutritionMeal, target float64) *float64 {
	var kcal, omega total
	for _, meal := range meals {
		for _, item := range meal.Items {
			kcal.add(energy(item))
			omega.add(combined(item, "epa", "dha"))
		}
	}
	if unlock(kcal.amount, target) && !omega.missing {
		return numberPtr(omega.amount * gramsToMilligrams)
	}
	return nil
}

func carbs(item model.NutritionMealItem) (float64, bool) {
	if v, ok := nutrient(item, "carbohydrates", "g"); ok {
		return v, true
	}
	return nutrient(item, "carbohydrates_total", "g")
}

func refuel(meals, prior []model.NutritionMeal, windows []refuelWindow) (carbsCovered, sugarCovered float64, hit bool) {
	type event struct {
		meal    model.NutritionMeal
		current bool
	}
	timeline := make([]event, 0, len(meals)+len(prior))
	for _, m := range prior {
		timeline = append(timeline, event{m, false})
	}
	for _, m := range meals {
		timeline = append(timeline, event{m, true})
	}
	slices.SortFunc(timeline, func(a, b event) int { return compareMeals(a.meal, b.meal) })
	for _, e := range timeline {
		m := e.meal
		for _, item := range m.Items {
			sugar, known := addedSugar(item)
			if !known {
				sugar = 0
			}
			eligible := sugar
			if gi, ok := nutrient(item, "glycemic_index", "index"); ok && gi >= highGI {
				if c, ok := carbs(item); ok {
					eligible = max(eligible, c)
				}
			}
			if eligible <= 0 {
				continue
			}
			remaining := eligible
			for i := range windows {
				w := &windows[i]
				if !inside(m.ConsumedAt, w.start, w.end) {
					continue
				}
				used := min(remaining, w.remaining)
				remaining -= used
				w.remaining -= used
				if e.current && used > 0 {
					carbsCovered += used
					sugarCovered += used / eligible * sugar
					hit = hit || w.remaining == 0
				}
				if remaining <= 0 {
					break
				}
			}
		}
	}
	return
}

func fuelMultiplier(b model.N2RBudget, kcal float64, c total) float64 {
	if b.Strain != "high" && b.Strain != "ultra" || b.EnergyTarget <= 0 {
		return 1
	}
	adequacy := kcal / b.EnergyTarget
	if !c.missing && b.CarbTarget > 0 {
		adequacy = min(adequacy, c.amount/b.CarbTarget)
	}
	fraction := clamp((adequacy-lowFuel)/(adequateFuel-lowFuel), 1)
	return minimumFuelMultiplier + (1-minimumFuelMultiplier)*fraction
}
