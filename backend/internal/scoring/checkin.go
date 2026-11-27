package scoring

import (
	"math"
	"time"
)

// The morning check-in: a recovery estimate for a night without a wearable
// (D291, D292; docs/API.md → Recovery without a watch → The score;
// docs/SCORING.md → Check-in recovery). A pure function of the check-in, the
// user's sleep base, the prior check-ins and nights, and yesterday's N2R
// score: no I/O, no clock. The store gathers the inputs and stores the result
// with CheckinModelVersion; a newer model rescores stored check-ins.
//
// Every constant is a versioned RYFT heuristic unless a source is named. The
// self-report itself follows Hooper & Mackinnon (1995, Sports Med 20:321) and
// Saw, Main & Gastin (2016, Br J Sports Med 50:281); the numbers are ours.
const (
	// wellness_absolute = (0.65·quality + 0.35·freshness − 1) / 9 · 100: the
	// split is a RYFT heuristic (sleep quality weighs more than how fresh one
	// feels on waking), the −1 and /9 map 1..10 onto 0..100.
	checkinQualityWeight   = 0.65
	checkinFreshnessWeight = 0.35
	checkinRatingMin       = 1
	checkinRatingMax       = 10

	// The personal part (RYFT heuristic): at least 5 prior check-ins in the 14
	// days before (the store applies the window), their SD floored at 6 points
	// so a user who always answers the same does not swing on one point,
	// personal = 50 + 25·z, and wellness = 0.7·absolute + 0.3·personal.
	CheckinPersonalMinPrior   = 5
	CheckinPersonalWindowDays = 14
	checkinSDFloor            = 6.0
	checkinPersonalCenter     = 50.0
	checkinPersonalPerZ       = 25.0
	checkinAbsoluteShare      = 0.7
	checkinPersonalShare      = 0.3

	// acute_drop (RYFT heuristic): z ≤ −1.5, or quality and freshness both ≤ 4.
	checkinDropZ      = -1.5
	checkinDropRating = 4

	// duration (RYFT heuristic): 100 from L to L+120 min; below L linear down to
	// 40 at 300 min (5 h), 40 below; above L+120 minus 10 per hour, floor 75.
	// L = 450 (7.5 h), or the user's sleep base clamped to 360..600 while it is
	// learned or manual.
	checkinDurationLDefault = 450
	checkinDurationLMin     = 360
	checkinDurationLMax     = 600
	checkinPlateauMin       = 120
	checkinShortMin         = 300
	checkinShortScore       = 40.0
	checkinLongPerHour      = 10.0
	checkinLongFloor        = 75.0

	// regularity (RYFT heuristic): today's sleep midpoint on the local clock
	// against the circular mean of up to 7 prior nights (at least 3); 100 within
	// 30 min, 50 from 120 min, linear between.
	CheckinRegularityMaxPrior = 7
	CheckinRegularityMinPrior = 3
	checkinRegularFullMin     = 30.0
	checkinRegularHalfMin     = 120.0
	checkinRegularHalfScore   = 50.0

	// recovery_score weights (RYFT heuristic), renormalised over the parts
	// present: a missing regularity or N2R part is left out, not counted as 0.
	checkinWeightWellness   = 0.50
	checkinWeightDuration   = 0.30
	checkinWeightRegularity = 0.10
	checkinWeightN2R        = 0.10

	minutesPerDayF = 1440.0
)

// The reasons a check-in counts as an acute drop (readiness_reasons, D294).
// When both hold, CheckinDropBelowUsual wins (D298).
const (
	CheckinDropBelowUsual          = "checkin_below_usual"
	CheckinDropLowQualityFreshness = "checkin_low_quality_and_freshness"
)

// The User.sleep_base_source values under which the sleep base sets the
// duration plateau (the third, default, does not).
const (
	SleepBaseSourceLearned = "learned"
	SleepBaseSourceManual  = "manual"
)

// CheckinModelVersion is the model a check-in's score is stored with.
const CheckinModelVersion = ModelVersion

// CheckinRating is one prior check-in's two ratings, from which its
// wellness_absolute is recomputed unrounded (the stored component is rounded).
type CheckinRating struct {
	Quality, Freshness int
}

// CheckinInput is everything a check-in's score depends on.
type CheckinInput struct {
	Bedtime, WakeTime time.Time // as reported (UTC instants)
	TZOffsetSec       int       // the clock the user saw at WakeTime
	Quality           int       // perceived_sleep_quality 1..10
	Freshness         int       // perceived_freshness 1..10
	Soreness          bool      // zero weight (D292): carried, never scored
	// User.sleep_base_min and sleep_base_source (default|learned|manual).
	SleepBaseMin    int
	SleepBaseSource string
	// The check-ins of the 14 days before the wake day (not the day itself),
	// any order; fewer than 5 → no personal part.
	Prior []CheckinRating
	// Sleep midpoints of the nights before, minutes after local midnight
	// (SleepMidpointMin), newest first, the wearable night where a day has
	// one; only the first 7 count, fewer than 3 → no regularity part.
	PriorMidpoints []int
	// Yesterday's N2R score 0..100; nil without one.
	N2R *int
}

// CheckinComponents are the parts behind the score, each 0..100 and rounded
// (RecoveryComponents in docs/API.md). The score uses them unrounded.
type CheckinComponents struct {
	Wellness         int  `json:"wellness"`
	WellnessAbsolute int  `json:"wellness_absolute"`
	WellnessPersonal *int `json:"wellness_personal"`
	Duration         int  `json:"duration"`
	Regularity       *int `json:"regularity"`
	N2R              *int `json:"n2r"`
}

// CheckinResult is the computed part of a stored check-in.
type CheckinResult struct {
	SleepDurationMin int
	RecoveryScore    int
	Components       CheckinComponents
	Z                *float64 // the personal z; nil without the personal part
	AcuteDrop        bool
	DropReason       string // CheckinDropBelowUsual, CheckinDropLowQualityFreshness, or "" without a drop
}

// CheckinScore computes a check-in's recovery estimate (D292). The handler
// validates the ranges (docs/API.md → PUT /v1/recovery/checkins/{date});
// out-of-range ratings are clamped here so the function stays total.
func CheckinScore(in CheckinInput) CheckinResult {
	q, f := clampRating(in.Quality), clampRating(in.Freshness)
	out := CheckinResult{SleepDurationMin: SleepDurationMin(in.Bedtime, in.WakeTime)}

	absolute := WellnessAbsolute(q, f)
	wellness := absolute
	out.Components.WellnessAbsolute = roundInt(absolute)
	if len(in.Prior) >= CheckinPersonalMinPrior {
		prior := make([]float64, len(in.Prior))
		for k, p := range in.Prior {
			prior[k] = WellnessAbsolute(clampRating(p.Quality), clampRating(p.Freshness))
		}
		mean, sd := meanSD(prior)
		z := (absolute - mean) / math.Max(sd, checkinSDFloor)
		personal := clamp(checkinPersonalCenter+checkinPersonalPerZ*z, 0, 100)
		wellness = checkinAbsoluteShare*absolute + checkinPersonalShare*personal
		out.Z = &z
		out.Components.WellnessPersonal = intPtr(roundInt(personal))
	}
	out.Components.Wellness = roundInt(wellness)

	duration := CheckinDuration(out.SleepDurationMin, in.SleepBaseMin, in.SleepBaseSource)
	out.Components.Duration = roundInt(duration)

	sum := checkinWeightWellness*wellness + checkinWeightDuration*duration
	weights := checkinWeightWellness + checkinWeightDuration
	if r, ok := CheckinRegularity(SleepMidpointMin(in.Bedtime, in.WakeTime, in.TZOffsetSec), in.PriorMidpoints); ok {
		sum += checkinWeightRegularity * r
		weights += checkinWeightRegularity
		out.Components.Regularity = intPtr(roundInt(r))
	}
	if in.N2R != nil {
		n := clamp(float64(*in.N2R), 0, 100)
		sum += checkinWeightN2R * n
		weights += checkinWeightN2R
		out.Components.N2R = intPtr(roundInt(n))
	}
	out.RecoveryScore = roundInt(clamp(sum/weights, 0, 100))

	switch {
	case out.Z != nil && *out.Z <= checkinDropZ:
		out.AcuteDrop, out.DropReason = true, CheckinDropBelowUsual
	case q <= checkinDropRating && f <= checkinDropRating:
		out.AcuteDrop, out.DropReason = true, CheckinDropLowQualityFreshness
	}
	return out
}

// WellnessAbsolute is (0.65·quality + 0.35·freshness − 1) / 9 · 100, unrounded.
func WellnessAbsolute(quality, freshness int) float64 {
	return (checkinQualityWeight*float64(quality) + checkinFreshnessWeight*float64(freshness) - 1) /
		(checkinRatingMax - checkinRatingMin) * 100
}

// SleepDurationMin is wake − bed in whole minutes (truncated), never negative:
// time in bed as reported, nothing taken off for falling asleep.
func SleepDurationMin(bed, wake time.Time) int {
	return max(0, int(wake.Sub(bed)/time.Minute))
}

// CheckinDurationL is the lower edge of the duration plateau: 450 minutes, or
// the sleep base clamped to 360..600 while it is learned or manual.
func CheckinDurationL(sleepBaseMin int, source string) int {
	if (source == SleepBaseSourceLearned || source == SleepBaseSourceManual) && sleepBaseMin > 0 {
		return min(checkinDurationLMax, max(checkinDurationLMin, sleepBaseMin))
	}
	return checkinDurationLDefault
}

// CheckinDuration is the duration part, 40..100 (unrounded).
func CheckinDuration(minutes, sleepBaseMin int, source string) float64 {
	l := float64(CheckinDurationL(sleepBaseMin, source))
	m := float64(minutes)
	switch {
	case m <= checkinShortMin:
		return checkinShortScore
	case m < l:
		return checkinShortScore + (m-checkinShortMin)/(l-checkinShortMin)*(100-checkinShortScore)
	case m <= l+checkinPlateauMin:
		return 100
	default:
		return math.Max(checkinLongFloor, 100-(m-l-checkinPlateauMin)/60*checkinLongPerHour)
	}
}

// SleepMidpointMin is the night's midpoint as minutes after local midnight
// (0..1439) on the clock offset by tzOffsetSec. For a wearable night pass the
// zone's offset at the midpoint (time.Time.In(loc).Zone()).
func SleepMidpointMin(bed, wake time.Time, tzOffsetSec int) int {
	mid := bed.Add(wake.Sub(bed) / 2)
	local := (mid.Unix() + int64(tzOffsetSec)) % 86400
	if local < 0 {
		local += 86400
	}
	return int(local / 60)
}

// CheckinRegularity scores today's midpoint against the circular mean of the
// first 7 prior midpoints (newest first); false with fewer than 3, or when the
// prior midpoints cancel out on the clock (no mean direction).
func CheckinRegularity(today int, prior []int) (float64, bool) {
	if len(prior) > CheckinRegularityMaxPrior {
		prior = prior[:CheckinRegularityMaxPrior]
	}
	if len(prior) < CheckinRegularityMinPrior {
		return 0, false
	}
	var s, c float64
	for _, m := range prior {
		a := 2 * math.Pi * float64(m) / minutesPerDayF
		s, c = s+math.Sin(a), c+math.Cos(a)
	}
	if math.Hypot(s, c)/float64(len(prior)) < 1e-9 {
		return 0, false
	}
	mean := math.Atan2(s, c) / (2 * math.Pi) * minutesPerDayF
	d := math.Mod(math.Abs(float64(today)-mean), minutesPerDayF)
	d = math.Min(d, minutesPerDayF-d)
	switch {
	case d <= checkinRegularFullMin:
		return 100, true
	case d >= checkinRegularHalfMin:
		return checkinRegularHalfScore, true
	}
	return 100 - (d-checkinRegularFullMin)/(checkinRegularHalfMin-checkinRegularFullMin)*(100-checkinRegularHalfScore), true
}

// meanSD is the mean and the sample SD (n − 1) of xs; the SD is 0 for one value.
func meanSD(xs []float64) (float64, float64) {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	var ss float64
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(ss / float64(len(xs)-1))
}

func clampRating(v int) int { return min(checkinRatingMax, max(checkinRatingMin, v)) }

func roundInt(v float64) int { return int(math.Round(v)) }

func intPtr(v int) *int { return &v }
