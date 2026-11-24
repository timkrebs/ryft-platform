package periodization

import (
	"slices"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
)

// Availability and readiness states describe the evidence and whether protective rules apply.
const (
	AvailabilityUnknown = "UNKNOWN"
	AvailabilityGreen   = "GREEN"
	AvailabilityYellow  = "YELLOW"
	AvailabilityRed     = "RED"

	OptimumMale   = 40
	OptimumFemale = 45
	severeMale    = 25
	severeFemale  = 30
	minCarbsGKG   = 3
	eaWindow      = 7
	eaYellowDays  = 8
	eaRedDays     = 14

	ReadinessUnknown   = "UNKNOWN"
	ReadinessOptimal   = "OPTIMAL"
	ReadinessLifestyle = "ACUTE_LIFESTYLE_SUPPRESSION"
	ReadinessFatigue   = "AUTONOMIC_FATIGUE"
	ReadinessGuardrail = "LEA_GUARDRAIL_ACTIVE"
)

// Recovery carries baseline changes and observations for readiness and availability checks.
type Recovery struct {
	Today, Prior      *model.N2RBiometrics
	YesterdayTags     []string
	Fatigue, RMRRatio *float64
	PersistentHRVDays int
}

func (r Recovery) confounders() []string {
	var found []string
	for _, key := range []string{"alcohol_above_035gkg", "caffeine_bed_high", "late_satfat_meal"} {
		if slices.Contains(r.YesterdayTags, key) {
			found = append(found, key)
		}
	}
	return found
}

// Readiness describes the current signal and any known confounders.
func Readiness(r Recovery, a model.Availability) (string, []string) {
	if a.State == AvailabilityRed || a.State == AvailabilityYellow {
		return ReadinessGuardrail, a.Markers
	}
	if r.Today == nil || r.Prior == nil || r.Today.RMSSDDelta == nil || r.Today.SWC == nil || *r.Today.SWC <= 0 ||
		r.Today.RMSSDCV == nil || r.Prior.RMSSDCV == nil {
		return ReadinessUnknown, []string{}
	}
	if *r.Today.RMSSDDelta <= -*r.Today.SWC && *r.Today.RMSSDCV > *r.Prior.RMSSDCV {
		if tags := r.confounders(); len(tags) > 0 {
			return ReadinessLifestyle, tags
		}
		return ReadinessFatigue, []string{"lnrmssd_below_swc", "lnrmssd_cv_rising"}
	}
	return ReadinessOptimal, []string{}
}

func optimum(sex string) float64 {
	if sex == "male" {
		return OptimumMale
	}
	return OptimumFemale
}

// CheckAvailability evaluates sustained complete intake windows and measured risk markers.
func CheckAvailability(days []energy.Day, mass, ffm float64, sex string, r Recovery) model.Availability {
	a := model.Availability{State: AvailabilityUnknown, Markers: []string{}}
	if mass <= 0 || ffm <= 0 || len(days) < eaWindow {
		return a
	}
	optimal, severe := optimum(sex), float64(severeFemale)
	if sex == "male" {
		severe = severeMale
	}
	median := energy.MedianLogged(days)
	lowDays, severeDays := 0, 0
	for end := len(days); end >= eaWindow; end-- {
		window := days[end-eaWindow : end]
		sum, carbs, known := 0., 0., 0
		carbsKnown, high := 0, false
		for _, d := range window {
			if !energy.CredibleIntake(d, median) {
				continue
			}
			known++
			sum += *d.Intake - d.NetExercise
			if d.Carbs != nil {
				carbs += *d.Carbs
				carbsKnown++
			}
			high = high || d.HighStrain
		}
		if known < eaWindow {
			break
		}
		ea := sum / (float64(eaWindow) * ffm)
		lca := carbs / (float64(eaWindow) * mass)
		if end == len(days) {
			a.Energy = &ea
			if carbsKnown == eaWindow {
				a.Carbs = &lca
			}
			if high && a.Carbs != nil && lca < minCarbsGKG {
				a.State = AvailabilityYellow
				a.Markers = append(a.Markers, "low_carbohydrate_availability")
			}
			if ea >= optimal && a.Carbs != nil && lca >= minCarbsGKG {
				a.State = AvailabilityGreen
			}
		}
		if ea >= optimal {
			break
		}
		lowDays++
		if severeDays == len(days)-end && ea < severe {
			severeDays++
		}
	}
	if lowDays >= eaYellowDays && a.State != AvailabilityYellow {
		a.State = AvailabilityYellow
		a.Markers = append(a.Markers, "persistent_low_energy_availability")
	}
	markers := redMarkers(r)
	if severeDays >= eaRedDays || a.State == AvailabilityYellow && len(markers) >= 2 {
		a.State = AvailabilityRed
		a.Markers = append(a.Markers, markers...)
		if severeDays >= eaRedDays {
			a.Markers = append(a.Markers, "sustained_very_low_energy_availability")
		}
	}
	return a
}

func redMarkers(r Recovery) []string {
	var result []string
	if r.RMRRatio != nil && *r.RMRRatio < .90 {
		result = append(result, "low_measured_rmr")
	}
	if len(r.confounders()) > 0 {
		return result
	}
	if r.PersistentHRVDays >= 3 {
		result = append(result, "persistent_lnrmssd_suppression")
	}
	if r.Today != nil && r.Today.RHRDelta != nil && *r.Today.RHRDelta <= -2 && r.Today.RMSSDDelta != nil &&
		r.Today.SWC != nil && *r.Today.SWC > 0 && *r.Today.RMSSDDelta <= -*r.Today.SWC && r.Fatigue != nil && *r.Fatigue >= 7 {
		result = append(result, "falling_rhr_with_fatigue")
	}
	return result
}
