package periodization

import (
	"slices"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// The check-in readiness path (D294; docs/API.md → CoachingBriefing.recovery,
// the implementation outline's Readiness and readiness_display rows). The drop
// reasons are scoring's (scoring.CheckinDropBelowUsual, ...); this package
// does not import scoring, so they are spelled out here.
const (
	belowUsual = "checkin_below_usual"
	lowRatings = "checkin_low_quality_and_freshness"
)

// bed is the reported bedtime 22:00 UTC on the evening before the wake day.
var bed = time.Date(2026, 11, 25, 22, 0, 0, 0, time.UTC)

func TestCheckinReadinessTable(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      CheckinState
		state   string
		reasons []string
	}{
		{"no drop", CheckinState{}, ReadinessOptimal, []string{}},
		{"no drop, confounders ignored", CheckinState{Confounders: []string{"alcohol_above_035gkg"}}, ReadinessOptimal, []string{}},
		{"drop + N2R alcohol", CheckinState{AcuteDrop: true, DropReason: lowRatings, Confounders: []string{"alcohol_above_035gkg"}},
			ReadinessLifestyle, []string{"alcohol_above_035gkg", lowRatings}},
		{"drop + journal late meal", CheckinState{AcuteDrop: true, DropReason: belowUsual, Confounders: []string{ConfounderJournalLateMeal}},
			ReadinessLifestyle, []string{ConfounderJournalLateMeal, belowUsual}},
		{"drop alone", CheckinState{AcuteDrop: true, DropReason: belowUsual}, ReadinessLowUnexplained, []string{belowUsual}},
		{"drop alone, low ratings", CheckinState{AcuteDrop: true, DropReason: lowRatings}, ReadinessLowUnexplained, []string{lowRatings}},
		{"sore, no drop", CheckinState{Soreness: true}, ReadinessOptimal, []string{ReasonSorenessReported}},
		{"sore, drop alone", CheckinState{AcuteDrop: true, DropReason: lowRatings, Soreness: true}, ReadinessLowUnexplained,
			[]string{lowRatings, ReasonSorenessReported}},
		{"sore, drop + sick", CheckinState{AcuteDrop: true, DropReason: belowUsual, Confounders: []string{ConfounderJournalSick}, Soreness: true},
			ReadinessLifestyle, []string{ConfounderJournalSick, belowUsual, ReasonSorenessReported}},
	} {
		state, reasons := CheckinReadiness(c.in)
		if state != c.state || !slices.Equal(reasons, c.reasons) || reasons == nil {
			t.Errorf("%s: %s %v, want %s %v", c.name, state, reasons, c.state, c.reasons)
		}
	}
}

func TestCheckinReadinessSorenessChangesOnlyAReason(t *testing.T) {
	for _, base := range []CheckinState{{}, {AcuteDrop: true, DropReason: lowRatings},
		{AcuteDrop: true, DropReason: belowUsual, Confounders: []string{ConfounderJournalTravel}}} {
		sore := base
		sore.Soreness = true
		s1, r1 := CheckinReadiness(base)
		s2, r2 := CheckinReadiness(sore)
		if s1 != s2 || !slices.Equal(append(slices.Clone(r1), ReasonSorenessReported), r2) {
			t.Errorf("%+v: %s %v vs sore %s %v", base, s1, r1, s2, r2)
		}
	}
}

func TestCheckinConfounders(t *testing.T) {
	at := func(h, m int) *time.Time { v := time.Date(2026, 11, 25, h, m, 0, 0, time.UTC); return &v }
	for _, c := range []struct {
		name string
		tags []string
		j    JournalFacts
		want []string
	}{
		{"nothing", nil, JournalFacts{}, []string{}},
		{"N2R tags in order, others ignored", []string{"late_satfat_meal", "low_fiber", "alcohol_above_035gkg"}, JournalFacts{},
			[]string{"alcohol_above_035gkg", "late_satfat_meal"}},
		{"journal alcohol (yes/no)", nil, JournalFacts{Alcohol: true}, []string{ConfounderJournalAlcohol}},
		{"journal alcohol (count)", nil, JournalFacts{AlcoholDrinks: 2}, []string{ConfounderJournalAlcohol}},
		{"caffeine after 2 pm", nil, JournalFacts{CaffeineAfter2pm: true}, []string{ConfounderJournalLateCaffeine}},
		// last_caffeine within 8 h of the 22:00 bedtime: 15:00 (7 h) and 14:00
		// (exactly 8 h) count, 13:59 does not; one after the bedtime does.
		{"last caffeine 7 h before bed", nil, JournalFacts{LastCaffeine: at(15, 0)}, []string{ConfounderJournalLateCaffeine}},
		{"last caffeine 8 h before bed", nil, JournalFacts{LastCaffeine: at(14, 0)}, []string{ConfounderJournalLateCaffeine}},
		{"last caffeine 8 h 1 min before bed", nil, JournalFacts{LastCaffeine: at(13, 59)}, []string{}},
		{"last caffeine after bed", nil, JournalFacts{LastCaffeine: at(22, 30)}, []string{ConfounderJournalLateCaffeine}},
		{"late meal", nil, JournalFacts{LateMeal: true}, []string{ConfounderJournalLateMeal}},
		{"sick, travel, work stress", nil, JournalFacts{Sick: true, Travel: true, WorkStress: true},
			[]string{ConfounderJournalSick, ConfounderJournalTravel, ConfounderJournalWorkStress}},
		{"all, N2R first", []string{"caffeine_bed_high"}, JournalFacts{Alcohol: true, CaffeineAfter2pm: true, LateMeal: true, Sick: true},
			[]string{"caffeine_bed_high", ConfounderJournalAlcohol, ConfounderJournalLateCaffeine, ConfounderJournalLateMeal, ConfounderJournalSick}},
	} {
		if got := CheckinConfounders(true, c.tags, c.j, bed); !slices.Equal(got, c.want) || got == nil {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Without a drop there are none to report ([] on the check-in).
	if got := CheckinConfounders(false, []string{"alcohol_above_035gkg"}, JournalFacts{Sick: true}, bed); got == nil || len(got) != 0 {
		t.Errorf("no drop: %v", got)
	}
	// The function takes the journal of the day before only: "sick" answered on
	// the wake day is not in it (the store passes D − 1's answers), so a drop
	// with nothing on the day before stays unexplained.
	var dayBefore JournalFacts // the wake day's journal says sick; the day before says nothing
	state, reasons := CheckinReadiness(CheckinState{AcuteDrop: true, DropReason: lowRatings,
		Confounders: CheckinConfounders(true, nil, dayBefore, bed)})
	if state != ReadinessLowUnexplained || !slices.Equal(reasons, []string{lowRatings}) {
		t.Errorf("sick on the wake day only: %s %v", state, reasons)
	}
}

func TestReadinessWithCheckin(t *testing.T) {
	delta, swc, cv, previous := -.08, .04, 3., 2.
	green := model.Availability{State: AvailabilityGreen}
	drop := &CheckinState{AcuteDrop: true, DropReason: lowRatings}

	// HRV present but no prior CV: the HRV path is UNKNOWN, the check-in decides.
	noPrior := Recovery{Today: &model.N2RBiometrics{RMSSDDelta: &delta, SWC: &swc, RMSSDCV: &cv}, Prior: &model.N2RBiometrics{}}
	if s, _ := Readiness(noPrior, green); s != ReadinessUnknown {
		t.Fatalf("HRV path without a prior CV: %s", s)
	}
	if s, r := ReadinessWithCheckin(noPrior, green, drop); s != ReadinessLowUnexplained || !slices.Equal(r, []string{lowRatings}) {
		t.Errorf("check-in path: %s %v", s, r)
	}
	// No overnight biometrics at all: the same.
	if s, _ := ReadinessWithCheckin(Recovery{}, green, &CheckinState{}); s != ReadinessOptimal {
		t.Errorf("no biometrics, no drop: %s", s)
	}
	// No check-in (or the flag off, or superseded): UNKNOWN as before.
	if s, r := ReadinessWithCheckin(Recovery{}, green, nil); s != ReadinessUnknown || len(r) != 0 {
		t.Errorf("no check-in: %s %v", s, r)
	}
	// HRV biometrics complete: the HRV path, the check-in ignored, whether it
	// would say better (OPTIMAL) or worse.
	full := Recovery{Today: &model.N2RBiometrics{RMSSDDelta: &delta, SWC: &swc, RMSSDCV: &cv}, Prior: &model.N2RBiometrics{RMSSDCV: &previous}}
	if s, r := ReadinessWithCheckin(full, green, &CheckinState{}); s != ReadinessFatigue || !slices.Equal(r, []string{"lnrmssd_below_swc", "lnrmssd_cv_rising"}) {
		t.Errorf("HRV path fatigue: %s %v", s, r)
	}
	up := .02
	fine := Recovery{Today: &model.N2RBiometrics{RMSSDDelta: &up, SWC: &swc, RMSSDCV: &cv}, Prior: &model.N2RBiometrics{RMSSDCV: &previous}}
	if s, _ := ReadinessWithCheckin(fine, green, drop); s != ReadinessOptimal {
		t.Errorf("HRV path optimal, check-in drop ignored: %s", s)
	}
	// The guardrail first.
	yellow := model.Availability{State: AvailabilityYellow, Markers: []string{"persistent_low_energy_availability"}}
	if s, r := ReadinessWithCheckin(Recovery{}, yellow, drop); s != ReadinessGuardrail || !slices.Equal(r, yellow.Markers) {
		t.Errorf("guardrail: %s %v", s, r)
	}
	// Never AUTONOMIC_FATIGUE from a check-in, whatever it holds.
	for _, d := range []bool{false, true} {
		for _, conf := range [][]string{nil, {ConfounderJournalSick}} {
			for _, sore := range []bool{false, true} {
				c := &CheckinState{AcuteDrop: d, DropReason: belowUsual, Confounders: conf, Soreness: sore}
				if s, _ := ReadinessWithCheckin(Recovery{}, green, c); s == ReadinessFatigue {
					t.Errorf("%+v: AUTONOMIC_FATIGUE from a check-in", c)
				}
			}
		}
	}
}

// fatLoss is a fat-loss account on a rest day: 80 kg, 64 kg FFM, base 2400,
// availability green. Unprotected: the deficit min(500, 0.15·2400 = 360) and
// 3.5 g/kg carbohydrate (280 g); protected: no deficit and the 4.5 g/kg floor
// (360 g).
func fatLoss(readiness string, reasons []string) model.CoachingPlan {
	return Plan(Input{Profile: model.GoalProfile{Goal: FatLoss}, Day: bed, Mass: 80, FFM: 64, Base: 2400, Sex: "male",
		Availability: model.Availability{State: AvailabilityGreen}, Readiness: readiness, ReadinessReasons: reasons})
}

func TestCheckinReadinessPlans(t *testing.T) {
	for _, c := range []struct {
		name      string
		readiness string
		reasons   []string
		protected bool
	}{
		{"optimal", ReadinessOptimal, []string{}, false},
		{"unknown", ReadinessUnknown, []string{}, false},
		{"HRV fatigue (unchanged)", ReadinessFatigue, []string{"lnrmssd_below_swc", "lnrmssd_cv_rising"}, true},
		{"HRV lifestyle (unchanged)", ReadinessLifestyle, []string{"alcohol_above_035gkg"}, false},
		{"check-in unexplained", ReadinessLowUnexplained, []string{lowRatings}, true},
		{"check-in lifestyle, travel", ReadinessLifestyle, []string{ConfounderJournalTravel, belowUsual}, false},
		{"check-in lifestyle, work stress", ReadinessLifestyle, []string{ConfounderJournalWorkStress, belowUsual}, false},
		{"check-in lifestyle, sick", ReadinessLifestyle, []string{ConfounderJournalSick, belowUsual}, true},
		{"check-in lifestyle, alcohol and sick", ReadinessLifestyle, []string{"alcohol_above_035gkg", ConfounderJournalSick, lowRatings}, true},
	} {
		p := fatLoss(c.readiness, c.reasons)
		carbs := 3.5 * 80.
		if c.protected {
			carbs = 4.5 * 80
		}
		if p.DeficitSuspended != c.protected || p.CarbsG != carbs || p.TrainLow {
			t.Errorf("%s: deficit suspended %v, carbs %v; want %v, %v", c.name, p.DeficitSuspended, p.CarbsG, c.protected, carbs)
		}
	}
	// LOW_RECOVERY_UNEXPLAINED gets exactly the HRV unexplained drop's plan.
	if a, b := fatLoss(ReadinessFatigue, nil), fatLoss(ReadinessLowUnexplained, nil); a.EnergyKcal != b.EnergyKcal ||
		a.CarbsG != b.CarbsG || a.ProteinG != b.ProteinG || a.FatG != b.FatG || a.DeficitSuspended != b.DeficitSuspended {
		t.Errorf("plans differ: %+v vs %+v", a, b)
	}
	// The deficit itself: unprotected 2400 − 360 = 2040 kcal before the macro floors.
	if p := fatLoss(ReadinessOptimal, nil); p.EnergyKcal >= fatLoss(ReadinessLowUnexplained, nil).EnergyKcal {
		t.Errorf("an unprotected day must keep the deficit: %v", p.EnergyKcal)
	}
}

func TestReadinessDisplay(t *testing.T) {
	for state, want := range map[string]string{
		ReadinessFatigue:        ReadinessLowUnexplained,
		ReadinessLowUnexplained: ReadinessLowUnexplained,
		ReadinessOptimal:        ReadinessOptimal,
		ReadinessLifestyle:      ReadinessLifestyle,
		ReadinessGuardrail:      ReadinessGuardrail,
		ReadinessUnknown:        ReadinessUnknown,
		"SOMETHING_NEW":         "SOMETHING_NEW",
	} {
		if got := ReadinessDisplay(state); got != want {
			t.Errorf("%s: %s, want %s", state, got, want)
		}
	}
}
