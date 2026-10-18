// Package scoring implements the Whoop-style Recovery / Strain / Sleep model as
// pure functions over already-loaded data. Nothing in this file touches the
// database, so every formula is unit-testable (see algorithms_test.go) and
// swappable without touching the service wiring in compute.go.
//
// The model, in one paragraph: every night produces an HRV, a resting heart
// rate and a respiratory rate. Each is compared to your own rolling 30-day
// baseline as a z-score and mapped through a logistic curve to 0..100.
// Recovery is a weighted blend of those plus last night's sleep performance.
// Strain integrates heart-rate-reserve over the day with Banister's TRIMP
// weighting and compresses it onto a 0..21 logarithmic scale, so the last few
// points are much harder to earn than the first. Sleep need is a personal
// baseline plus yesterday's strain plus accumulated debt; performance is
// actual / need.
package scoring

import (
	"math"
	"sort"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Fallback standard deviations used while fewer than MinBaselineDays of
// history exist ("calibrating"). Values are typical population spreads.
const (
	MinBaselineDays = 7
	fallbackSDlnHRV = 0.18
	fallbackSDRHR   = 3.5
	fallbackSDResp  = 1.0
)

// Baseline is a mean/sd over a window of previous days.
type Baseline struct {
	Mean float64
	SD   float64
	N    int
}

func NewBaseline(values []float64, fallbackSD float64) Baseline {
	b := Baseline{N: len(values)}
	if b.N == 0 {
		return b
	}
	for _, v := range values {
		b.Mean += v
	}
	b.Mean /= float64(b.N)
	if b.N >= MinBaselineDays {
		var ss float64
		for _, v := range values {
			ss += (v - b.Mean) * (v - b.Mean)
		}
		b.SD = math.Sqrt(ss / float64(b.N-1))
		if b.SD < 1e-6 {
			b.SD = fallbackSD
		}
	} else {
		b.SD = fallbackSD
	}
	return b
}

// z returns the z-score of v against b, or 0 when there is no history.
func (b Baseline) z(v float64) float64 {
	if b.N == 0 || b.SD == 0 {
		return 0
	}
	return (v - b.Mean) / b.SD
}

// logistic maps a z-score to 0..100: z=0 → 50, z=+1 → ~77, z=-1 → ~23, z=+2 → ~92.
func logistic(z float64) float64 {
	return 100 / (1 + math.Exp(-1.2*z))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- Recovery ----------------------------------------------------------------

type RecoveryInputs struct {
	HRVms    *float64 // overnight HRV (SDNN from HealthKit); compared in log space
	RHR      *float64
	RespRate *float64

	LnHRVBase Baseline
	RHRBase   Baseline
	RespBase  Baseline

	SleepPerformance *int // 0..100 from last night
}

type RecoveryResult struct {
	Score       int                // 1..100
	Calibrating bool               // fewer than MinBaselineDays of history
	Components  map[string]float64 // per-signal 0..100 sub-scores and z-scores, for the UI
}

// Recovery weights. HRV dominates because it is the most responsive signal to
// autonomic load; sleep performance is the strongest controllable lever.
const (
	wHRV   = 0.45
	wRHR   = 0.25
	wSleep = 0.25
	wResp  = 0.05
)

func Recovery(in RecoveryInputs) RecoveryResult {
	comp := map[string]float64{}
	var score, weightSum float64

	if in.HRVms != nil && *in.HRVms > 0 {
		z := in.LnHRVBase.z(math.Log(*in.HRVms))
		s := logistic(z)
		comp["hrv_z"], comp["hrv_score"] = z, s
		score += wHRV * s
		weightSum += wHRV
	}
	if in.RHR != nil && *in.RHR > 0 {
		z := -in.RHRBase.z(*in.RHR) // lower RHR is better
		s := logistic(z)
		comp["rhr_z"], comp["rhr_score"] = z, s
		score += wRHR * s
		weightSum += wRHR
	}
	if in.RespRate != nil && *in.RespRate > 0 {
		z := -in.RespBase.z(*in.RespRate) // elevated breathing rate → illness/overreaching
		s := logistic(z)
		comp["resp_z"], comp["resp_score"] = z, s
		score += wResp * s
		weightSum += wResp
	}
	if in.SleepPerformance != nil {
		s := clamp(float64(*in.SleepPerformance), 0, 100)
		comp["sleep_score"] = s
		score += wSleep * s
		weightSum += wSleep
	}

	res := RecoveryResult{Components: comp,
		Calibrating: in.LnHRVBase.N < MinBaselineDays || in.RHRBase.N < MinBaselineDays}
	if weightSum == 0 {
		res.Score = 0 // no data at all
		return res
	}
	res.Score = int(math.Round(clamp(score/weightSum, 1, 100)))
	return res
}

// ---- Readiness ---------------------------------------------------------------

// Readiness overreach rule: yesterday's strain above readinessStrainLimit on a
// recovery below readinessRecoveryLimit costs up to readinessMaxPenalty points,
// ramping linearly from 0 at the limit to the full penalty at strain 21.
const (
	readinessStrainLimit   = 14.0
	readinessRecoveryLimit = 50
	readinessMaxPenalty    = 10.0
)

// Readiness is the Today screen's headline number:
// round(0.6·recovery + 0.4·sleep performance) minus the overreach penalty,
// clamped to 1..100.
func Readiness(recovery, sleepPerf int, yesterdayStrain float64) int {
	base := int(math.Round(0.6*float64(recovery) + 0.4*float64(sleepPerf)))
	return int(clamp(float64(base-ReadinessPenalty(recovery, yesterdayStrain)), 1, 100))
}

// ReadinessPenalty is the number of points Readiness subtracts because
// yesterday's strain exceeded what today's recovery supports.
func ReadinessPenalty(recovery int, yesterdayStrain float64) int {
	if yesterdayStrain <= readinessStrainLimit || recovery >= readinessRecoveryLimit {
		return 0
	}
	over := clamp((yesterdayStrain-readinessStrainLimit)/(21-readinessStrainLimit), 0, 1)
	return int(math.Round(readinessMaxPenalty * over))
}

// ReadinessState is the word the app shows under the greeting. Same bands as
// recovery (≥67 green, 34–66 yellow, <34 red); "calibrating" while baselines
// are still being built or there is nothing to score.
func ReadinessState(readiness *int, calibrating bool) string {
	switch {
	case readiness == nil || calibrating:
		return model.StateCalibrating
	case *readiness >= 67:
		return model.StateBalanced
	case *readiness >= 34:
		return model.StateStrained
	default:
		return model.StateDepleted
	}
}

// ---- Strain ------------------------------------------------------------------

// HRPoint is one heart-rate sample.
type HRPoint struct {
	T   time.Time
	BPM float64
}

type StrainResult struct {
	TRIMP       float64    // Banister training impulse, unbounded
	Score       float64    // 0..21
	ZoneMinutes [5]float64 // minutes in HRR zones 50-60, 60-70, 70-80, 80-90, 90+
	Minutes     float64    // minutes of usable HR data
}

const (
	maxSampleGap = 5 * time.Minute // longer gaps mean the watch was off: no load credited
	hrrFloor     = 0.25            // HRR below this (sitting, light walking) earns no load
	trimpScale   = 80.0            // calibrated so a 60-min run at 75% HRR ≈ 14 strain
)

// Strain integrates HR samples against heart-rate reserve.
//
// Effective intensity i = max(0, HRR - hrrFloor) / (1 - hrrFloor), so a sedentary
// day scores ~0 and a day with walking and stairs 3-6, like Whoop.
// TRIMP per minute = i * 0.64 * e^(1.92*i)   (Banister, male coefficients;
// use 0.86 / 1.67 for female). Score = 21 * (1 - e^(-TRIMP/trimpScale)).
func Strain(points []HRPoint, rhr, maxHR float64) StrainResult {
	var res StrainResult
	if len(points) < 2 || maxHR <= rhr {
		return res
	}
	sort.Slice(points, func(i, j int) bool { return points[i].T.Before(points[j].T) })
	for i := 0; i+1 < len(points); i++ {
		dt := points[i+1].T.Sub(points[i].T)
		if dt <= 0 {
			continue
		}
		if dt > maxSampleGap {
			dt = maxSampleGap
		}
		mins := dt.Minutes()
		hrr := clamp((points[i].BPM-rhr)/(maxHR-rhr), 0, 1)
		res.Minutes += mins
		res.TRIMP += mins * trimpPerMinute(hrr)
		switch {
		case hrr >= 0.9:
			res.ZoneMinutes[4] += mins
		case hrr >= 0.8:
			res.ZoneMinutes[3] += mins
		case hrr >= 0.7:
			res.ZoneMinutes[2] += mins
		case hrr >= 0.6:
			res.ZoneMinutes[1] += mins
		case hrr >= 0.5:
			res.ZoneMinutes[0] += mins
		}
	}
	res.Score = StrainFromTRIMP(res.TRIMP)
	return res
}

func trimpPerMinute(hrr float64) float64 {
	i := math.Max(0, hrr-hrrFloor) / (1 - hrrFloor)
	return i * 0.64 * math.Exp(1.92*i)
}

func StrainFromTRIMP(trimp float64) float64 {
	return math.Round(21*(1-math.Exp(-trimp/trimpScale))*10) / 10
}

// StrainFromAverage estimates a workout's strain when only avg HR and duration
// are known (e.g. a workout logged without continuous HR samples).
func StrainFromAverage(avgHR, rhr, maxHR float64, dur time.Duration) float64 {
	if maxHR <= rhr || dur <= 0 {
		return 0
	}
	hrr := clamp((avgHR-rhr)/(maxHR-rhr), 0, 1)
	return StrainFromTRIMP(dur.Minutes() * trimpPerMinute(hrr))
}

// ---- Sleep -------------------------------------------------------------------

type SleepTotals struct {
	Start, End                                    time.Time
	InBedMin, AsleepMin, DeepMin, REMMin, CoreMin int
	AwakeMin                                      int
	HasStages                                     bool
}

// SummarizeSleep collapses HealthKit segments into totals. Stage segments are
// non-overlapping by HealthKit contract; "inbed" overlaps them and is only used
// as a fallback when a source (older Garmin firmware) records no stages.
func SummarizeSleep(segs []model.SleepSegment) SleepTotals {
	var t SleepTotals
	for _, s := range segs {
		mins := int(math.Round(s.End.Sub(s.Start).Minutes()))
		if mins <= 0 {
			continue
		}
		if t.Start.IsZero() || s.Start.Before(t.Start) {
			t.Start = s.Start
		}
		if s.End.After(t.End) {
			t.End = s.End
		}
		switch s.Stage {
		case model.SleepInBed:
			t.InBedMin += mins
		case model.SleepAwake:
			t.AwakeMin += mins
		case model.SleepDeep:
			t.DeepMin += mins
			t.AsleepMin += mins
			t.HasStages = true
		case model.SleepREM:
			t.REMMin += mins
			t.AsleepMin += mins
			t.HasStages = true
		case model.SleepCore:
			t.CoreMin += mins
			t.AsleepMin += mins
			t.HasStages = true
		case model.SleepAsleep:
			t.AsleepMin += mins
		}
	}
	if t.AsleepMin == 0 && t.InBedMin > 0 {
		// No asleep data at all: assume ~90% efficiency on time in bed.
		t.AsleepMin = int(float64(t.InBedMin) * 0.9)
	}
	return t
}

// SleepNeed = personal baseline + strain top-up + a share of accumulated debt.
// Yesterday's strain adds up to 60 min (3 min per strain point); 30% of the
// outstanding debt is asked back each night, capped at 60 min, so need never
// exceeds baseline + 2h and one bad night does not spiral.
func SleepNeed(baseMin int, yesterdayStrain float64, debtMin int) int {
	strainTopUp := math.Min(60, yesterdayStrain*3)
	debtTopUp := math.Min(60, float64(debtMin)*0.3)
	return baseMin + int(math.Round(strainTopUp+debtTopUp))
}

// SleepDebt sums shortfalls over the previous days with geometric decay
// (0.7 per day), so a bad night a week ago barely matters.
func SleepDebt(prevNeedMin, prevActualMin []int) int {
	var debt, w float64 = 0, 1
	for i := 0; i < len(prevNeedMin) && i < len(prevActualMin); i++ { // index 0 = most recent
		short := math.Max(0, float64(prevNeedMin[i]-prevActualMin[i]))
		debt += w * short
		w *= 0.7
	}
	return int(math.Round(debt))
}

func SleepPerformance(actualMin, needMin int) int {
	if needMin <= 0 {
		return 0
	}
	return int(math.Round(clamp(float64(actualMin)/float64(needMin)*100, 0, 100)))
}
