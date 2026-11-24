package score

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestGlucoseSpikeAndTrapezoids(t *testing.T) {
	start := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	m := model.NutritionMeal{ID: uuid.New(), ConsumedAt: start}
	sample := func(minute int, mg float64) model.NutritionGlucoseSample {
		return model.NutritionGlucoseSample{Timestamp: start.Add(time.Duration(minute) * time.Minute), GlucoseMgDL: mg}
	}
	input := []model.NutritionGlucoseSample{
		sample(90, 120), sample(40, 80), sample(20, 140), sample(0, 100), sample(-5, 110), sample(-15, 90),
		sample(-16, 400), sample(121, 400), sample(21, math.NaN()),
	}
	result := GlucoseByMeal([]model.NutritionMeal{m}, nil, input)[m.ID]
	if result.SampleCount != 4 || !result.WindowEnd.Equal(start.Add(120*time.Minute)) {
		t.Fatalf("window: %+v", result)
	}
	closeTo(t, result.BaselineMgDL, 100)
	closeTo(t, result.PeakDeltaMgDL, 40)
	closeTo(t, result.IAUCMgDLMin, 1300)
	closeTo(t, result.EndDeltaMgDL, 20)
	closeTo(t, result.PeakScore, 50)
	closeTo(t, result.AUCScore, 100*3200.0/3500)
	closeTo(t, result.RecoveryScore, 40)
	closeTo(t, result.GlucoseScore, 63)
}

func TestGlucoseOverlapFallbackAndDuplicateTimestamps(t *testing.T) {
	start := time.Date(2026, 11, 24, 23, 50, 0, 0, time.UTC)
	a := model.NutritionMeal{ID: uuid.New(), ConsumedAt: start}
	b := model.NutritionMeal{ID: uuid.New(), ConsumedAt: start.Add(30 * time.Minute)}
	sample := func(minute int, mg float64) model.NutritionGlucoseSample {
		return model.NutritionGlucoseSample{Timestamp: start.Add(time.Duration(minute) * time.Minute), GlucoseMgDL: mg}
	}
	input := []model.NutritionGlucoseSample{
		sample(5, 100), sample(10, 105), sample(20, 115), sample(20, 125),
		sample(30, 300), sample(35, 290), sample(40, 250),
	}
	out := GlucoseByMeal([]model.NutritionMeal{b, a}, nil, input)
	if len(out) != 2 || out[a.ID].SampleCount != 4 || !out[a.ID].WindowEnd.Equal(b.ConsumedAt) {
		t.Fatalf("windows: %+v", out)
	}
	closeTo(t, out[a.ID].BaselineMgDL, 100)
	closeTo(t, out[a.ID].PeakDeltaMgDL, 200)
	closeTo(t, out[b.ID].BaselineMgDL, 210) // median of 120 and 300
	if len(GlucoseByMeal([]model.NutritionMeal{a}, []time.Time{start.Add(19 * time.Minute)}, input)) != 0 {
		t.Fatal("meal with two distinct samples was scored")
	}
	if len(GlucoseByMeal([]model.NutritionMeal{a}, nil, nil)) != 0 {
		t.Fatal("meal in the absence of CGM data was scored")
	}
}
