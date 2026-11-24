package evaluation

import (
	"math"
	"testing"
)

func TestResidualAndPhysiologicalBounds(t *testing.T) {
	days := make([]ForecastDay, 14)
	for i := range days {
		days[i] = ForecastDay{Intake: 2000, TDEE: 2200}
	}
	out := WeightResidual(days, -.5, 7700)
	if out == nil || math.Abs(out.BiasKcalDay-75) > 1e-8 || out.AbsolutePercentageError == nil {
		t.Fatalf("residual: %+v", out)
	}
	zero := WeightResidual(days, 0, 7700)
	if zero.AbsolutePercentageError != nil {
		t.Fatal("undefined denominator must be omitted")
	}
	p := DefaultParameters()
	samples := make([]Evidence, 21)
	for i := range samples {
		samples[i] = Evidence{Bias: &out.BiasKcalDay, Confidence: .90, PowerKcal: 300}
	}
	if changed := EnergyParameters(p, samples, false); changed.GME >= p.GME || changed.GME < .20 {
		t.Fatalf("GME: %+v", changed)
	}
	if unchanged := EnergyParameters(p, samples, true); unchanged.GME != p.GME {
		t.Fatal("calibrated during a transition")
	}
}
