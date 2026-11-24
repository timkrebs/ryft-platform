package energy

import (
	"math"
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// DefaultTissueDensity and BaselineActivity seed the model before observations.
const (
	DefaultTissueDensity   = 7700
	BaselineActivity       = 1.35
	weightAlpha            = .10
	maxWeighInGap          = 7
	adaptiveDays           = 14
	minLoggedDays          = 11
	logThreshold           = .50
	dailyChange            = 100
	priorBounds            = .40
	phaseDays              = 28
	phaseObservationWeight = .20
)

// Day holds the dated observations used by the rolling energy model.
type Day struct {
	Date                                 time.Time
	Weight                               *float64
	WaterOffset                          float64
	Intake, Carbs, Protein, Fat          *float64
	NetExercise, Load, PowerKcal         float64
	Fasting, IntakeConfirmed, HighStrain bool
}

// Adaptive reports the prior, latest estimate and evidence for one forecast.
type Adaptive struct {
	Prior, Observed, TDEE, Base, Density float64
	Adapted                              bool
	Confidence                           float64
	IntakeMean, ExerciseMean             float64
	WeightChange                         *float64
}

// Trend returns an EWMA only on days with observed or bounded interpolated weights.
// Water offset is supplied from a separately established personal cycle history.
func Trend(days []Day) []*float64 {
	raw := make([]*float64, len(days))
	last := -1
	for i, d := range days {
		if d.Weight == nil || *d.Weight < 25 || *d.Weight > 350 {
			continue
		}
		value := *d.Weight - d.WaterOffset
		raw[i] = &value
		if last >= 0 && i-last-1 <= maxWeighInGap {
			for j := last + 1; j < i; j++ {
				v := *raw[last] + (value-*raw[last])*float64(j-last)/float64(i-last)
				raw[j] = &v
			}
		}
		last = i
	}
	out := make([]*float64, len(days))
	var running float64
	for i, v := range raw {
		if v == nil {
			continue
		}
		if i == 0 || raw[i-1] == nil {
			running = *v
		} else {
			running += weightAlpha * (*v - running)
		}
		copy := running
		out[i] = &copy
	}
	return out
}

// MedianLogged returns the median on represented non-fasting days.
func MedianLogged(days []Day) float64 {
	values := make([]float64, 0, len(days))
	for _, d := range days {
		if d.Intake != nil && *d.Intake > 0 && !d.Fasting {
			values = append(values, *d.Intake)
		}
	}
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	n := len(values)
	return (values[(n-1)/2] + values[n/2]) / 2
}

// CredibleIntake applies the incomplete-log cutoff and explicit fasting exception.
func CredibleIntake(d Day, median float64) bool {
	return d.Intake != nil && *d.Intake >= 0 && (d.Fasting || *d.Intake > 0 && *d.Intake >= median*logThreshold)
}

// Learn consumes consecutive local days ending with the forecast day. Only
// completed days enter the observed intake window. PreviousTDEE is a frozen
// prior-day prediction; initial forecasts start at the physiological prior.
func Learn(days []Day, prior, previousTDEE, density float64, switched time.Time) Adaptive {
	if density < 5000 || density > 10000 {
		density = DefaultTissueDensity
	}
	out := Adaptive{Prior: prior, TDEE: prior, Base: prior, Density: density}
	if prior <= 0 || len(days) <= adaptiveDays {
		return out
	}
	if previousTDEE > 0 {
		out.TDEE = previousTDEE
		out.Base = previousTDEE
	}
	i := len(days) - 1
	window := days[i-adaptiveDays : i]
	trend := Trend(days)
	median := MedianLogged(window)
	count, confirmed := 0, 0
	for _, d := range window {
		out.ExerciseMean += d.NetExercise / float64(adaptiveDays)
		if !CredibleIntake(d, median) {
			continue
		}
		count++
		out.IntakeMean += *d.Intake
		if d.IntakeConfirmed || d.Fasting {
			confirmed++
		}
	}
	out.Confidence = float64(confirmed) / float64(adaptiveDays)
	if count > 0 {
		out.IntakeMean /= float64(count)
	}
	if count < minLoggedDays || trend[i-adaptiveDays] == nil || trend[i] == nil || slices.Contains(trend[i-adaptiveDays:i+1], nil) {
		if previousTDEE > 0 {
			out.Base = max(0, previousTDEE-out.ExerciseMean*Compensation)
		}
		return out
	}
	delta := *trend[i] - *trend[i-adaptiveDays]
	out.WeightChange = &delta
	out.Observed = out.IntakeMean - delta*density/float64(adaptiveDays)
	weight := 1.0
	if !switched.IsZero() && !days[i].Date.Before(switched) && days[i].Date.Before(switched.AddDate(0, 0, phaseDays)) {
		weight = phaseObservationWeight
	}
	out.TDEE += math.Max(-dailyChange, math.Min(dailyChange, (out.Observed-out.TDEE)*weight))
	out.TDEE = max(prior*(1-priorBounds), min(prior*(1+priorBounds), out.TDEE))
	out.Base = max(0, out.TDEE-out.ExerciseMean*Compensation)
	out.Adapted = true
	return out
}

// Load reports seven-day load versus the weekly average of four weeks.
func Load(days []Day) model.CoachingLoad {
	var out model.CoachingLoad
	for i, d := range days {
		if len(days)-i <= 28 {
			out.Chronic += d.Load / 4
		}
		if len(days)-i <= 7 {
			out.Acute += d.Load
		}
	}
	if len(days) >= 28 && out.Chronic > 0 {
		ratio := out.Acute / out.Chronic
		out.Ratio = &ratio
	}
	out.Balance = out.Chronic - out.Acute
	return out
}
