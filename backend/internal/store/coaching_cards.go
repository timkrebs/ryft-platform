package store

import (
	"time"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
)

func coachingCards(s coachingPlanDay, score model.N2RScore, at time.Time, days []energy.Day, previous model.CoachingPlan) []model.N2RGuidanceCard {
	out := []model.N2RGuidanceCard{}
	add := func(code string, actions []string, targets map[string]float64) {
		out = append(out, model.N2RGuidanceCard{Code: code, Actions: actions, Targets: targets, Foods: []model.FoodSummary{}})
	}
	switch s.Plan.Availability.State {
	case periodization.AvailabilityRed:
		add("LEA_DEFICIT_SUSPENDED", []string{"Pause the calorie deficit and train-low sessions. Discuss persistent under-fueling or symptoms with a sports physician."}, nil)
	case periodization.AvailabilityYellow:
		add("LEA_SUBCLINICAL_CARB_BOOST", []string{"Hold calorie cuts and add carbohydrate around demanding sessions."}, nil)
	}
	if f := s.Plan.Fuel; f != nil {
		targets := map[string]float64{"carbs_min_g_per_h": f.CarbMinGPerH, "carbs_max_g_per_h": f.CarbMaxGPerH}
		if f.PreRaceMinG != nil && f.PreRaceMaxG != nil {
			targets["pre_race_min_g"], targets["pre_race_max_g"] = *f.PreRaceMinG, *f.PreRaceMaxG
		}
		if s.Feedback.Session != nil && !at.Before(s.Feedback.Session.Start.Add(-4*time.Hour)) &&
			at.Before(s.Feedback.Session.Start.Add(time.Duration(s.Feedback.Session.Minutes*float64(time.Minute)))) {
			if f.GlucoseFructoseRatio != "" {
				add("PLANNED_SESSION_FUEL", []string{"Use the practiced fueling plan. For the longer event, the suggested glucose-to-fructose ratio is " + f.GlucoseFructoseRatio + "."}, targets)
			} else {
				add("PLANNED_SESSION_FUEL", []string{"Use the practiced fueling plan for this session."}, targets)
			}
		}
	}
	if score.Bedtime != nil {
		until := score.Bedtime.Sub(at)
		if until >= 3*time.Hour && until < 4*time.Hour {
			add("EVENING_FUEL", []string{"Choose a lighter meal with lean protein if you still need to eat before bed."}, nil)
		}
	}
	if len(days) >= 2 && previous.SessionClass == periodization.CarbLoad {
		last, prior := days[len(days)-1], days[len(days)-2]
		if last.Weight != nil && prior.Weight != nil {
			gain := *last.Weight - *prior.Weight
			if gain >= 1 && gain <= 2 {
				add("GLYCOGEN_WEIGHT_CHANGE", []string{"Carb loading can temporarily increase scale weight through stored glycogen and water."}, map[string]float64{"scale_change_kg": gain})
			}
		}
	}
	return out
}
