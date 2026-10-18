package insights

import (
	"fmt"
	"math"

	"github.com/timkrebs/ryft/internal/model"
)

// Thresholds, in one place so a tweak is a one-line change.
const (
	hrvLowNights      = 3   // consecutive nights below baseline
	rhrElevatedBPM    = 5.0 // above baseline
	sleepDebtMin      = 90
	highStrain        = 14.0
	greenRecovery     = 67
	highStrainDays    = 3
	bedtimeSpreadMin  = 90 // max − min over the week
	bedtimeMinNights  = 5  // of the last 7, to judge consistency
	readinessUpPerDay = 1.0
	readinessMinDays  = 5 // of the last 7, to fit a trend
	bestHRVMinNights  = 14
)

// hrvBelowBaseline: HRV under the 30-day baseline for 3+ consecutive nights.
func hrvBelowBaseline(h History) *model.Insight {
	has := func(d Day) bool { return d.HRV != nil && d.HRVBase != nil && !d.Calibrating }
	n, last := h.Streak(has, func(d Day) bool { return *d.HRV < *d.HRVBase })
	if n < hrvLowNights {
		return nil
	}
	return &model.Insight{ID: "hrv-low-3d", Severity: model.SeverityWarn, Metric: model.MetricHRV, Day: last.Day,
		Title: fmt.Sprintf("HRV below your baseline for %d nights", n),
		Body: fmt.Sprintf("Your overnight HRV has been under your 30-day baseline of %.0f ms for %d nights in a row "+
			"(%.0f ms last night). That usually means accumulated stress, a cold coming on or too little recovery — "+
			"keep today's strain moderate and protect tonight's sleep.", *last.HRVBase, n, *last.HRV)}
}

// rhrElevated: last night's resting HR ≥ 5 bpm above baseline.
func rhrElevated(h History) *model.Insight {
	d, ok := h.Latest(func(d Day) bool { return d.RHR != nil && d.RHRBase != nil && !d.Calibrating })
	if !ok || *d.RHR-*d.RHRBase < rhrElevatedBPM {
		return nil
	}
	delta := *d.RHR - *d.RHRBase
	return &model.Insight{ID: "rhr-elevated", Severity: model.SeverityWarn, Metric: model.MetricRHR, Day: d.Day,
		Title: fmt.Sprintf("Resting heart rate up %.0f bpm", delta),
		Body: fmt.Sprintf("Your resting heart rate was %.0f bpm, %.0f bpm above your 30-day baseline of %.0f. "+
			"An elevated resting HR often follows hard training, alcohol or a late meal, and can precede illness.",
			*d.RHR, delta, *d.RHRBase)}
}

// sleepDebt: accumulated sleep debt ≥ 90 min.
func sleepDebt(h History) *model.Insight {
	d, ok := h.Latest(func(d Day) bool { return d.SleepDebtMin != nil })
	if !ok || *d.SleepDebtMin < sleepDebtMin {
		return nil
	}
	return &model.Insight{ID: "sleep-debt", Severity: model.SeverityNotice, Metric: model.MetricSleep, Day: d.Day,
		Title: "Sleep debt: " + hm(*d.SleepDebtMin),
		Body: fmt.Sprintf("You have built up %s of sleep debt over the past week. Part of it is added to tonight's "+
			"sleep need — an earlier bedtime pays it down fastest.", hm(*d.SleepDebtMin))}
}

// strainWithoutRecovery: three or more days at strain ≥ 14 in the last 14 days
// with no green recovery (≥ 67) since the first of them. A day's recovery is
// the morning's, so a green recovery resets the count before that day's strain.
func strainWithoutRecovery(h History) *model.Insight {
	count, last := 0, ""
	for _, d := range h.Last(14) {
		if d.Recovery != nil && *d.Recovery >= greenRecovery {
			count = 0
		}
		if d.Strain != nil && *d.Strain >= highStrain {
			count, last = count+1, d.Day
		}
	}
	if count < highStrainDays {
		return nil
	}
	return &model.Insight{ID: "strain-no-recovery", Severity: model.SeverityWarn, Metric: model.MetricStrain, Day: last,
		Title: fmt.Sprintf("%d hard days without recovering", count),
		Body: fmt.Sprintf("You have logged %d days at strain 14+ since your last green recovery. Stacking load "+
			"without recovering raises injury and illness risk — plan a lighter day until recovery is back above 67 %%.",
			count)}
}

// bedtimeConsistency: bedtime spread (latest − earliest) > 90 min over the last
// 7 nights, judged on at least 5 nights.
func bedtimeConsistency(h History) *model.Insight {
	var mins []int
	var last string
	for _, d := range h.Last(7) {
		if d.Bedtime != nil {
			mins, last = append(mins, minutesAfterNoon(d)), d.Day
		}
	}
	if len(mins) < bedtimeMinNights {
		return nil
	}
	lo, hi := mins[0], mins[0]
	for _, m := range mins {
		lo, hi = min(lo, m), max(hi, m)
	}
	if hi-lo <= bedtimeSpreadMin {
		return nil
	}
	return &model.Insight{ID: "bedtime-irregular", Severity: model.SeverityNotice, Metric: model.MetricSleep, Day: last,
		Title: "Irregular bedtime this week",
		Body: fmt.Sprintf("Your bedtime varied by %s over the last %d nights (earliest %s, latest %s). "+
			"Going to bed within the same hour each night improves sleep quality and recovery.",
			hm(hi-lo), len(mins), clock(lo), clock(hi))}
}

// readinessTrendingUp: least-squares slope of readiness over the last 7 days ≥ 1
// point per day, fitted on at least 5 days.
func readinessTrendingUp(h History) *model.Insight {
	var xs, ys []float64
	var last string
	for k, d := range h.Last(7) {
		if d.Readiness != nil {
			xs, ys, last = append(xs, float64(k)), append(ys, float64(*d.Readiness)), d.Day
		}
	}
	if len(xs) < readinessMinDays {
		return nil
	}
	slope, intercept := linearFit(xs, ys)
	if slope < readinessUpPerDay {
		return nil
	}
	from, to := intercept+slope*xs[0], intercept+slope*xs[len(xs)-1]
	return &model.Insight{ID: "readiness-up", Severity: model.SeverityInfo, Metric: model.MetricReadiness, Day: last,
		Title: "Readiness trending up",
		Body: fmt.Sprintf("Your readiness climbed about %.0f points this week, from around %.0f to %.0f. "+
			"Whatever you changed is working — keep it up.", to-from, from, to)}
}

// bestHRV30d: last night's HRV is the highest of the last 30 days, judged on at
// least 14 nights so an early-days high does not count.
func bestHRV30d(h History) *model.Insight {
	d, ok := h.Latest(func(d Day) bool { return d.HRV != nil && !d.Calibrating })
	if !ok {
		return nil
	}
	nights := 0
	for _, x := range h.Last(Window) {
		if x.HRV == nil {
			continue
		}
		nights++
		if x.Day != d.Day && *x.HRV >= *d.HRV {
			return nil
		}
	}
	if nights < bestHRVMinNights {
		return nil
	}
	return &model.Insight{ID: "hrv-best-30d", Severity: model.SeverityInfo, Metric: model.MetricHRV, Day: d.Day,
		Title: "Best HRV in 30 days",
		Body: fmt.Sprintf("Last night's HRV of %.0f ms is your highest in 30 days — your body is well recovered. "+
			"A good day for a harder session.", *d.HRV)}
}

// ---- helpers -----------------------------------------------------------------

// minutesAfterNoon places a bedtime on a noon-to-noon axis so 23:30 and 00:30
// are 60 minutes apart, not 23 hours.
func minutesAfterNoon(d Day) int {
	t := *d.Bedtime
	m := t.Hour()*60 + t.Minute() - 12*60
	if m < 0 {
		m += 24 * 60
	}
	return m
}

// clock formats minutes after noon as a wall-clock time.
func clock(minAfterNoon int) string {
	m := (minAfterNoon + 12*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// hm formats minutes as "1h 35m" / "50m".
func hm(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}

// linearFit is ordinary least squares y = intercept + slope·x.
func linearFit(xs, ys []float64) (slope, intercept float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for k := range xs {
		sx, sy, sxx, sxy = sx+xs[k], sy+ys[k], sxx+xs[k]*xs[k], sxy+xs[k]*ys[k]
	}
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-9 {
		return 0, sy / n
	}
	slope = (n*sxy - sx*sy) / den
	return slope, (sy - slope*sx) / n
}
