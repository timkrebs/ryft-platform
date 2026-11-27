package scoring

import (
	"slices"
	"sort"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Plausible readings (D307 leaves the range check to the readers, D311). A
// value outside is ignored as if it had not arrived: HealthKit's VO₂ max
// estimates lie around 15-90 ml/kg/min, a one-minute recovery drop around
// 5-80 bpm; the bounds leave room on both sides and drop only sensor or unit errors.
const (
	vo2MaxMin, vo2MaxMax = 10.0, 100.0 // ml/kg/min
	hrrMin, hrrMax       = 1.0, 120.0  // bpm
)

// VO2MaxOn returns the latest reading in the 30 calendar days ending on day,
// and its change from the value seven days earlier if a new reading arrived.
// The earlier value is taken from the latest reading's source, so two devices
// with different estimates do not make a change out of their difference.
func VO2MaxOn(day string, loc *time.Location, readings []model.Sample) (vo2, delta *float64) {
	start, err := time.ParseInLocation(dayLayout, day, loc)
	if err != nil {
		return nil, nil
	}
	end := start.AddDate(0, 0, 1)
	previousEnd := start.AddDate(0, 0, -6)
	latest := latestVO2Max(readings, start.AddDate(0, 0, -29), end, nil)
	if latest == nil {
		return nil, nil
	}
	vo2 = fitnessDecimal(latest.Value)
	if !latest.Start.Before(previousEnd) {
		if previous := latestVO2Max(readings, start.AddDate(0, 0, -36), previousEnd, &latest.Source); previous != nil {
			delta = fitnessDecimal(latest.Value - previous.Value)
		}
	}
	return vo2, delta
}

// latestVO2Max is the newest plausible reading with start in [from, to), from
// source only when one is given.
func latestVO2Max(readings []model.Sample, from, to time.Time, source *string) *model.Sample {
	var latest *model.Sample
	for i := range readings {
		s := &readings[i]
		if s.Start.Before(from) || !s.Start.Before(to) || !(s.Value >= vo2MaxMin && s.Value <= vo2MaxMax) {
			continue
		}
		if source != nil && s.Source != *source {
			continue
		}
		if latest == nil || s.Start.After(latest.Start) || (s.Start.Equal(latest.Start) && s.Value > latest.Value) {
			latest = s
		}
	}
	return latest
}

// HRRMean returns the mean of the plausible heart-rate recovery readings
// (hrrMin-hrrMax bpm) whose start falls on the local day.
func HRRMean(day string, loc *time.Location, readings []model.Sample) *float64 {
	start, err := time.ParseInLocation(dayLayout, day, loc)
	if err != nil {
		return nil
	}
	end := start.AddDate(0, 0, 1)
	var mean float64
	var count int
	for _, s := range readings {
		// The negated range test also drops NaN.
		if s.Start.Before(start) || !s.Start.Before(end) || !(s.Value >= hrrMin && s.Value <= hrrMax) {
			continue
		}
		count++
		mean += (s.Value - mean) / float64(count)
	}
	if count == 0 {
		return nil
	}
	return fitnessDecimal(mean)
}

// fitnessDecimal rounds a (bounded, so finite) tile value to one decimal.
func fitnessDecimal(value float64) *float64 {
	value = round1(value)
	return &value
}

// ActiveMinutes counts the union of workout time and heart-rate time in zone 1
// or above, clipped to the day. Nil means neither input had enough data.
func ActiveMinutes(points []HRPoint, rhr, maxHR float64, workouts [][2]time.Time, dayStart, dayEnd time.Time) *int {
	if len(points) < 2 && len(workouts) == 0 {
		return nil
	}
	intervals := make([][2]time.Time, 0, len(workouts))
	add := func(start, end time.Time) {
		start, end = maxTime(start, dayStart), minTime(end, dayEnd)
		if end.After(start) {
			intervals = append(intervals, [2]time.Time{start, end})
		}
	}
	for _, w := range workouts {
		add(w[0], w[1])
	}
	if len(points) >= 2 && maxHR > rhr {
		points = slices.Clone(points)
		sort.Slice(points, func(i, j int) bool { return points[i].T.Before(points[j].T) })
		for i := 0; i+1 < len(points); i++ {
			dt, reserve := strainInterval(points[i], points[i+1], rhr, maxHR)
			if dt > 0 && hrrZone(reserve) >= 0 {
				add(points[i].T, points[i].T.Add(dt))
			}
		}
	}
	slices.SortFunc(intervals, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	var total time.Duration
	end := dayStart
	for _, w := range intervals {
		if w[1].After(end) {
			total += w[1].Sub(maxTime(w[0], end))
			end = w[1]
		}
	}
	minutes := roundInt(total.Minutes())
	return &minutes
}
