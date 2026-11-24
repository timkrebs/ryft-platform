package score

import (
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

const n2rVersion = "1.0.0"

// N2RInput groups one intake day's evidence and relevant training and diet history.
type N2RInput struct {
	Profile            model.N2RProfile
	BaseKcal           float64
	Start, End         time.Time
	Bedtime            *time.Time
	Meals, PriorMeals  []model.NutritionMeal
	Workouts           []model.Workout
	Glucose            map[uuid.UUID]model.NutritionMealGlucoseScore
	Omega              []OmegaDay
	CalibratedHalfH    float64
	Coaching           *model.CoachingPlan
	AlcoholSensitivity float64
	CoachingHalfH      *float64
}

func numberPtr(v float64) *float64 { return &v }

func unlock(kcal, target float64) bool {
	return target > 0 && validAmount(target) && roundCalories(kcal) >= roundCalories(minimumLoggedFraction*target)
}

// N2R computes contextual recovery inputs on immutable meal snapshots.
func N2R(in N2RInput) model.N2RScore {
	p := in.Profile
	b, windows, strengthDay := workoutBudget(p, in.BaseKcal, in.Workouts, in.Start, in.End)
	if in.Coaching != nil && in.Coaching.EnergyKcal > 0 {
		b.EnergyTarget = in.Coaching.EnergyKcal
		b.CarbTarget = in.Coaching.CarbsG
		b.WorkoutEnergy = in.Coaching.NetExercise
	}
	out := model.N2RScore{Version: n2rVersion, Budget: b, Bedtime: in.Bedtime,
		CaffeineHalfH: max(orDefault(p.CaffeineHalfH, DefaultCaffeineHalfH), in.CalibratedHalfH), Tags: []string{}}
	if in.CoachingHalfH != nil && p.CaffeineHalfH == nil {
		out.CaffeineHalfH = *in.CoachingHalfH
	}
	meals := slices.Clone(in.Meals)
	slices.SortFunc(meals, compareMeals)
	var quality qualityTotals
	var carb total
	for _, m := range meals {
		for _, item := range m.Items {
			quality.add(item)
			carb.add(carbs(item))
		}
	}
	out.LoggedKcal, out.LoggedCarb = quality.energy.amount, carb.amount
	out.CarbComplete = !carb.missing && len(meals) > 0
	out.FuelMultiplier = fuelMultiplier(b, out.LoggedKcal, carb)
	if out.FuelMultiplier < 1 {
		out.Tags = append(out.Tags, "underfueled_high_strain")
	}
	coveredCarb, sugar, hit := refuel(meals, in.PriorMeals, windows)
	out.CoveredCarb, out.CoveredSugar = coveredCarb, sugar
	if hit {
		out.Tags = append(out.Tags, "post_workout_refeed_hit")
	}
	quality.sugar.amount = max(0, quality.sugar.amount-sugar)
	if !quality.sugar.missing && len(meals) > 0 {
		out.NetSugar = numberPtr(quality.sugar.amount)
	}
	omega := slices.Clone(in.Omega)
	if unlock(out.LoggedKcal, b.EnergyTarget) && !quality.epaDHA.missing {
		omega = append(omega, OmegaDay{Age: 0, Mg: quality.epaDHA.amount * gramsToMilligrams})
	}
	out.OmegaDepot = omegaEWMA(omega)
	out.Glucose = contextualGlucose(meals, in.Glucose, windows, in.Bedtime)
	autonomic(&out, meals, p, strengthDay, in.AlcoholSensitivity)
	if !unlock(out.LoggedKcal, b.EnergyTarget) {
		return out
	}
	out.Components = quality.breakdown()
	out.Components.Omega3EPADHAMg = omegaPoints(out.OmegaDepot)
	out.Components.SodiumMg = negative(quality.sodium, sodiumOptimalMg+b.SodiumExemption, sodiumPoorMg+b.SodiumExemption)
	out.Quality = qualityScore(out.Components)
	if out.Quality == nil {
		return out
	}
	combined := *out.Quality * out.FuelMultiplier
	if out.Glucose != nil {
		combined = qualityWeight*combined + glucoseWeight*(*out.Glucose)
	}
	out.Score = numberPtr(clamp(combined-out.AutoPenalty, dayScoreMax))
	return out
}
