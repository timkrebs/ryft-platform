package score

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	lowMinutes            = 30
	highMinutes           = 75
	ultraMinutes          = 150
	highIntensityStrain   = 12.0
	highStrengthLoad      = 12.0
	strengthMinutes       = 45
	enduranceMinutes      = 60
	longEnduranceMinutes  = 120
	quickTurnaround       = 10 * time.Hour
	standardRefuel        = 2 * time.Hour
	quickRefuel           = 4 * time.Hour
	defaultSweatRate      = 0.8
	defaultSweatSodium    = 900.0
	meaningfulSodium      = 400.0
	lowCarbGPerKG         = 3.5
	moderateCarbGPerKG    = 5.0
	highCarbGPerKG        = 8.0
	ultraCarbGPerKG       = 10.0
	strengthRefuelGPerKG  = 0.5
	enduranceRefuelGPerKG = 1.0
	longRefuelGPerKG      = 1.5
	quickRefuelGPerKGH    = 1.2
)

type refuelWindow struct {
	start, end, glucoseEnd time.Time
	remaining              float64
}

func orDefault(p *float64, fallback float64) float64 {
	if p != nil && validAmount(*p) {
		return *p
	}
	return fallback
}

func strength(w model.Workout) bool {
	a := strings.ToLower(w.Activity)
	return w.SessionID != nil || strings.Contains(a, "strength") || strings.Contains(a, "weight") || a == "functional_training"
}

func endurance(w model.Workout) bool {
	a := strings.ToLower(w.Activity)
	return slices.Contains([]string{"run", "running", "cycle", "cycling", "ride", "swim", "swimming", "rowing", "rower", "hiking", "endurance", "cross_country_skiing", "xc_skiing", "skiing", "elliptical", "bike", "biking", "triathlon"}, a)
}

func intense(w model.Workout) bool {
	return orDefault(w.Strain, 0) >= highIntensityStrain || strength(w) && orDefault(w.MuscularLoad, 0) >= highStrengthLoad
}

func intervalTraining(w model.Workout) bool {
	return w.Activity == "hiit" || w.Activity == "high_intensity_interval_training"
}

func workoutBudget(p model.N2RProfile, base float64, workouts []model.Workout, start, end time.Time) (model.N2RBudget, []refuelWindow, bool) {
	work := slices.DeleteFunc(slices.Clone(workouts), func(w model.Workout) bool {
		return w.DuplicateOf != nil || !w.Start.Before(w.End)
	})
	slices.SortFunc(work, func(a, b model.Workout) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return cmp.Compare(a.ID.String(), b.ID.String())
	})
	var windows []refuelWindow
	var quick, strengthDay, highEndurance bool
	var b model.N2RBudget
	var segments [][2]time.Time
	for i, w := range work {
		belongs := !w.Start.Before(start) && w.Start.Before(end)
		within := minTime(w.End, end).Sub(maxTime(w.Start, start))
		if within > 0 {
			segments = append(segments, [2]time.Time{maxTime(w.Start, start), minTime(w.End, end)})
		}
		if belongs {
			b.WorkoutEnergy += orDefault(w.EnergyKcal, 0)
			strengthDay = strengthDay || strength(w)
		}
		fast := p.NextWorkoutAt != nil && !p.NextWorkoutAt.Before(w.End) && p.NextWorkoutAt.Sub(w.End) < quickTurnaround
		for _, next := range work[i+1:] {
			if next.Start.Before(w.End) {
				continue
			}
			fast = fast || next.Start.Sub(w.End) < quickTurnaround
			break
		}
		quick = quick || belongs && fast
		mins := w.End.Sub(w.Start).Minutes()
		highEndurance = highEndurance || belongs && endurance(w) && intense(w) && mins >= strengthMinutes
		if !(mins > strengthMinutes && (intense(w) || intervalTraining(w)) || mins > enduranceMinutes && endurance(w)) {
			continue
		}
		period, kg := standardRefuel, strengthRefuelGPerKG
		if endurance(w) {
			kg = enduranceRefuelGPerKG
			if mins >= longEnduranceMinutes {
				kg = longRefuelGPerKG
			}
		}
		if fast {
			period, kg = quickRefuel, quickRefuelGPerKGH*quickRefuel.Hours()
		}
		until := w.End.Add(period)
		if until.Before(start) || !w.Start.Before(end) {
			continue
		}
		budget := p.BodyMassKG * kg
		b.CarbAllowance += budget
		windows = append(windows, refuelWindow{w.Start, until, w.End.Add(standardRefuel), budget})
	}
	var last time.Time
	for _, segment := range segments {
		if segment[1].After(last) {
			b.WorkoutMinutes += segment[1].Sub(maxTime(segment[0], last)).Minutes()
			last = segment[1]
		}
	}
	kg := lowCarbGPerKG
	b.Strain = "low"
	switch {
	case b.WorkoutMinutes > ultraMinutes || quick:
		kg, b.Strain = ultraCarbGPerKG, "ultra"
	case b.WorkoutMinutes >= highMinutes || highEndurance:
		kg, b.Strain = highCarbGPerKG, "high"
	case b.WorkoutMinutes >= lowMinutes:
		kg, b.Strain = moderateCarbGPerKG, "moderate"
	}
	b.CarbTarget = p.BodyMassKG * kg
	if base > 0 && validAmount(base) {
		b.EnergyTarget = base + b.WorkoutEnergy
	}
	b.SweatSodium = orDefault(p.SweatRateLPH, defaultSweatRate) * b.WorkoutMinutes / 60 * orDefault(p.SweatSodiumMgL, defaultSweatSodium)
	if b.SweatSodium >= meaningfulSodium {
		b.SodiumExemption = b.SweatSodium
	}
	slices.SortStableFunc(windows, func(a, b refuelWindow) int { return a.end.Compare(b.end) })
	return b, windows, strengthDay
}

// EnergyTarget applies the same day attribution used by the full strain budget.
func EnergyTarget(base float64, workouts []model.Workout, start, end time.Time) float64 {
	if base <= 0 || !validAmount(base) {
		return 0
	}
	for _, w := range workouts {
		if w.DuplicateOf == nil && w.Start.Before(w.End) && !w.Start.Before(start) && w.Start.Before(end) {
			base += orDefault(w.EnergyKcal, 0)
		}
	}
	return base
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func inside(at, start, end time.Time) bool { return !at.Before(start) && !at.After(end) }
