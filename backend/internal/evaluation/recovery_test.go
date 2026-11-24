package evaluation

import (
	"math"
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

func measure(v float64) *float64 { return &v }

func TestCalibrationSeparatesExposures(t *testing.T) {
	prior := DefaultParameters()
	nights := make([]Night, 28)
	for i := range nights {
		caffeine := 75 * float64(i%2)
		alcohol := .35 * float64(i/2%2)
		late := 6 * float64(i/4%2)
		nights[i] = Night{Caffeine: caffeine, Alcohol: alcohol, Late: late,
			HRV: measure(-.2*caffeine/75 - .16*alcohol/.35 - late/15),
			RHR: measure(5*caffeine/75 + 5*alcohol/.35),
			SWS: measure(80 - 8*caffeine/75 - 10*alcohol/.35)}
	}
	if got := Recovery(prior, nights); got.Caffeine != 5.125 || got.Alcohol != 1.025 {
		t.Fatalf("independent exposure adjustment: %+v", got)
	}
	for i := range nights {
		nights[i].Caffeine, nights[i].Alcohol = 0, 0
		nights[i].RHR, nights[i].SWS = nil, nil
	}
	if got := Recovery(prior, nights); got != prior {
		t.Fatalf("late meals alone changed other parameters: %+v", got)
	}
	for i := range nights {
		nights[i].HRV = nil
	}
	if got := Recovery(prior, nights); got != prior {
		t.Fatalf("missing outcomes changed priors: %+v", got)
	}
}

func TestSweatCalibrationUsesFluidAdjustedMass(t *testing.T) {
	got := Sweat(Parameters{SweatRate: measure(.8), SweatSodium: measure(900)}, model.CoachingFeedback{
		PreMass: measure(70), PostMass: measure(69.5), FluidL: measure(.8), UrineL: measure(.1), WorkoutMinutes: measure(60),
		SweatPatch: measure(1100),
	})
	if got.SweatRate == nil || math.Abs(*got.SweatRate-.88) > 1e-9 || got.SweatSodium == nil || *got.SweatSodium != 940 {
		t.Fatalf("sweat calibration: %+v", got)
	}
	bad := Sweat(got, model.CoachingFeedback{PreMass: measure(70), PostMass: measure(69), WorkoutMinutes: measure(0)})
	if bad != got {
		t.Fatalf("invalid duration changed calibration: %+v", bad)
	}
}
