package score

import (
	"cmp"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

const (
	// BaselineWindow bounds the preprandial CGM interval.
	BaselineWindow = 15 * time.Minute
	// GlucoseWindow bounds the postprandial CGM interval.
	GlucoseWindow = 120 * time.Minute

	minimumCGMSamples                     = 3
	peakFull, peakZero                    = 20, 60
	aucFull, aucZero                      = 1000, 4500
	recoveryFull, recoveryZero            = 5, 30
	peakWeight, aucWeight, recoveryWeight = 0.50, 0.35, 0.15
)

// ValidGlucose bounds imported device values to the supported numeric range.
func ValidGlucose(value float64) bool { return value > 0 && value <= 10_000 }

// GlucoseByMeal scores evaluable windows. Neighboring meal times can cut a window even across local dates.
func GlucoseByMeal(meals []model.NutritionMeal, neighbors []time.Time,
	readings []model.NutritionGlucoseSample) map[uuid.UUID]model.NutritionMealGlucoseScore {
	points := glucosePoints(readings)
	times := slices.Clone(neighbors)
	for _, m := range meals {
		times = append(times, m.ConsumedAt)
	}
	slices.SortFunc(times, time.Time.Compare)
	out := make(map[uuid.UUID]model.NutritionMealGlucoseScore)
	for _, m := range meals {
		start := m.ConsumedAt
		end := start.Add(GlucoseWindow)
		next := sort.Search(len(times), func(i int) bool { return times[i].After(start) })
		if next < len(times) && times[next].Before(end) {
			end = times[next]
		}
		left := sort.Search(len(points), func(i int) bool { return !points[i].Timestamp.Before(start) })
		right := sort.Search(len(points), func(i int) bool { return points[i].Timestamp.After(end) })
		if right-left < minimumCGMSamples {
			continue
		}
		baseStart := sort.Search(len(points), func(i int) bool { return !points[i].Timestamp.Before(start.Add(-BaselineWindow)) })
		baseEnd := left
		if points[left].Timestamp.Equal(start) {
			baseEnd++
		}
		base := points[left].GlucoseMgDL
		if baseStart < baseEnd {
			values := make([]float64, 0, baseEnd-baseStart)
			for _, p := range points[baseStart:baseEnd] {
				values = append(values, p.GlucoseMgDL)
			}
			base = median(values)
		}
		out[m.ID] = scoreMeal(m.ID, end, base, points[left:right])
	}
	return out
}

func glucosePoints(readings []model.NutritionGlucoseSample) []model.NutritionGlucoseSample {
	sorted := make([]model.NutritionGlucoseSample, 0, len(readings))
	for _, r := range readings {
		if !r.Timestamp.IsZero() && ValidGlucose(r.GlucoseMgDL) {
			sorted = append(sorted, r)
		}
	}
	slices.SortFunc(sorted, func(a, b model.NutritionGlucoseSample) int {
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return cmp.Compare(a.GlucoseMgDL, b.GlucoseMgDL)
	})
	points := sorted[:0]
	for i := 0; i < len(sorted); {
		j := i + 1
		for j < len(sorted) && sorted[j].Timestamp.Equal(sorted[i].Timestamp) {
			j++
		}
		p := sorted[i]
		p.GlucoseMgDL = mean(sorted[(i+j-1)/2].GlucoseMgDL, sorted[(i+j)/2].GlucoseMgDL)
		points = append(points, p)
		i = j
	}
	return points
}

func mean(a, b float64) float64 { return a/2 + b/2 }

func median(values []float64) float64 {
	slices.Sort(values)
	n := len(values)
	return mean(values[(n-1)/2], values[n/2])
}

func scaledLoss(value, full, zero float64) float64 {
	return clamp(dayScoreMax*(zero-value)/(zero-full), dayScoreMax)
}

func scoreMeal(id uuid.UUID, end time.Time, baseline float64, points []model.NutritionGlucoseSample) model.NutritionMealGlucoseScore {
	out := model.NutritionMealGlucoseScore{MealID: id, WindowEnd: end.UTC(), BaselineMgDL: baseline, SampleCount: len(points)}
	previous := points[0].GlucoseMgDL - baseline
	out.PeakDeltaMgDL = previous
	for i := 1; i < len(points); i++ {
		delta := points[i].GlucoseMgDL - baseline
		out.PeakDeltaMgDL = max(out.PeakDeltaMgDL, delta)
		dt := points[i].Timestamp.Sub(points[i-1].Timestamp).Minutes()
		out.IAUCMgDLMin += mean(max(0, previous), max(0, delta)) * dt
		previous = delta
	}
	out.EndDeltaMgDL = max(0, previous)
	out.PeakScore = scaledLoss(out.PeakDeltaMgDL, peakFull, peakZero)
	out.AUCScore = scaledLoss(out.IAUCMgDLMin, aucFull, aucZero)
	out.RecoveryScore = scaledLoss(out.EndDeltaMgDL, recoveryFull, recoveryZero)
	out.GlucoseScore = clamp(peakWeight*out.PeakScore+aucWeight*out.AUCScore+recoveryWeight*out.RecoveryScore, dayScoreMax)
	return out
}
