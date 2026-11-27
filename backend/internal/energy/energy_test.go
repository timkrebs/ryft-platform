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

// Session RPE (D293; docs/SCORING.md → Check-in recovery → Session RPE).
// Banister's TRIMP with r = RPE/10: 60 min at 8 is 60 · 0.8 · a · e^(0.8·b):
// 36 · e^1.436 = 151.3 with the default 0.75/1.795, 30.72 · e^1.536 = 142.7
// male, 41.28 · e^1.336 = 157.0 female; 60 min at 3 is 13.5 · e^0.5385 = 23.1.
func TestSessionRPELoad(t *testing.T) {
	for _, c := range []struct {
		minutes, rpe float64
		sex          string
		want         int
	}{{60, 8, "", 151}, {60, 8, "male", 143}, {60, 8, "female", 157}, {60, 3, "", 23}, {60, 3, "other", 23}} {
		if got := int(math.Round(SessionRPETRIMP(c.minutes, c.rpe, c.sex))); got != c.want {
			t.Errorf("%v min at %v (%q): %d, want %d", c.minutes, c.rpe, c.sex, got, c.want)
		}
	}
	// Foster's AU: whole minutes (rounded) × RPE.
	for _, c := range []struct {
		minutes, rpe float64
		want         int
	}{{60, 8, 480}, {45.4, 7, 315}, {45.6, 7, 322}, {30, 6.5, 195}, {60, 0.5, 0}, {60, 11, 0}, {0, 8, 0}, {60, math.NaN(), 0}} {
		if got := SessionRPELoad(c.minutes, c.rpe); got != c.want {
			t.Errorf("AU %v min at %v: %d, want %d", c.minutes, c.rpe, got, c.want)
		}
	}

	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	hand := wearables.Workout{Sport: "strength", Provider: wearables.HealthKit, UserEntered: true, Start: start, End: start.Add(time.Hour)}
	p := Physiology{MassKG: 70, RMR: 1680, RestHR: 55, MaxHR: 190}
	// No heart rate, no power, rated 8: the coaching load is the rating's.
	x := RatedWorkout(hand, p, 8)
	if !x.HasRPE || x.HasTRIMP || x.HasTSS || math.Round(x.Load()) != 151 || x.TRIMP != 0 {
		t.Errorf("rated, no HR: %+v (load %v)", x, x.Load())
	}
	// The rating does not move net energy.
	if x.NetKcal != Workout(hand, p).NetKcal {
		t.Errorf("net energy moved: %v vs %v", x.NetKcal, Workout(hand, p).NetKcal)
	}
	// Unrated: as before, no load.
	if x := RatedWorkout(hand, p, 0); x.HasRPE || x.Load() != 0 {
		t.Errorf("unrated: %+v", x)
	}
	// With usable heart rate the rating is ignored: the load is the HR TRIMP
	// (reserve (122.5 − 55)/135 = 0.5 → 60 · 0.5 · 0.75 · e^0.8975).
	hr := hand
	hr.AvgHR = 122.5
	x = RatedWorkout(hr, p, 8)
	if x.HasRPE || !x.HasTRIMP || x.Load() != Workout(hr, p).TRIMP || math.Abs(x.Load()-60*.5*.75*math.Exp(.8975)) > 1e-8 {
		t.Errorf("with HR: %+v", x)
	}
	// Heart rate without a usable reserve (no resting HR) is no usable heart
	// rate: the rating counts.
	if x := RatedWorkout(hr, Physiology{MassKG: 70, RMR: 1680, MaxHR: 190}, 8); !x.HasRPE || math.Round(x.Load()) != 151 {
		t.Errorf("HR without a reserve: %+v", x)
	}
	// Power wins too.
	ride := wearables.Workout{Sport: "cycling", Start: start, End: start.Add(time.Hour), NormalizedPower: 250}
	if x := RatedWorkout(ride, Physiology{FTP: 250}, 3); x.HasRPE || x.Load() != 100 {
		t.Errorf("with power: %+v", x)
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
