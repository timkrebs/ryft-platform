package energy

import (
	"math"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/wearables"
)

func TestCaloricTruth(t *testing.T) {
	start := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	p := Physiology{MassKG: 70, RMR: 1680, RestHR: 55, MaxHR: 190, Sex: "male", FTP: 250}
	for _, tc := range []struct {
		sport      string
		provider   wearables.Provider
		work, want float64
	}{
		{"cycling", wearables.GarminFIT, 600, 600/(4.184*DefaultGME) - 70},
		{"strength", wearables.HealthKit, 0, 3.5*70 - 70},
		{"cycling", wearables.HealthKit, 600, 6*70 - 70},
	} {
		x := Workout(wearables.Workout{Sport: tc.sport, Provider: tc.provider, Start: start, End: start.Add(time.Hour), WorkKJ: tc.work, RawKcal: 900}, p)
		if math.Abs(x.NetKcal-tc.want) > 1e-8 {
			t.Errorf("%+v -> %+v", tc, x)
		}
	}
	x := Workout(wearables.Workout{Sport: "cycling", Start: start, End: start.Add(time.Hour), NormalizedPower: 250, AvgHR: 122.5}, p)
	if !x.HasTRIMP || !x.HasTSS || x.TSS != 100 || math.Abs(x.TRIMP-60*.50*.64*math.Exp(.96)) > 1e-8 {
		t.Fatalf("load %+v", x)
	}
}

func TestAdaptiveFilteringAndDampening(t *testing.T) {
	days := make([]Day, 15)
	today := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	mass, intake, partial, zero := 70., 2500., 100., 0.
	for i := range days {
		days[i] = Day{Date: today.AddDate(0, 0, i-14), Weight: &mass, Intake: &intake, NetExercise: 200}
	}
	days[0].Intake = &partial
	days[3].Weight = nil
	out := Learn(days, 2450, 2450, 7700, time.Time{})
	if !out.Adapted || out.Observed != 2500 || out.TDEE != 2500 || math.Abs(out.Base-(2500-.72*200)) > 1e-8 {
		t.Fatalf("adapted: %+v", out)
	}
	out = Learn(days, 2450, 2450, 7700, today.AddDate(0, 0, -27))
	if out.TDEE != 2460 {
		t.Fatalf("transition: %+v", out)
	}
	days[0].Fasting = true
	days[0].Intake = &zero
	out = Learn(days, 2450, 2450, 7700, time.Time{})
	if out.TDEE != 2350 {
		t.Fatalf("explicit fasting and daily cap: %+v", out)
	}
	out = Learn(days[:14], 2450, 0, 7700, time.Time{})
	if out.Adapted || out.Base != 2450 {
		t.Fatalf("insufficient history: %+v", out)
	}
}
