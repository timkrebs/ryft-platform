package score

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	baselineDays       = 7
	swcScale           = .2
	minorSWC           = 1
	moderateSWC        = 3
	majorSWC           = 6
	calibrationDays    = 30
	minPairs           = 14
	minRepeatSignals   = 3
	minCorrelation     = .4
	calibrationStep    = .5
	calibrationCeiling = 7.5
	rhrAlertDelta      = 2.0
)

// Overnight holds the observations for a local wake date.
type Overnight struct {
	Date    time.Time
	RMSSDms *float64
	RHR     *float64
}

func average(xs []float64) float64 {
	var sum float64
	for _, v := range xs {
		sum += v
	}
	return sum / float64(len(xs))
}

func positiveObservation(p *float64) bool { return p != nil && *p > 0 && validAmount(*p) }

// MorningBaseline compares a wake day against the seven preceding calendar days.
func MorningBaseline(today Overnight, prior []Overnight) model.N2RBiometrics {
	b := model.N2RBiometrics{WakeDate: today.Date.Format(time.DateOnly), Change: "unavailable"}
	days := map[string]Overnight{}
	for _, o := range prior {
		days[o.Date.Format(time.DateOnly)] = o
	}
	var hrv, rhr []float64
	for i := 1; i <= baselineDays; i++ {
		o := days[today.Date.AddDate(0, 0, -i).Format(time.DateOnly)]
		if positiveObservation(o.RMSSDms) {
			hrv = append(hrv, math.Log(*o.RMSSDms))
		}
		if positiveObservation(o.RHR) {
			rhr = append(rhr, *o.RHR)
		}
	}
	if len(rhr) == baselineDays && positiveObservation(today.RHR) {
		b.RHRDelta = numberPtr(*today.RHR - average(rhr))
	}
	if len(hrv) != baselineDays {
		return b
	}
	mu := average(hrv)
	var variance float64
	for _, x := range hrv {
		variance += (x - mu) * (x - mu)
	}
	sd := math.Sqrt(variance / (baselineDays - 1))
	b.RMSSDMean, b.RMSSDSD, b.SWC = numberPtr(mu), numberPtr(sd), numberPtr(swcScale*sd)
	if mu > 0 {
		b.RMSSDCV = numberPtr(100 * sd / mu)
	}
	if !positiveObservation(today.RMSSDms) {
		return b
	}
	delta := math.Log(*today.RMSSDms) - mu
	b.RMSSDDelta = numberPtr(delta)
	if *b.SWC <= 0 {
		return b
	}
	x := math.Abs(delta) / (*b.SWC)
	switch {
	case x >= majorSWC:
		b.Change = "major"
	case x >= moderateSWC:
		b.Change = "moderate"
	case x >= minorSWC:
		b.Change = "minor"
	default:
		b.Change = "stable"
	}
	return b
}

// CaffeinePair joins bedtime exposure with the following wake day's biometrics.
type CaffeinePair struct {
	WakeDate                  time.Time
	CaffeineMg                float64
	RMSSDDelta, SWC, RHRDelta *float64
}

func correlation(xs, ys []float64) float64 {
	if len(xs) < minPairs {
		return 0
	}
	mx, my := average(xs), average(ys)
	var sxy, sxx, syy float64
	for i, x := range xs {
		dx, dy := x-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}

// CalibrateCaffeine adjusts a model parameter after repeated within-account associations.
func CalibrateCaffeine(asOf time.Time, current float64, pairs []CaffeinePair) float64 {
	var hx, hy, rx, ry []float64
	var hFlags, rFlags int
	cutoff := asOf.AddDate(0, 0, -(calibrationDays - 1))
	seen := map[string]bool{}
	for _, p := range pairs {
		key := p.WakeDate.Format(time.DateOnly)
		if seen[key] || p.WakeDate.Before(cutoff) || p.WakeDate.After(asOf) || !validAmount(p.CaffeineMg) {
			continue
		}
		seen[key] = true
		if p.RMSSDDelta != nil && positiveObservation(p.SWC) && finite(*p.RMSSDDelta) {
			hx, hy = append(hx, p.CaffeineMg), append(hy, (*p.RMSSDDelta)/(*p.SWC))
			if p.CaffeineMg > caffeineOnset && *p.RMSSDDelta <= -moderateSWC*(*p.SWC) {
				hFlags++
			}
		}
		if p.RHRDelta != nil && finite(*p.RHRDelta) {
			rx, ry = append(rx, p.CaffeineMg), append(ry, *p.RHRDelta)
			if p.CaffeineMg > caffeineOnset && *p.RHRDelta >= rhrAlertDelta {
				rFlags++
			}
		}
	}
	if current < calibrationCeiling && (hFlags >= minRepeatSignals && correlation(hx, hy) <= -minCorrelation ||
		rFlags >= minRepeatSignals && correlation(rx, ry) >= minCorrelation) {
		return min(calibrationCeiling, current+calibrationStep)
	}
	return current
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
