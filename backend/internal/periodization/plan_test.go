package periodization

import (
	"math"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
)

func TestFemaleSustainedLEASuspendsDeficit(t *testing.T) {
	intake, carbs := 2100., 220.
	days := make([]energy.Day, 20)
	for i := range days {
		days[i] = energy.Day{Intake: &intake, Carbs: &carbs, NetExercise: 650, HighStrain: true}
	}
	for _, tc := range []struct {
		n     int
		state string
	}{{5, AvailabilityUnknown}, {14, AvailabilityYellow}, {20, AvailabilityRed}} {
		a := CheckAvailability(days[:tc.n], 70, 56, "female", Recovery{})
		if a.State != tc.state {
			t.Fatalf("%d days: %+v", tc.n, a)
		}
		out := Plan(Input{Profile: model.GoalProfile{Goal: FatLoss, TrainLow: true}, Mass: 70, FFM: 56, Base: 2050, Sex: "female",
			Minutes: 115, NetExercise: 650, Availability: a})
		if !out.DeficitSuspended || out.TrainLow || out.FatG*9 < .20*out.EnergyKcal-.0001 {
			t.Fatalf("guard: %+v", out)
		}
		if tc.state == AvailabilityRed && out.EnergyKcal < 45*56+650 {
			t.Fatalf("maintenance floor: %+v", out)
		}
	}
}

func TestRecoveryConfoundersAndRaceFuel(t *testing.T) {
	delta, swc, cv, previous, rhr, fatigue, rmr := -.08, .04, 3., 2., -3., 8., .85
	a := model.Availability{State: AvailabilityGreen}
	r := Recovery{Today: &model.N2RBiometrics{RMSSDDelta: &delta, SWC: &swc, RMSSDCV: &cv, RHRDelta: &rhr},
		Prior: &model.N2RBiometrics{RMSSDCV: &previous}, Fatigue: &fatigue, RMRRatio: &rmr}
	if state, _ := Readiness(r, a); state != ReadinessFatigue {
		t.Fatal(state)
	}
	if len(redMarkers(r)) != 2 {
		t.Fatalf("paradox markers: %v", redMarkers(r))
	}
	r.YesterdayTags = []string{"alcohol_above_035gkg"}
	if state, _ := Readiness(r, a); state != ReadinessLifestyle {
		t.Fatal(state)
	}
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	race := day.AddDate(0, 0, 2).Format(time.DateOnly)
	out := Plan(Input{Profile: model.GoalProfile{Goal: Marathon, RaceDate: &race}, Day: day, Mass: 69, Base: 2200, Availability: a})
	if out.SessionClass != CarbLoad || out.CarbsG != 759 || math.Abs(out.EnergyKcal-4*(out.CarbsG+out.ProteinG)-9*out.FatG) > 1e-8 {
		t.Fatalf("taper: %+v", out)
	}
}
