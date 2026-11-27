package scoring

import (
	"math"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/wearables"
)

// ValidGoal reports whether PATCH /v1/me accepts the goal (D304: five values; null is handled by the caller).
func ValidGoal(goal string) bool {
	switch goal {
	case "health", "endurance", "strength", "body_composition", "performance":
		return true
	}
	return false
}

// ValidSex reports whether PATCH /v1/me accepts the sex (closed: female, male, unspecified).
func ValidSex(sex string) bool {
	switch sex {
	case "female", "male", "unspecified":
		return true
	}
	return false
}

// ValidWeightKG reports whether a weigh-in is in the accepted 25–350 kg (NaN is not).
func ValidWeightKG(kg float64) bool { return kg >= 25 && kg <= 350 }

// RoundWeightKG rounds a weight to one decimal, half away from zero, as User.weight_kg serves it.
func RoundWeightKG(kg float64) float64 { return math.Round(kg*10) / 10 }

// WeighIn is one body-weight reading: manual (PATCH /v1/me) or healthkit (a body_mass sample).
type WeighIn struct {
	KG         float64
	Source     string
	MeasuredAt time.Time
}

// CurrentWeighIn picks the newest reading, preferring the manual entry on a tie.
func CurrentWeighIn(manual, healthkit *WeighIn) *WeighIn {
	winner := manual
	if winner == nil || healthkit != nil && healthkit.MeasuredAt.After(winner.MeasuredAt) {
		winner = healthkit
	}
	if winner == nil {
		return nil
	}
	result := *winner
	result.KG = RoundWeightKG(result.KG)
	return &result
}

// NutritionGoalFor returns the Nutrition picker default for the app-wide goal.
func NutritionGoalFor(goal string, cyclingDominant bool) (nutritionGoal string, ok bool) {
	switch goal {
	case "endurance":
		if cyclingDominant {
			return periodization.CyclingRace, true
		}
		return periodization.Marathon, true
	case "strength":
		return periodization.Hypertrophy, true
	case "body_composition":
		return periodization.FatLoss, true
	}
	return "", false
}

// DominantIsCycling reports whether a single activity has the most minutes and
// the provider's activity label is a cycling variant.
func DominantIsCycling(minutesByActivity map[string]float64) bool {
	highest := math.Inf(-1)
	var dominant string
	for activity, minutes := range minutesByActivity {
		switch {
		case minutes > highest:
			highest, dominant = minutes, activity
		case minutes == highest:
			dominant = ""
		}
	}
	return strings.EqualFold(dominant, "ride") || wearables.Sport(dominant) == "cycling"
}
