package score

import (
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	refuelGlucoseWeight = .2
	lateGlucoseWeight   = 1.5
)

func contextualGlucose(meals []model.NutritionMeal, readings map[uuid.UUID]model.NutritionMealGlucoseScore, windows []refuelWindow, bed *time.Time) *float64 {
	var weighted, weights float64
	for _, m := range meals {
		impact, ok := readings[m.ID]
		if !ok || !validAmount(impact.GlucoseScore) {
			continue
		}
		var kcal total
		for _, item := range m.Items {
			kcal.add(energy(item))
		}
		if kcal.missing || kcal.amount <= 0 {
			continue
		}
		weight := 1.0
		if bed != nil && !m.ConsumedAt.After(*bed) && hoursToBed(*bed, m.ConsumedAt) < lateWindowH {
			weight = lateGlucoseWeight
		}
		for _, w := range windows {
			if inside(m.ConsumedAt, w.start, w.glucoseEnd) {
				weight = refuelGlucoseWeight
				break
			}
		}
		weighted += weight * kcal.amount * impact.GlucoseScore
		weights += weight * kcal.amount
	}
	if weights == 0 {
		return nil
	}
	return numberPtr(clamp(weighted/weights, dayScoreMax))
}
