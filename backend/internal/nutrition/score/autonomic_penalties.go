package score

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// DefaultCaffeineHalfH is the standard decay assumption in hours.
const DefaultCaffeineHalfH = 5.0

const (
	lateWindowH                 = 4.0
	maxAlcoholPenalty           = 25.0
	alcoholInflection           = .35
	maxCaffeinePenalty          = 15.0
	maxLatePenalty              = 12.0
	maxAutoPenalty              = 45.0
	caffeineOnset               = 25.0
	caffeineSpan                = 100.0
	lateSaturatedFatTagFraction = .10
	alcoholHighDoseScale        = .50
	alcoholBasePoints           = 8.0
	alcoholHighDosePoints       = 10.0
	alcoholFemaleMultiplier     = 1.15
	alcoholTimingFloor          = .55
	alcoholTimingLossPerHour    = .06
	lateLeanFatLimitG           = 10.0
	lateLeanProteinLimitG       = 40.0
	lateLeanProteinExemptShare  = .70
	latePenaltyScale            = 24.0
	lateTimingExponent          = 1.3
	lateSaturatedFatWeight      = .5
	lateSugarWeight             = .4
)

func hoursToBed(bed, when time.Time) float64 { return max(0, bed.Sub(when).Hours()) }

func alcoholPenalty(dose, beforeBed float64, sex string) float64 {
	factor := 1.0
	if sex == "female" {
		factor = alcoholFemaleMultiplier
	}
	doseEffect := alcoholBasePoints*dose/alcoholInflection + alcoholHighDosePoints*max(0, (dose-alcoholInflection)/alcoholHighDoseScale)
	return clamp(doseEffect*factor*max(alcoholTimingFloor, 1-alcoholTimingLossPerHour*beforeBed), maxAlcoholPenalty)
}

func caffeineResidual(dose, hours, halfLife float64) float64 {
	return dose * math.Exp2(-hours/halfLife)
}

func lateMeal(m model.NutritionMeal, hours, target float64, strengthDay bool) (points float64, saturated bool) {
	if hours >= lateWindowH || target <= 0 {
		return 0, false
	}
	var kcal, fat, protein, sat, sugar total
	for _, item := range m.Items {
		kcal.add(energy(item))
		fat.add(nutrient(item, "fat", "g"))
		protein.add(nutrient(item, "protein", "g"))
		sat.add(nutrient(item, "saturated_fat", "g"))
		sugar.add(addedSugar(item))
	}
	if kcal.amount <= 0 {
		return 0, false
	}
	adjusted := kcal.amount
	if strengthDay && !fat.missing && fat.amount < lateLeanFatLimitG {
		adjusted = max(0, adjusted-lateLeanProteinExemptShare*min(lateLeanProteinLimitG, protein.amount)*proteinKcalPerGram)
	}
	fatFraction := sat.amount * fatKcalPerGram / kcal.amount
	sugarFraction := sugar.amount * carbKcalPerGram / kcal.amount
	return latePenaltyScale * math.Pow((lateWindowH-hours)/lateWindowH, lateTimingExponent) * (adjusted / target) * (1 + lateSaturatedFatWeight*fatFraction + lateSugarWeight*sugarFraction), fatFraction >= lateSaturatedFatTagFraction
}

func autonomic(out *model.N2RScore, meals []model.NutritionMeal, p model.N2RProfile, strengthDay bool, alcoholSensitivity float64) {
	var lastAlcohol time.Time
	var alcoholG, residual float64
	var caffeineObserved, lateSat bool
	for _, m := range meals {
		for _, item := range m.Items {
			if a, ok := nutrient(item, "alcohol", "g"); ok && a > 0 {
				alcoholG += a
				lastAlcohol = maxTime(lastAlcohol, m.ConsumedAt)
			}
			if out.Bedtime != nil && !m.ConsumedAt.After(*out.Bedtime) {
				if c, ok := nutrient(item, "caffeine", "mg"); ok {
					caffeineObserved = true
					residual += caffeineResidual(c, hoursToBed(*out.Bedtime, m.ConsumedAt), out.CaffeineHalfH)
				}
			}
		}
		if out.Bedtime != nil {
			points, highSat := lateMeal(m, hoursToBed(*out.Bedtime, m.ConsumedAt), out.Budget.EnergyTarget, strengthDay)
			out.LateMealPenalty += points
			lateSat = lateSat || highSat
		}
	}
	if p.BodyMassKG > 0 {
		out.AlcoholGKG = alcoholG / p.BodyMassKG
	}
	if out.AlcoholGKG > alcoholInflection {
		out.Tags = append(out.Tags, "alcohol_above_035gkg")
	}
	if out.Bedtime != nil && !lastAlcohol.IsZero() {
		if alcoholSensitivity < .8 || alcoholSensitivity > 1.5 {
			alcoholSensitivity = 1
		}
		out.AlcoholPenalty = clamp(alcoholPenalty(out.AlcoholGKG, hoursToBed(*out.Bedtime, lastAlcohol), p.Sex)*alcoholSensitivity, maxAlcoholPenalty)
	}
	if caffeineObserved {
		out.CaffeineBedMg = numberPtr(residual)
		out.CaffeinePenalty = clamp(maxCaffeinePenalty*(residual-caffeineOnset)/caffeineSpan, maxCaffeinePenalty)
		if residual > caffeineOnset {
			out.Tags = append(out.Tags, "caffeine_bed_high")
		}
	}
	if lateSat {
		out.Tags = append(out.Tags, "late_satfat_meal")
	}
	out.LateMealPenalty = min(maxLatePenalty, out.LateMealPenalty)
	out.AutoPenalty = min(maxAutoPenalty, out.AlcoholPenalty+out.CaffeinePenalty+out.LateMealPenalty)
}
