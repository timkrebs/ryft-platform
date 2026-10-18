package scoring

import (
	"math"
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

func TestTrendSummary(t *testing.T) {
	pts := []model.TrendPoint{{Day: "2026-10-12", Value: 71}, {Day: "2026-10-13", Value: 52},
		{Day: "2026-10-15", Value: 88}, {Day: "2026-10-18", Value: 84}}
	tr := TrendSummary("readiness", "7d", "2026-10-12", "2026-10-18", pts, nil)
	if *tr.Avg != 73.8 || *tr.Min != 52 || *tr.Max != 88 || tr.Baseline != nil || len(tr.Points) != 4 {
		t.Fatalf("got avg %v min %v max %v", *tr.Avg, *tr.Min, *tr.Max)
	}
	empty := TrendSummary("hrv", "90d", "a", "b", nil, nil)
	if empty.Points == nil || empty.Avg != nil || empty.Min != nil || empty.Max != nil {
		t.Fatalf("empty trend must have [] points and null stats: %+v", empty)
	}
}

func TestHRVBaselineIsGeometricMean(t *testing.T) {
	b := HRVBaselineMS(lnBase(40, 90)) // arithmetic mean 65, geometric 60
	if b == nil || *b != 60 {
		t.Fatalf("HRV baseline should be exp(mean ln) = 60, got %v", b)
	}
	if HRVBaselineMS(Baseline{}) != nil || RHRBaseline(Baseline{}) != nil {
		t.Fatal("no history → nil baselines")
	}
	if r := RHRBaseline(NewBaseline([]float64{48, 50, 52}, fallbackSDRHR)); r == nil || math.Abs(*r-50) > 1e-9 {
		t.Fatalf("RHR baseline = %v", r)
	}
}
