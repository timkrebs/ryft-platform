package scoring

import (
	"math"

	"github.com/timkrebs/ryft/internal/model"
)

// TrendSummary wraps a metric's points with the window's mean (one decimal),
// minimum and maximum; all three are nil when there are no points.
func TrendSummary(metric, rng, from, to string, points []model.TrendPoint, baseline *float64) model.Trend {
	t := model.Trend{Metric: metric, Range: rng, From: from, To: to, Points: points, Baseline: baseline}
	if t.Points == nil {
		t.Points = []model.TrendPoint{}
	}
	if len(t.Points) == 0 {
		return t
	}
	lo, hi, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, p := range t.Points {
		lo, hi, sum = math.Min(lo, p.Value), math.Max(hi, p.Value), sum+p.Value
	}
	avg := math.Round(sum/float64(len(t.Points))*10) / 10
	t.Avg, t.Min, t.Max = &avg, &lo, &hi
	return t
}
