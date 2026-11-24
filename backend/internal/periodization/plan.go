package periodization

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
)

// Supported coaching goals and session classes use these stable identifiers.
const (
	Hypertrophy = "HYPERTROPHY"
	Marathon    = "MARATHON"
	CyclingRace = "CYCLING_RACE"
	FatLoss     = "FAT_LOSS_RECOMP"

	Rest     = "REST"
	Zone2    = "ZONE2_LOW"
	Moderate = "MODERATE"
	Key      = "KEY_HIGH_INTENSITY"
	CarbLoad = "TAPER_CARB_LOAD"
	Race     = "RACE"

	kcalCarbsProtein = 4
	kcalFat          = 9
	fatShare         = .20
	fatPerKG         = .60
	maximumDeficit   = 500
)

// Input combines the plan’s goal, evidence and user preferences for a calendar day.
type Input struct {
	Profile                                      model.GoalProfile
	Day                                          time.Time
	Sex                                          string
	Mass, FFM, Base, NetExercise                 float64
	Minutes, PeakReserve, SweatRate, SweatSodium float64
	Session                                      *model.PlannedSession
	LostMass                                     *float64
	Availability                                 model.Availability
	Readiness                                    string
	WeightChange                                 *float64
}

// IsRaceGoal reports whether the goal is eligible for race-day periodization.
func IsRaceGoal(goal string) bool { return goal == Marathon || goal == CyclingRace }

// SessionClass resolves the goal date, session plan and measured intensity.
func SessionClass(in Input) string {
	if in.Profile.RaceDate != nil && IsRaceGoal(in.Profile.Goal) {
		race, err := time.ParseInLocation(time.DateOnly, *in.Profile.RaceDate, in.Day.Location())
		if err == nil {
			if race.Equal(in.Day) {
				return Race
			}
			if race.After(in.Day) && !race.After(in.Day.AddDate(0, 0, 2)) {
				return CarbLoad
			}
		}
	}
	if in.PeakReserve >= .75 || in.Minutes >= 90 {
		return Key
	}
	if in.Session != nil {
		return in.Session.Class
	}
	if in.Minutes >= 45 {
		return Moderate
	}
	if in.Minutes > 0 {
		return Zone2
	}
	return Rest
}

// Plan computes energy and macro targets while enforcing the availability guardrail.
func Plan(in Input) model.CoachingPlan {
	class := SessionClass(in)
	risk := in.Availability.State
	blockDeficit := risk != AvailabilityGreen || class == Race || class == CarbLoad || in.Readiness == ReadinessFatigue
	if in.WeightChange != nil && *in.WeightChange < -.014*in.Mass {
		blockDeficit = true
	}
	out := model.CoachingPlan{SessionClass: class, NetExercise: in.NetExercise, Availability: in.Availability,
		DeficitSuspended: in.Profile.Goal == FatLoss && blockDeficit}
	if in.Base <= 0 || in.Mass <= 0 {
		return out
	}
	kcal := in.Base + energy.Compensation*in.NetExercise
	goal := in.Profile.Goal
	switch goal {
	case Hypertrophy:
		kcal += in.Base * value(in.Profile.GoalAdjustment, .10)
	case FatLoss:
		if !blockDeficit {
			kcal -= min(maximumDeficit, in.Base*value(in.Profile.GoalAdjustment, .15))
		}
	}
	if risk == AvailabilityRed {
		kcal = max(kcal, optimum(in.Sex)*in.FFM+in.NetExercise)
	}
	protein := 1.8 * in.Mass
	if goal == Hypertrophy {
		protein = 2 * in.Mass
	}
	if goal == FatLoss && in.FFM > 0 {
		protein = 2.6 * in.FFM
	}
	carb := carbsPerKG(class, in.Profile.Recreational) * in.Mass
	if in.Readiness == ReadinessFatigue || risk == AvailabilityRed || risk == AvailabilityYellow {
		carb = max(carb, 4.5*in.Mass)
	}
	macroKcal := (carb + protein) * kcalCarbsProtein
	kcal = max(kcal, macroKcal/(1-fatShare), macroKcal+fatPerKG*in.Mass*kcalFat)
	out.TrainLow = in.Profile.TrainLow && risk == AvailabilityGreen && in.Readiness == ReadinessOptimal && class == Zone2
	out.EnergyKcal, out.ProteinG, out.CarbsG, out.FatG = kcal, protein, carb, (kcal-macroKcal)/kcalFat
	out.Fuel = fueling(in, class)
	return out
}

func carbsPerKG(class string, recreational bool) float64 {
	switch class {
	case Zone2:
		return 4.5
	case Moderate:
		return 6
	case Key:
		return 8.5
	case Race:
		return 9
	case CarbLoad:
		if recreational {
			return 9
		}
		return 11
	default:
		return 3.5
	}
}

func fueling(in Input, class string) *model.FuelingPlan {
	minutes := in.Minutes
	if in.Session != nil {
		minutes = max(minutes, in.Session.Minutes)
	}
	if minutes < 60 && class != Race {
		return nil
	}
	f := model.FuelingPlan{}
	switch {
	case minutes > 150:
		f.CarbMinGPerH, f.CarbMaxGPerH, f.GlucoseFructoseRatio = 60, 90, "1:0.8"
		if in.Mass < 60 && !in.Profile.GutTrained {
			f.CarbMaxGPerH = 60
		}
		if in.Profile.GutTrained && in.PeakReserve >= .75 {
			f.CarbMaxGPerH = 120
		}
	case minutes >= 60:
		f.CarbMinGPerH, f.CarbMaxGPerH = 30, 60
	}
	if class == Race {
		lo, hi := in.Mass, 4*in.Mass
		f.PreRaceMinG, f.PreRaceMaxG = &lo, &hi
	}
	loss := in.SweatRate * minutes / 60
	if in.LostMass != nil {
		loss = max(0, *in.LostMass)
	}
	if minutes >= 60 && loss > 0 {
		liters, sodium := 1.5*loss, in.SweatRate*minutes/60*in.SweatSodium
		f.RehydrateL, f.SweatSodiumMg = &liters, &sodium
	}
	return &f
}

// ValidProfile checks supported goals and physiological ranges.
func ValidProfile(p model.GoalProfile) bool {
	switch p.Goal {
	case Hypertrophy, Marathon, CyclingRace, FatLoss:
	default:
		return false
	}
	if p.RaceDate != nil {
		t, err := time.Parse(time.DateOnly, *p.RaceDate)
		if err != nil || t.Year() < 2000 || t.Year() > 2200 || !IsRaceGoal(p.Goal) {
			return false
		}
	}
	return inRange(&p.HeightCM, 120, 230) && inRange(p.BodyFatPct, 3, 65) &&
		inRange(p.FTP, 30, 800) && inRange(p.HRMax, 80, 240) && inRange(p.HRRest, 25, 130) &&
		(p.HRMax == nil || p.HRRest == nil || *p.HRMax > *p.HRRest) && inRange(p.SweatSodium, 200, 2000) &&
		(p.GoalAdjustment == nil || p.Goal == Hypertrophy && inRange(p.GoalAdjustment, .05, .15) ||
			p.Goal == FatLoss && inRange(p.GoalAdjustment, .10, .20))
}

// ValidFeedback checks ranges and plan fields before feedback is stored.
func ValidFeedback(f model.CoachingFeedback) bool {
	if s := f.Session; s != nil {
		if s.Start.IsZero() || len(s.Sport) > 64 || s.Sport == "" || !inRange(&s.Minutes, 1, 1440) {
			return false
		}
		switch s.Class {
		case Rest, Zone2, Moderate, Key:
		default:
			return false
		}
	}
	return inRange(f.WorkoutMinutes, 10, 1440) && inRange(f.PreMass, 25, 350) && inRange(f.PostMass, 25, 350) &&
		inRange(f.FluidL, 0, 20) && inRange(f.UrineL, 0, 20) && inRange(f.SweatPatch, 200, 2000) &&
		inRange(f.IntraCarbs, 0, 1500) && inRange(f.GIDistress, 0, 10) && inRange(f.RPE, 0, 10) && inRange(f.Fatigue, 0, 10) &&
		inRange(f.MeasuredRMR, 600, 5000) && inRange(f.CycleWaterKG, 0, 4) && f.TrackedCycles >= 0 && f.TrackedCycles <= 100 &&
		(f.CycleWaterKG == nil || f.TrackedCycles >= 2)
}

func inRange(v *float64, lo, hi float64) bool {
	return v == nil || (!math.IsNaN(*v) && *v >= lo && *v <= hi)
}
func value(v *float64, d float64) float64 {
	if v != nil {
		return *v
	}
	return d
}
