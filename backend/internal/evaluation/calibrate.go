package evaluation

import (
	"math"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
)

// ForecastDay pairs logged energy with the expenditure projected for that day.
type ForecastDay struct{ Intake, TDEE float64 }

// Residual records signed energy bias and error in the weight trajectory.
type Residual struct {
	PredictedKG, ActualKG, BiasKcalDay float64
	AbsolutePercentageError            *float64
}

// WeightResidual compares a fourteen-day forecast with observed weight change.
func WeightResidual(days []ForecastDay, deltaKG, density float64) *Residual {
	if len(days) != 14 || density <= 0 {
		return nil
	}
	var balance float64
	for _, d := range days {
		balance += d.Intake - d.TDEE
	}
	predicted := balance / density
	out := &Residual{PredictedKG: predicted, ActualKG: deltaKG, BiasKcalDay: (predicted - deltaKG) * density / 14}
	if math.Abs(deltaKG) >= .1 {
		r := 100 * math.Abs(predicted-deltaKG) / math.Abs(deltaKG)
		out.AbsolutePercentageError = &r
	}
	return out
}

// Evidence is one dated, confidence-scored observation for energy calibration.
type Evidence struct {
	Bias                  *float64
	Confidence, PowerKcal float64
	PredictedKG, ActualKG *float64
}

// Parameters holds bounded individual model coefficients.
type Parameters struct {
	GME, Density, Caffeine, Alcohol float64
	SweatRate, SweatSodium          *float64
}

// EnergyParameters updates efficiency or tissue density given persistent error and sufficient evidence.
func EnergyParameters(p Parameters, samples []Evidence, inTransition bool) Parameters {
	if len(samples) < 21 || inTransition {
		return p
	}
	var sum, power, density float64
	positive, negative, known, ratios := 0, 0, 0, 0
	for _, s := range samples {
		if s.Bias == nil || s.Confidence < .85 {
			continue
		}
		known++
		sum += *s.Bias
		power += s.PowerKcal
		if *s.Bias > 0 {
			positive++
		} else if *s.Bias < 0 {
			negative++
		}
		if s.PredictedKG != nil && s.ActualKG != nil && math.Abs(*s.ActualKG) >= .25 && *s.PredictedKG**s.ActualKG > 0 {
			density += min(10000, max(5000, p.Density*(*s.PredictedKG / *s.ActualKG)))
			ratios++
		}
	}
	if known < 21 {
		return p
	}
	bias := sum / float64(known)
	persistent := float64(max(positive, negative))/float64(known) >= .7
	if persistent && power/float64(known) >= 100 && math.Abs(bias) > 50 {
		p.GME = clamp(p.GME-math.Copysign(.001, bias), .20, .25)
	} else if persistent && power == 0 && ratios >= 14 {
		p.Density = clamp(p.Density+clamp((density/float64(ratios)-p.Density)*.05, -100, 100), 5000, 10000)
	}
	return p
}

// Sweat updates sodium from a measured patch and sweat rate from a fluid-adjusted weight change.
func Sweat(p Parameters, f model.CoachingFeedback) Parameters {
	if f.SweatPatch != nil {
		p.SweatSodium = smoothed(p.SweatSodium, *f.SweatPatch, 200, 2000)
	}
	if f.PreMass == nil || f.PostMass == nil || f.WorkoutMinutes == nil {
		return p
	}
	loss := *f.PreMass - *f.PostMass
	if f.FluidL != nil {
		loss += *f.FluidL
	}
	if f.UrineL != nil {
		loss -= *f.UrineL
	}
	rate := loss / (*f.WorkoutMinutes / 60)
	if rate >= .1 && rate <= 4 {
		p.SweatRate = smoothed(p.SweatRate, rate, .1, 4)
	}
	return p
}

func smoothed(old *float64, next, lo, hi float64) *float64 {
	if old != nil {
		next = *old + .20*(next-*old)
	}
	next = clamp(next, lo, hi)
	return &next
}

// Night pairs exposure and recovery observations for one night.
type Night struct {
	Caffeine, Alcohol, Late float64
	HRV, RHR, SWS           *float64
}

// Recovery updates caffeine and alcohol coefficients using measured outcomes and joint exposure.
func Recovery(p Parameters, nights []Night) Parameters {
	if len(nights) < 21 {
		return p
	}
	// Fit caffeine, alcohol and late-meal exposure jointly. Exposure is standardized
	// to practical changes in dose before solving the small ridge system.
	changes := make([][3]float64, 0, 3)
	for _, measure := range []func(Night) *float64{
		func(n Night) *float64 { return n.HRV },
		func(n Night) *float64 {
			if n.RHR == nil {
				return nil
			}
			v := -*n.RHR / 30
			return &v
		},
		func(n Night) *float64 {
			if n.SWS == nil {
				return nil
			}
			v := *n.SWS / 60
			return &v
		},
	} {
		if b, ok := regress(nights, measure); ok {
			changes = append(changes, b)
		}
	}
	for i := 0; i < 2; i++ {
		negative, positive := 0, 0
		for _, c := range changes {
			if c[i] < -.03 {
				negative++
			}
			if c[i] > .03 {
				positive++
			}
		}
		if negative == 0 && positive == 0 || negative == positive {
			continue
		}
		step := 1.
		if positive > negative {
			step = -1
		}
		if i == 0 {
			p.Caffeine = clamp(p.Caffeine+.125*step, 2.5, 9)
		} else {
			p.Alcohol = clamp(p.Alcohol+.025*step, .8, 1.5)
		}
	}
	return p
}

func regress(nights []Night, value func(Night) *float64) ([3]float64, bool) {
	var rows [][4]float64
	var mean [4]float64
	for _, n := range nights {
		if y := value(n); y != nil {
			row := [4]float64{n.Caffeine / 75, n.Alcohol / .35, n.Late / 6, *y}
			rows = append(rows, row)
			for i, x := range row {
				mean[i] += x
			}
		}
	}
	var b [3]float64
	if len(rows) < 14 {
		return b, false
	}
	for i := range mean {
		mean[i] /= float64(len(rows))
	}
	var a [3][4]float64
	for _, row := range rows {
		for i := 0; i < 3; i++ {
			for j := 0; j < 4; j++ {
				a[i][j] += (row[i] - mean[i]) * (row[j] - mean[j])
			}
		}
	}
	for i := 0; i < 3; i++ {
		a[i][i] += .5
	}
	for i := 0; i < 3; i++ {
		pivot := a[i][i]
		if pivot <= 0 {
			return b, false
		}
		for j := i; j < 4; j++ {
			a[i][j] /= pivot
		}
		for k := 0; k < 3; k++ {
			if k == i {
				continue
			}
			m := a[k][i]
			for j := i; j < 4; j++ {
				a[k][j] -= m * a[i][j]
			}
		}
	}
	for i := range b {
		b[i] = a[i][3]
	}
	return b, true
}

// DefaultParameters returns cohort priors before individualized estimation.
func DefaultParameters() Parameters {
	return Parameters{GME: energy.DefaultGME, Density: energy.DefaultTissueDensity, Caffeine: 5, Alcohol: 1}
}
func clamp(v, lo, hi float64) float64 { return min(hi, max(lo, v)) }
