package scoring

import "github.com/timkrebs/ryft/internal/model"

// Calibration is what CalibratingReason decides from: the day's window and
// what the day itself brought.
type Calibration struct {
	// Window (W): the days with data (store.History.HasData) among the stored
	// days the day's baselines were drawn from (ScoreHistory: up to baselineDays
	// scored days before it). HRVDays (H) and RHRDays (R): how many of them have
	// an overnight HRV and a resting HR, the N of each baseline.
	Window, HRVDays, RHRDays int
	// DayHRV, DayRHR: the day itself has an overnight HRV, a resting HR.
	DayHRV, DayRHR bool
}

// CalibratingReason says why a day is calibrating (DailyScore.calibrating_reason;
// docs/API.md → calibrating_reason, docs/SCORING.md → Calibrating and its
// reason). In this order:
//
//  1. both baselines have MinBaselineDays nights: "" (not calibrating);
//  2. the window has MinBaselineDays days with data, not one with an HRV, and
//     the day has none either: no_hrv;
//  3. likewise for the resting HR: no_rhr;
//  4. otherwise short_history, which more nights end.
//
// So it is "" exactly when Recovery's Calibrating is false. The window test
// keeps a new account, whose counts are 0 because nothing is there yet, at
// short_history; days stored without data (a range or a rescore that starts
// before the account's first sync) are not history and do not count. The day's
// own signal keeps the first morning of a new source (a watch after weeks of
// phone-only days) at short_history: the window only holds the days before it,
// so it cannot know yet. With neither signal no_hrv wins: HRV carries the most
// recovery weight, and baseline_rhr_days == 0 shows the second gap. One to six
// HRV nights are short_history, not no_hrv: sporadic data that more nights do end.
func CalibratingReason(c Calibration) string {
	switch {
	case min(c.HRVDays, c.RHRDays) >= MinBaselineDays:
		return ""
	case c.Window >= MinBaselineDays && c.HRVDays == 0 && !c.DayHRV:
		return model.CalibratingNoHRV
	case c.Window >= MinBaselineDays && c.RHRDays == 0 && !c.DayRHR:
		return model.CalibratingNoRHR
	default:
		return model.CalibratingShortHistory
	}
}
