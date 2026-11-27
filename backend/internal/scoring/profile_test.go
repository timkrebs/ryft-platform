package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func TestProfileEnums(t *testing.T) {
	for _, tt := range []struct {
		name  string
		valid func(string) bool
		good  []string
		bad   []string
	}{
		{"goal", ValidGoal, []string{"health", "endurance", "strength", "body_composition", "performance"}, []string{"", "Strength", "x"}},
		{"sex", ValidSex, []string{"female", "male", "unspecified"}, []string{"", "Female", "other"}},
	} {
		for _, v := range tt.good {
			if !tt.valid(v) {
				t.Errorf("valid %s rejected: %q", tt.name, v)
			}
		}
		for _, v := range tt.bad {
			if tt.valid(v) {
				t.Errorf("invalid %s accepted: %q", tt.name, v)
			}
		}
	}
}

func TestValidWeightKG(t *testing.T) {
	for _, tt := range []struct {
		kg   float64
		want bool
	}{
		{24.9, false}, {25, true}, {350, true}, {350.1, false},
		{math.NaN(), false}, {math.Inf(1), false}, {math.Inf(-1), false},
	} {
		if got := ValidWeightKG(tt.kg); got != tt.want {
			t.Errorf("ValidWeightKG(%v) = %v, want %v", tt.kg, got, tt.want)
		}
	}
}

func TestRoundWeightKG(t *testing.T) {
	for _, tt := range []struct{ kg, want float64 }{
		{25, 25}, {350, 350}, {64.45, 64.5}, {64.44, 64.4}, {78.25, 78.3}, {-78.25, -78.3},
	} {
		if got := RoundWeightKG(tt.kg); got != tt.want {
			t.Errorf("RoundWeightKG(%v) = %v, want %v", tt.kg, got, tt.want)
		}
	}
}

func TestCurrentWeighIn(t *testing.T) {
	older := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	manual := WeighIn{64.45, model.WeightSourceManual, older}
	healthkit := WeighIn{78.25, model.WeightSourceHealthKit, older}
	newManual, newHealthKit := manual, healthkit
	newManual.MeasuredAt, newHealthKit.MeasuredAt = newer, newer
	for _, tt := range []struct {
		name              string
		manual, healthkit *WeighIn
		want              *WeighIn
	}{
		{"manual only", &manual, nil, &WeighIn{64.5, model.WeightSourceManual, older}},
		{"HealthKit only", nil, &healthkit, &WeighIn{78.3, model.WeightSourceHealthKit, older}},
		{"newer HealthKit", &manual, &newHealthKit, &WeighIn{78.3, model.WeightSourceHealthKit, newer}},
		{"newer manual", &newManual, &healthkit, &WeighIn{64.5, model.WeightSourceManual, newer}},
		{"tie", &manual, &healthkit, &WeighIn{64.5, model.WeightSourceManual, older}},
		{"neither", nil, nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := CurrentWeighIn(tt.manual, tt.healthkit)
			if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
				t.Errorf("CurrentWeighIn = %+v, want %+v", got, tt.want)
			}
		})
	}
	if manual.KG != 64.45 || healthkit.KG != 78.25 || newManual.KG != 64.45 || newHealthKit.KG != 78.25 {
		t.Error("CurrentWeighIn changed an input")
	}
}

func TestNutritionGoalFor(t *testing.T) {
	for _, tt := range []struct {
		goal, want string
		cycling    bool
	}{
		{"health", "", false}, {"health", "", true},
		{"endurance", "MARATHON", false}, {"endurance", "CYCLING_RACE", true},
		{"strength", "HYPERTROPHY", false}, {"strength", "HYPERTROPHY", true},
		{"body_composition", "FAT_LOSS_RECOMP", false}, {"body_composition", "FAT_LOSS_RECOMP", true},
		{"performance", "", false}, {"performance", "", true},
		{"", "", false}, {"unknown", "", true},
	} {
		if got, ok := NutritionGoalFor(tt.goal, tt.cycling); got != tt.want || ok != (tt.want != "") {
			t.Errorf("NutritionGoalFor(%q, %v) = %q, %v, want %q, %v", tt.goal, tt.cycling, got, ok, tt.want, tt.want != "")
		}
	}
}

func TestDominantIsCycling(t *testing.T) {
	for _, activity := range []string{"cycling", "cycle", "indoorCycling", "road_biking", "mountain_bike", "biking", "ride"} {
		if !DominantIsCycling(map[string]float64{activity: 30, "running": 20}) {
			t.Errorf("%q should count as cycling", activity)
		}
	}
	for _, minutes := range []map[string]float64{
		{},
		{"running": 30, "cycling": 20},
		{"running": 30, "cycling": 30, "walking": 20},
		{"cycling": 30, "biking": 30},
	} {
		if DominantIsCycling(minutes) {
			t.Errorf("no unique cycling leader in %v", minutes)
		}
	}
}
