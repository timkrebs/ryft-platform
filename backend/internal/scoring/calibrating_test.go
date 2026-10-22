package scoring

import (
	"fmt"
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

// Every branch of the rule in docs/SCORING.md → Calibrating and its reason,
// with the account each row stands for. W is the days with data in the 30-day
// window, H and R how many of them carry an overnight HRV and a resting HR;
// hrv and rhr say whether the day itself has one.
func TestCalibratingReason(t *testing.T) {
	const (
		none  = ""
		short = model.CalibratingShortHistory
		noHRV = model.CalibratingNoHRV
		noRHR = model.CalibratingNoRHR
	)
	for _, c := range []struct {
		name         string
		window, h, r int
		hrv, rhr     bool
		want         string
	}{
		// Day one: no data before it. H = R = 0 like a phone-only account,
		// but waiting is the answer, so not no_hrv.
		{"day one", 0, 0, 0, true, true, short},
		{"day one of a phone", 0, 0, 0, false, false, short},
		// The sixth day of a phone-only account: still too little history to say a
		// signal is missing (W < 7).
		{"six days, no signal yet", 6, 0, 0, false, false, short},
		// The report (53d437eb): a month of scored days, resting HR every night,
		// never an HRV, none this night either. Waiting will not end it.
		{"30 days, no HRV", 30, 0, 30, false, true, noHRV},
		// The mirror case: HRV every night, no resting HR in the window or today.
		{"30 days, no resting HR", 30, 30, 0, true, false, noRHR},
		// Neither signal (iPhone only, 30 days of steps): no_hrv by precedence;
		// baseline_rhr_days == 0 tells the app about the second gap.
		{"30 days, neither", 30, 0, 0, false, false, noHRV},
		// The threshold is the window, not the calendar: the seventh day with data.
		{"seven days, no HRV", 7, 0, 7, false, true, noHRV},
		// The first morning of a watch after 10 phone-only days (the review's
		// case): the window holds the days before, without HRV, but this night has
		// one. A source has appeared, so waiting ends it.
		{"a watch's first morning", 10, 0, 0, true, true, short},
		// The same after 30 days with the watch off.
		{"back after 30 days off", 30, 0, 0, true, true, short},
		// The no-HRV account gets an HRV source: its first night with one.
		{"HRV appears", 30, 0, 30, true, true, short},
		{"resting HR appears", 30, 30, 0, true, true, short},
		// A new source with HRV but no resting HR yet: the resting HR is still
		// missing from the window and the day.
		{"HRV appears, no resting HR anywhere", 30, 0, 0, true, false, noRHR},
		// Sporadic HRV (3 nights of 30): more nights do end it, so short_history,
		// although the resting HR is complete and this night has no HRV.
		{"sporadic HRV", 30, 3, 30, false, true, short},
		// Six nights of both on day seven: one night to go.
		{"one night to go", 6, 6, 6, true, true, short},
		// A complete resting HR, HRV on 6 of 20 days: still short_history.
		{"HRV one short", 20, 6, 20, false, true, short},
		// Both baselines have 7 nights: not calibrating, whatever the window or the day.
		{"exactly seven of both", 7, 7, 7, true, true, none},
		{"seven of both, nothing tonight", 7, 7, 7, false, false, none},
		{"seven of both in a month with gaps", 30, 7, 12, true, true, none},
		{"a full window", 30, 30, 30, true, true, none},
		{"the contract's example", 30, 12, 19, true, true, none},
	} {
		in := Calibration{Window: c.window, HRVDays: c.h, RHRDays: c.r, DayHRV: c.hrv, DayRHR: c.rhr}
		if got := CalibratingReason(in); got != c.want {
			t.Errorf("%s (%+v): %q, want %q", c.name, in, got, c.want)
		}
	}
}

// Over every state the engine can reach (0 ≤ H, R ≤ W ≤ 30, the day with or
// without each signal) the reason is empty exactly when Recovery says the day
// is not calibrating, and each reason means what the rule says it means: a
// signal is missing only when the window is long enough and neither the window
// nor the day has it.
func TestCalibratingReasonInvariants(t *testing.T) {
	for w := 0; w <= baselineDays; w++ {
		for h := 0; h <= w; h++ {
			for r := 0; r <= w; r++ {
				for _, day := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
					in := Calibration{Window: w, HRVDays: h, RHRDays: r, DayHRV: day[0], DayRHR: day[1]}
					at := fmt.Sprintf("%+v", in)
					got := CalibratingReason(in)
					calibrating := Recovery(RecoveryInputs{LnHRVBase: Baseline{N: h}, RHRBase: Baseline{N: r}}).Calibrating
					if (got == "") == calibrating {
						t.Fatalf("%s: reason %q with calibrating %v", at, got, calibrating)
					}
					hrvMissing := w >= MinBaselineDays && h == 0 && !in.DayHRV
					rhrMissing := w >= MinBaselineDays && r == 0 && !in.DayRHR
					switch got {
					case model.CalibratingNoHRV:
						if !hrvMissing {
							t.Fatalf("%s: no_hrv", at)
						}
					case model.CalibratingNoRHR:
						if !rhrMissing || hrvMissing {
							t.Fatalf("%s: no_rhr", at)
						}
					case model.CalibratingShortHistory:
						if hrvMissing || rhrMissing {
							t.Fatalf("%s: short_history although a signal is missing", at)
						}
					}
				}
			}
		}
	}
}
