package score

import (
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Stable guidance codes used by the Nutrition API and catalog filters.
const (
	GuideEndurance = "POST_WORKOUT_ENDURANCE_REFEED"
	GuideStrength  = "POST_WORKOUT_STRENGTH_PRESLEEP"
	GuideCaffeine  = "CAFFEINE_CUTOFF_WARNING"
	GuideLate      = "LATE_MEAL_COMPOSITION_GUARD"
	GuideAlcohol   = "ALCOHOL_MITIGATION"
	GuideRed       = "RED_RECOVERY_PROTOCOL"
)

// GuidanceInput is the timestamped context used by the guidance rules.
type GuidanceInput struct {
	Now                 time.Time
	Score               model.N2RScore
	Profile             model.N2RProfile
	Workouts            []model.Workout
	Meals               []model.NutritionMeal
	Morning             *model.N2RBiometrics
	SleepLatencyMinutes *float64
}

// Guidance produces cards in urgency order using only timestamped account inputs.
func Guidance(in GuidanceInput) []model.N2RGuidanceCard {
	cards := []model.N2RGuidanceCard{}
	add := func(code string, targets map[string]float64, actions ...string) {
		cards = append(cards, model.N2RGuidanceCard{Code: code, Targets: targets, Actions: actions, Foods: []model.FoodSummary{}})
	}
	var toBed time.Duration
	bedKnown := in.Score.Bedtime != nil && !in.Now.After(*in.Score.Bedtime)
	if bedKnown {
		toBed = in.Score.Bedtime.Sub(in.Now)
	}
	if bedKnown && toBed < 4*time.Hour && in.Score.AlcoholGKG >= .25 {
		add(GuideAlcohol, nil, "Stop drinking alcohol for the evening.", "Drink water or an electrolyte beverage.", "Allow more time for sleep if your schedule permits.")
	}
	var refeed, highStrength bool
	for _, w := range in.Workouts {
		if w.DuplicateOf != nil || !w.Start.Before(w.End) || w.End.After(in.Now) {
			continue
		}
		refeed = refeed || endurance(w) && w.End.Sub(w.Start) > time.Hour && in.Now.Sub(w.End) <= time.Hour
		highStrength = highStrength || strength(w) && intense(w)
	}
	if refeed && in.Profile.BodyMassKG > 0 {
		kg := in.Profile.BodyMassKG
		add(GuideEndurance, map[string]float64{"carb_min_g": .8 * kg, "carb_max_g": 1.2 * kg, "optional_protein_min_g": .2 * kg,
			"optional_protein_max_g": .4 * kg, "fluid_l_per_kg_lost": 1.5, "fluid_sodium_mg_per_l": orDefault(in.Profile.SweatSodiumMgL, defaultSweatSodium)},
			"Refuel with fast-digesting carbohydrates. Add protein if it fits your meal.", "Base fluid replacement on measured workout weight loss, when available.")
	}
	b := in.Morning
	if b != nil && positiveObservation(b.SWC) && b.RMSSDDelta != nil && *b.RMSSDDelta <= -moderateSWC*(*b.SWC) && b.RHRDelta != nil && *b.RHRDelta >= rhrAlertDelta {
		add(GuideRed, map[string]float64{"caffeine_delay_min": 90, "caffeine_delay_max": 120},
			"If caffeine is part of your routine, try having it 90–120 minutes after waking.", "Plan a larger share of today's food earlier in the day.")
		if in.SleepLatencyMinutes != nil && *in.SleepLatencyMinutes >= 30 {
			cards[len(cards)-1].Actions = append(cards[len(cards)-1].Actions, "If you are considering a pre-sleep supplement, discuss alpha-lactalbumin or glycine as optional choices.")
			cards[len(cards)-1].Targets["optional_alpha_lactalbumin_g"] = 21
			cards[len(cards)-1].Targets["optional_glycine_g"] = 3
			cards[len(cards)-1].Targets["optional_before_bed_min_h"] = 1
			cards[len(cards)-1].Targets["optional_before_bed_max_h"] = 3
		}
	}
	if highStrength && bedKnown && toBed >= time.Hour && toBed <= 3*time.Hour {
		add(GuideStrength, map[string]float64{"protein_min_g": 30, "protein_max_g": 40}, "Choose a low-fat, unsweetened slow-digesting protein such as plain skyr or low-fat quark.")
	}
	if bedKnown {
		cutoff := 9 * time.Hour
		var priorDose float64
		for _, m := range in.Meals {
			if m.ConsumedAt.After(in.Now) {
				continue
			}
			var mealDose float64
			for _, i := range m.Items {
				if c, ok := nutrient(i, "caffeine", "mg"); ok {
					mealDose += c
				}
			}
			priorDose = max(priorDose, mealDose)
		}
		if in.Score.CaffeineHalfH >= 6.5 || priorDose > 200 {
			cutoff = 13 * time.Hour
		}
		if toBed <= cutoff {
			add(GuideCaffeine, map[string]float64{"bedtime_residual_min_mg": 25, "bedtime_residual_max_mg": 50}, "Additional caffeine could raise the amount still active at bedtime. Try decaf or herbal tea.")
		}
		if toBed <= 210*time.Minute && in.Score.Budget.EnergyTarget > 0 && in.Score.LoggedKcal < .70*in.Score.Budget.EnergyTarget {
			add(GuideLate, nil, "If you're still hungry, choose an easy-to-digest meal with lean protein and non-starchy vegetables.")
		}
	}
	return cards
}
