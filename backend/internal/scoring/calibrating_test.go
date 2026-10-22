package scoring

import (
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

// Every branch of the rule in docs/API.md → calibrating_reason, with the
// account each row stands for. W is the stored days in the 30-day window, H and
// R how many of them carry an overnight HRV and a resting HR.
func TestCalibratingReason(t *testing.T) {
	const (
		none  = ""
		short = model.CalibratingShortHistory
		noHRV = model.CalibratingNoHRV
		noRHR = model.CalibratingNoRHR
	)
	for _, c := range []struct {
		name      string
		window, h int
		r         int
		want      string
	}{
		// Day one: nothing stored before it. H = R = 0 like a phone-only account,
		// but waiting is the answer, so not no_hrv.
		{"day one", 0, 0, 0, short},
		// The sixth day of a phone-only account: still too little history to say a
		// signal is missing (W < 7).
		{"six days, no signal yet", 6, 0, 0, short},
		// The report (53d437eb): a month of scored days, resting HR every night,
		// never an HRV. Waiting will not end it.
		{"30 days, no HRV", 30, 0, 30, noHRV},
		// The mirror case: HRV every night, no resting HR in the window.
		{"30 days, no resting HR", 30, 30, 0, noRHR},
		// Neither signal (iPhone only, 30 days of steps): no_hrv by precedence;
		// baseline_rhr_days == 0 tells the app about the second gap.
		{"30 days, neither", 30, 0, 0, noHRV},
		// The threshold is the window, not the calendar: the seventh stored day.
		{"seven days, no HRV", 7, 0, 7, noHRV},
		// Sporadic HRV (3 nights of 30): more nights do end it, so short_history,
		// although the resting HR is complete.
		{"sporadic HRV", 30, 3, 30, short},
		// Six nights of both on day seven: one night to go.
		{"one night to go", 6, 6, 6, short},
		// A complete resting HR, HRV on 6 of 20 days: still short_history.
		{"HRV one short", 20, 6, 20, short},
		// Both baselines have 7 nights: not calibrating, whatever the window.
		{"exactly seven of both", 7, 7, 7, none},
		{"seven of both in a month with gaps", 30, 7, 12, none},
		{"a full window", 30, 30, 30, none},
		{"the contract's example", 30, 12, 19, none},
	} {
		if got := CalibratingReason(c.window, c.h, c.r); got != c.want {
			t.Errorf("%s (W=%d, H=%d, R=%d): %q, want %q", c.name, c.window, c.h, c.r, got, c.want)
		}
	}
}

// Over every state the engine can reach (0 ≤ H, R ≤ W ≤ 30) the reason is
// empty exactly when Recovery says the day is not calibrating, and each reason
// means what the contract's table says it means.
func TestCalibratingReasonInvariants(t *testing.T) {
	for w := 0; w <= baselineDays; w++ {
		for h := 0; h <= w; h++ {
			for r := 0; r <= w; r++ {
				got := CalibratingReason(w, h, r)
				calibrating := Recovery(RecoveryInputs{LnHRVBase: Baseline{N: h}, RHRBase: Baseline{N: r}}).Calibrating
				if (got == "") == calibrating {
					t.Fatalf("W=%d H=%d R=%d: reason %q with calibrating %v", w, h, r, got, calibrating)
				}
				switch got {
				case model.CalibratingNoHRV:
					if w < MinBaselineDays || h != 0 {
						t.Fatalf("W=%d H=%d R=%d: no_hrv", w, h, r)
					}
				case model.CalibratingNoRHR:
					if w < MinBaselineDays || r != 0 || h == 0 {
						t.Fatalf("W=%d H=%d R=%d: no_rhr", w, h, r)
					}
				case model.CalibratingShortHistory:
					if w >= MinBaselineDays && (h == 0 || r == 0) {
						t.Fatalf("W=%d H=%d R=%d: short_history although a signal is missing", w, h, r)
					}
				}
			}
		}
	}
}
