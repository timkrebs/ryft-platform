package scoring

import "github.com/timkrebs/ryft/internal/model"

// CalibratingReason says why a day is calibrating (DailyScore.calibrating_reason;
// docs/API.md → calibrating_reason, docs/SCORING.md → Calibrating and its
// reason). window is the number of stored days the day's baselines were drawn
// from (ScoreHistory: up to baselineDays scored days before it), hrvDays and
// rhrDays how many of them have an overnight HRV and a resting HR, the N of
// each baseline. In this order:
//
//  1. both baselines have MinBaselineDays nights: "" (not calibrating);
//  2. the window has MinBaselineDays days, yet not one with an HRV: no_hrv;
//  3. likewise without a resting HR: no_rhr;
//  4. otherwise short_history, which more nights end.
//
// So it is "" exactly when Recovery's Calibrating is false. The window test
// keeps a new account, whose counts are 0 because nothing is there yet, at
// short_history. With neither signal no_hrv wins: HRV carries the most recovery
// weight, and baseline_rhr_days == 0 shows the second gap. One to six HRV nights
// are short_history, not no_hrv: sporadic data that more nights do end.
func CalibratingReason(window, hrvDays, rhrDays int) string {
	switch {
	case min(hrvDays, rhrDays) >= MinBaselineDays:
		return ""
	case window >= MinBaselineDays && hrvDays == 0:
		return model.CalibratingNoHRV
	case window >= MinBaselineDays && rhrDays == 0:
		return model.CalibratingNoRHR
	default:
		return model.CalibratingShortHistory
	}
}
