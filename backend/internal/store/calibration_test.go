package store

import (
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

// knownCalibration serves the counts and the reason only when they belong to
// the row's baseline_days and calibrating (no database needed).
func TestKnownCalibration(t *testing.T) {
	n := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		name string
		in   model.DailyScore
		kept bool
	}{
		{"scored by this version, calibrating",
			model.DailyScore{BaselineDays: 0, BaselineHRVDays: n(0), BaselineRHRDays: n(24), Calibrating: true,
				CalibratingReason: s(model.CalibratingNoHRV)}, true},
		{"scored by this version, not calibrating",
			model.DailyScore{BaselineDays: 12, BaselineHRVDays: n(12), BaselineRHRDays: n(19)}, true},
		{"scored before the fields existed",
			model.DailyScore{BaselineDays: 3, Calibrating: true}, false},
		// An older image re-scored it: 7 nights now, not calibrating, but the
		// columns it does not know still say 6 and short_history.
		{"written again by an older image, now not calibrating",
			model.DailyScore{BaselineDays: 7, BaselineHRVDays: n(6), BaselineRHRDays: n(9),
				CalibratingReason: s(model.CalibratingShortHistory)}, false},
		// The other way: a new night, still calibrating, the counts are stale.
		{"written again by an older image, counts moved",
			model.DailyScore{BaselineDays: 5, BaselineHRVDays: n(4), BaselineRHRDays: n(4), Calibrating: true,
				CalibratingReason: s(model.CalibratingShortHistory)}, false},
		{"calibrating without a reason",
			model.DailyScore{BaselineDays: 3, BaselineHRVDays: n(3), BaselineRHRDays: n(5), Calibrating: true}, false},
	} {
		d := c.in
		knownCalibration(&d)
		kept := d.BaselineHRVDays != nil
		if kept != c.kept {
			t.Errorf("%s: kept %v, want %v", c.name, kept, c.kept)
		}
		if !kept && (d.BaselineRHRDays != nil || d.CalibratingReason != nil) {
			t.Errorf("%s: all three go together, got %+v", c.name, d)
		}
		if kept && (*d.BaselineHRVDays != *c.in.BaselineHRVDays || d.CalibratingReason != c.in.CalibratingReason) {
			t.Errorf("%s: changed what it kept", c.name)
		}
		if d.BaselineDays != c.in.BaselineDays || d.Calibrating != c.in.Calibrating {
			t.Errorf("%s: touched baseline_days or calibrating", c.name)
		}
	}
}
