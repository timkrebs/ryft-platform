package scoring

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// MaxHRPoints caps a workout's heart-rate curve on the wire.
const MaxHRPoints = 600

// WorkoutWindowEnd is the exclusive upper bound for a workout's samples: the
// workout end itself is inclusive, as in ComputeDay, and Postgres stores µs.
func WorkoutWindowEnd(w model.Workout) time.Time { return w.End.Add(time.Microsecond) }

// WorkoutDetail assembles the workout screen from the stored row and the raw
// heart-rate samples inside its window. Average and max HR come from the app
// when it sent them, otherwise from the samples.
func WorkoutDetail(w store.WorkoutRow, hr []model.Sample) model.WorkoutDetail {
	pts := make([]HRPoint, 0, len(hr))
	var sum, peak float64
	for _, s := range hr {
		pts = append(pts, HRPoint{T: s.Start, BPM: s.Value})
		sum += s.Value
		peak = math.Max(peak, s.Value)
	}
	out := model.WorkoutDetail{Workout: w.Workout, HR: []model.HRSample{}, ZoneMin: zoneInts(w.ZoneMinutes)}
	if len(pts) > 0 {
		if out.AvgHR == nil {
			avg := math.Round(sum/float64(len(pts))*10) / 10
			out.AvgHR = &avg
		}
		if out.MaxHR == nil {
			out.MaxHR = &peak
		}
	}
	for _, p := range DownsampleHR(pts, MaxHRPoints) {
		out.HR = append(out.HR, model.HRSample{T: p.T.UTC(), BPM: int(math.Round(p.BPM))})
	}
	if w.TRIMP != nil {
		t := int(math.Round(*w.TRIMP))
		out.TRIMP = &t
	}
	return out
}

// DayStrainFrom turns a stored day load into the API object; nil without HR data.
func DayStrainFrom(l store.DayLoad) *model.DayStrain {
	if l.TRIMP == nil || l.HRMin == nil || *l.HRMin == 0 {
		return nil
	}
	return &model.DayStrain{TRIMP: int(math.Round(*l.TRIMP)), HRMin: *l.HRMin, ZoneMin: zoneInts(l.ZoneMinutes)}
}

// zoneInts rounds zone minutes for the wire; nil stays nil (not scored yet).
func zoneInts(z []float64) []int {
	if z == nil {
		return nil
	}
	out := make([]int, len(z))
	for k, v := range z {
		out[k] = int(math.Round(v))
	}
	return out
}

// DownsampleHR reduces time-sorted points to at most limit with
// Largest-Triangle-Three-Buckets, which keeps the curve's visual shape — peaks
// and troughs survive instead of being averaged away. The first and last points
// are always kept; every returned point is an original sample.
func DownsampleHR(pts []HRPoint, limit int) []HRPoint {
	if limit < 3 || len(pts) <= limit {
		return pts
	}
	x := func(p HRPoint) float64 { return float64(p.T.UnixMilli()) }
	out := make([]HRPoint, 0, limit)
	out = append(out, pts[0])
	bucket := float64(len(pts)-2) / float64(limit-2) // > 1 because len(pts) > limit
	a := 0
	for b := 0; b < limit-2; b++ {
		// Average of the next bucket (the last point for the final bucket).
		next, nextEnd := int(float64(b+1)*bucket)+1, min(int(float64(b+2)*bucket)+1, len(pts))
		var avgX, avgY float64
		for _, p := range pts[next:nextEnd] {
			avgX += x(p)
			avgY += p.BPM
		}
		n := float64(nextEnd - next)
		avgX, avgY = avgX/n, avgY/n

		// Pick the point in this bucket forming the largest triangle with the
		// previously selected point and the next bucket's average.
		ax, ay := x(pts[a]), pts[a].BPM
		best, bestArea := -1, -1.0
		for j := int(float64(b)*bucket) + 1; j < int(float64(b+1)*bucket)+1; j++ {
			area := math.Abs((ax-avgX)*(pts[j].BPM-ay) - (ax-x(pts[j]))*(avgY-ay))
			if area > bestArea {
				best, bestArea = j, area
			}
		}
		out = append(out, pts[best])
		a = best
	}
	return append(out, pts[len(pts)-1])
}
