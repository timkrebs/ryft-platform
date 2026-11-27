package periodization

import (
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Readiness from a morning check-in (D294; docs/API.md → CoachingBriefing.recovery):
// used only where the HRV path yields UNKNOWN, after the availability
// guardrail. It names what the user reported, never a cause: it never emits
// AUTONOMIC_FATIGUE.

// The check-in path's confounders and reasons (readiness_reasons and
// RecoveryCheckin.confounders; an open set for the app). The N2R tags are the
// HRV path's (Recovery.confounders); the journal ones apply to this path only.
const (
	ConfounderJournalAlcohol      = "journal_alcohol"
	ConfounderJournalLateCaffeine = "journal_late_caffeine"
	ConfounderJournalLateMeal     = "journal_late_meal"
	ConfounderJournalSick         = "journal_sick"
	ConfounderJournalTravel       = "journal_travel"
	ConfounderJournalWorkStress   = "journal_work_stress"
	ReasonSorenessReported        = "soreness_reported"

	// lateCaffeineWindow: a last caffeine within 8 h of the reported bedtime
	// counts as late (RYFT heuristic, D294; with caffeine's elimination
	// half-life of about 5 h on average, a third of a dose is left after 8 h).
	lateCaffeineWindow = 8 * time.Hour
)

// n2rConfounderTags are the N2R tags of D − 1 that explain a drop, in order.
var n2rConfounderTags = []string{"alcohol_above_035gkg", "caffeine_bed_high", "late_satfat_meal"}

// JournalFacts is the journal of the day before the wake day (the evening
// before the night; never the wake day itself, D294), by behaviour slug:
// yes/no behaviours are value_bool, alcohol_drinks is value_num, and
// last_caffeine is its local "HH:MM" on that journal day resolved in the
// user's zone (nil when unanswered). The zero value is a day without answers.
type JournalFacts struct {
	Alcohol          bool // alcohol
	AlcoholDrinks    int  // alcohol_drinks
	CaffeineAfter2pm bool // caffeine_after_2pm
	LastCaffeine     *time.Time
	LateMeal         bool // late_meal
	Sick             bool // sick
	Travel           bool // travel
	WorkStress       bool // work_stress
}

// CheckinConfounders is what the evening before may explain a drop with:
// the N2R tags of D − 1 (from the live N2R with the reported bedtime, D293),
// then the journal of the day before, in a fixed order. Always non-nil;
// empty without acuteDrop (RecoveryCheckin.confounders is [] then).
func CheckinConfounders(acuteDrop bool, n2rTags []string, dayBefore JournalFacts, bedtime time.Time) []string {
	found := []string{}
	if !acuteDrop {
		return found
	}
	for _, tag := range n2rConfounderTags {
		if slices.Contains(n2rTags, tag) {
			found = append(found, tag)
		}
	}
	j := dayBefore
	late := j.CaffeineAfter2pm || j.LastCaffeine != nil && bedtime.Sub(*j.LastCaffeine) <= lateCaffeineWindow
	for _, c := range []struct {
		on  bool
		key string
	}{
		{j.Alcohol || j.AlcoholDrinks > 0, ConfounderJournalAlcohol},
		{late, ConfounderJournalLateCaffeine},
		{j.LateMeal, ConfounderJournalLateMeal},
		{j.Sick, ConfounderJournalSick},
		{j.Travel, ConfounderJournalTravel},
		{j.WorkStress, ConfounderJournalWorkStress},
	} {
		if c.on {
			found = append(found, c.key)
		}
	}
	return found
}

// CheckinState is a current check-in as readiness reads it: the scored drop
// (scoring.CheckinResult.AcuteDrop and DropReason), its confounders
// (CheckinConfounders) and the soreness answer.
type CheckinState struct {
	AcuteDrop   bool
	DropReason  string
	Confounders []string
	Soreness    bool
}

// CheckinReadiness is the D294 table: no drop → OPTIMAL; a drop with
// confounders → ACUTE_LIFESTYLE_SUPPRESSION, the confounders then the drop
// reason; a drop without → LOW_RECOVERY_UNEXPLAINED with the drop reason.
// Soreness only appends soreness_reported. Never AUTONOMIC_FATIGUE.
func CheckinReadiness(c CheckinState) (string, []string) {
	state, reasons := ReadinessOptimal, []string{}
	if c.AcuteDrop {
		state = ReadinessLowUnexplained
		if len(c.Confounders) > 0 {
			state = ReadinessLifestyle
			reasons = append(reasons, c.Confounders...)
		}
		if c.DropReason != "" {
			reasons = append(reasons, c.DropReason)
		}
	}
	if c.Soreness {
		reasons = append(reasons, ReasonSorenessReported)
	}
	return state, reasons
}

// ReadinessWithCheckin is Readiness, with the check-in path where that yields
// UNKNOWN (no overnight HRV, or not enough for its baseline) and the day has a
// current check-in (checkin non-nil; nil while the flag is off or the check-in
// is superseded). The guardrail and every HRV result come first, unchanged.
func ReadinessWithCheckin(r Recovery, a model.Availability, checkin *CheckinState) (string, []string) {
	state, reasons := Readiness(r, a)
	if state != ReadinessUnknown || checkin == nil {
		return state, reasons
	}
	return CheckinReadiness(*checkin)
}

// ReadinessDisplay is CoachingBriefing.readiness_display (D294): the readiness
// state, except that AUTONOMIC_FATIGUE is shown as LOW_RECOVERY_UNEXPLAINED
// (what was observed, not a cause). The plan keeps reading the state itself.
func ReadinessDisplay(readiness string) string {
	if readiness == ReadinessFatigue {
		return ReadinessLowUnexplained
	}
	return readiness
}

// protectiveReadiness says whether the readiness state suspends a fat-loss
// deficit and raises carbohydrate to the 4.5 g/kg floor: the HRV path's
// unexplained drop, the check-in path's (LOW_RECOVERY_UNEXPLAINED), and a
// lifestyle drop the user explained with feeling sick (D294: a deficit while
// ill is the riskier default). Only the check-in path emits journal_sick, so
// the HRV path's plans do not change.
func protectiveReadiness(state string, reasons []string) bool {
	switch state {
	case ReadinessFatigue, ReadinessLowUnexplained:
		return true
	case ReadinessLifestyle:
		return slices.Contains(reasons, ConfounderJournalSick)
	}
	return false
}
