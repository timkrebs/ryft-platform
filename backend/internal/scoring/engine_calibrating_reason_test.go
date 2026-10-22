package scoring

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// seedSignals writes 40 nights (2026-09-01 .. 2026-10-10) of sleep and, when
// asked, a resting heart rate in the morning and an overnight HRV sample.
func seedSignals(t *testing.T, st *store.Store, user uuid.UUID, hrv, rhr bool) {
	t.Helper()
	ctx := context.Background()
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var segs []model.SleepSegment
	var samples []model.Sample
	for k := 0; k < 40; k++ {
		d := first.AddDate(0, 0, k)
		segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour), End: d.Add(7 * time.Hour),
			Stage: model.SleepCore, Source: "watch"})
		if rhr {
			samples = append(samples, model.Sample{Type: model.SampleRestingHR, Start: d.Add(9 * time.Hour),
				End: d.Add(9 * time.Hour), Value: 55 + float64(k%3), Unit: "count/min", Source: "watch"})
		}
		if hrv {
			samples = append(samples, model.Sample{Type: model.SampleHRVSDNN, Start: d.Add(3 * time.Hour),
				End: d.Add(3 * time.Hour), Value: 60 + float64(k%5), Unit: "ms", Source: "watch"})
		}
	}
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	if len(samples) > 0 {
		if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
			t.Fatal(err)
		}
	}
}

// calib is what a stored day says about its calibration.
type calib struct {
	days, hrv, rhr int
	calibrating    bool
	reason         string // "" for null
}

func calibOf(t *testing.T, g model.DailyScore) calib {
	t.Helper()
	if g.BaselineHRVDays == nil || g.BaselineRHRDays == nil {
		t.Fatalf("%s: a day scored now has both counts, got %v %v", g.Day, g.BaselineHRVDays, g.BaselineRHRDays)
	}
	c := calib{days: g.BaselineDays, hrv: *g.BaselineHRVDays, rhr: *g.BaselineRHRDays, calibrating: g.Calibrating}
	if g.CalibratingReason != nil {
		c.reason = *g.CalibratingReason
	}
	return c
}

// Forty nights scored oldest first, one account per set-up. Each day's window
// W is the days scored before it (day k of the range has k-1), so the reason
// can be worked out by hand: the 7th day (2026-09-07) has W = 6, the 8th W = 7,
// the 40th the full window of 30.
func TestEngineCalibratingReason(t *testing.T) {
	short, noHRV, noRHR := model.CalibratingShortHistory, model.CalibratingNoHRV, model.CalibratingNoRHR
	for _, c := range []struct {
		name     string
		hrv, rhr bool
		want     map[string]calib
	}{
		// A watch that writes both: short_history for a week, counting up; then not
		// calibrating. Day one has W = H = R = 0 and is short_history, not no_hrv.
		{"both signals", true, true, map[string]calib{
			"2026-09-01": {0, 0, 0, true, short},
			"2026-09-05": {4, 4, 4, true, short},
			"2026-09-07": {6, 6, 6, true, short},
			"2026-09-08": {7, 7, 7, false, ""},
			"2026-10-10": {30, 30, 30, false, ""},
		}},
		// The report: resting HR and sleep, never an HRV. short_history while W < 7,
		// no_hrv from the 8th day on, for good; R counts up behind it.
		{"no HRV source", false, true, map[string]calib{
			"2026-09-01": {0, 0, 0, true, short},
			"2026-09-07": {0, 0, 6, true, short},
			"2026-09-08": {0, 0, 7, true, noHRV},
			"2026-10-10": {0, 0, 30, true, noHRV},
		}},
		// HRV without a resting HR: no_rhr from the 8th day on.
		{"no resting-HR source", true, false, map[string]calib{
			"2026-09-07": {0, 6, 0, true, short},
			"2026-09-08": {0, 7, 0, true, noRHR},
			"2026-10-10": {0, 30, 0, true, noRHR},
		}},
		// Sleep only (a phone with a sleep schedule): neither signal, no_hrv by
		// precedence; baseline_rhr_days == 0 shows the second gap.
		{"neither", false, false, map[string]calib{
			"2026-09-07": {0, 0, 0, true, short},
			"2026-09-08": {0, 0, 0, true, noHRV},
			"2026-10-10": {0, 0, 0, true, noHRV},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, engine, user := engineStore(t)
			seedSignals(t, st, user, c.hrv, c.rhr)
			if err := engine.ComputeRange(context.Background(), user, "2026-09-01", "2026-10-10"); err != nil {
				t.Fatal(err)
			}
			days := scoresByDay(t, st, user, "2026-09-01", "2026-10-10")
			if len(days) != 40 {
				t.Fatalf("%d days scored, want 40", len(days))
			}
			for day, want := range c.want {
				if got := calibOf(t, days[day]); got != want {
					t.Errorf("%s: %+v, want %+v", day, got, want)
				}
			}
			for day, g := range days { // the contract's invariants, on every day
				got := calibOf(t, g)
				if got.days != min(got.hrv, got.rhr) || (got.reason == "") == got.calibrating {
					t.Errorf("%s: %+v breaks baseline_days = min(H, R) or reason ⇔ calibrating", day, got)
				}
			}
		})
	}
}

// A range or a rescore that starts before the account's first data stores
// empty days (scripts/rescore_all.sh goes 400 days back for everyone). They
// are not history: the first night with data is short_history, not no_hrv as
// 30 stored rows without an HRV would say, and the reason comes on the 8th day
// with data, as without them. The baselines (and baseline_days) are as before.
func TestEngineCalibratingReasonIgnoresEmptyDays(t *testing.T) {
	short, noHRV := model.CalibratingShortHistory, model.CalibratingNoHRV
	for _, c := range []struct {
		name string
		hrv  bool
		want map[string]calib
	}{
		{"both signals", true, map[string]calib{
			"2026-08-31": {0, 0, 0, true, short}, // empty itself, 30 empty days before it
			"2026-09-01": {0, 0, 0, true, short}, // the first night: W = 0, not 30
			"2026-09-07": {6, 6, 6, true, short},
			"2026-09-08": {7, 7, 7, false, ""},
		}},
		{"no HRV source", false, map[string]calib{
			"2026-09-01": {0, 0, 0, true, short},
			"2026-09-07": {0, 0, 6, true, short},
			"2026-09-08": {0, 0, 7, true, noHRV},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, engine, user := engineStore(t)
			seedSignals(t, st, user, c.hrv, true) // data from 2026-09-01 on
			if err := engine.ComputeRange(context.Background(), user, "2026-07-31", "2026-09-10"); err != nil {
				t.Fatal(err)
			}
			days := scoresByDay(t, st, user, "2026-07-31", "2026-09-10")
			if len(days) != 42 {
				t.Fatalf("%d days stored, want 42 (32 of them empty)", len(days))
			}
			for day, want := range c.want {
				if got := calibOf(t, days[day]); got != want {
					t.Errorf("%s: %+v, want %+v", day, got, want)
				}
			}
		})
	}
}

// Whether a day has data is a history input: a phone's steps for days that
// were stored empty, synced late, re-score the days after them (store.ScoreWrite),
// and the 8th day of steps without an HRV turns from short_history to no_hrv.
func TestEngineLateStepsRescoreTheReason(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	steps := func(days ...string) {
		t.Helper()
		var xs []model.Sample
		for _, day := range days {
			d, _ := time.Parse(dayLayout, day)
			xs = append(xs, model.Sample{Type: model.SampleSteps, Start: d.Add(10 * time.Hour),
				End: d.Add(11 * time.Hour), Value: 800, Unit: "count", Source: "phone"})
		}
		if _, err := st.UpsertSamples(ctx, user, xs); err != nil {
			t.Fatal(err)
		}
	}
	week := []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-05", "2026-09-06", "2026-09-07"}
	steps("2026-09-08")
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-09-08"); err != nil { // the week stored empty
		t.Fatal(err)
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-09-08", "2026-09-08")["2026-09-08"]); got.reason != model.CalibratingShortHistory {
		t.Fatalf("one day of steps after an empty week: %+v", got)
	}
	steps(week...)
	if err := engine.ComputeDays(ctx, user, week); err != nil { // the sync's touched days only
		t.Fatal(err)
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-09-08", "2026-09-08")["2026-09-08"]); got !=
		(calib{0, 0, 0, true, model.CalibratingNoHRV}) {
		t.Fatalf("after the week's steps: %+v, want no_hrv", got)
	}
}

// A watch's first morning is not "no HRV": 10 phone-only days (steps), then a
// watch from 09-11 to 09-20, then 35 phone-only days, then the watch again on
// 10-26. The window holds only the days before, so on each first watch morning
// it has no HRV yet; the night's own HRV makes it short_history.
func TestEngineCalibratingReasonNewSourceMorning(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	watch := func(d time.Time) bool {
		return (d.Month() == 9 && d.Day() >= 11 && d.Day() <= 20) || (d.Month() == 10 && d.Day() == 26)
	}
	var segs []model.SleepSegment
	var samples []model.Sample
	for d := first; !d.After(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		samples = append(samples, model.Sample{Type: model.SampleSteps, Start: d.Add(10 * time.Hour),
			End: d.Add(11 * time.Hour), Value: 900, Unit: "count", Source: "phone"})
		if watch(d) {
			segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour), End: d.Add(7 * time.Hour),
				Stage: model.SleepCore, Source: "watch"})
			samples = append(samples,
				model.Sample{Type: model.SampleHRVSDNN, Start: d.Add(3 * time.Hour), End: d.Add(3 * time.Hour),
					Value: 60, Unit: "ms", Source: "watch"},
				model.Sample{Type: model.SampleRestingHR, Start: d.Add(9 * time.Hour), End: d.Add(9 * time.Hour),
					Value: 55, Unit: "count/min", Source: "watch"})
		}
	}
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-26"); err != nil {
		t.Fatal(err)
	}
	days := scoresByDay(t, st, user, "2026-09-01", "2026-10-26")
	short, noHRV := model.CalibratingShortHistory, model.CalibratingNoHRV
	for day, want := range map[string]calib{
		"2026-09-07": {0, 0, 0, true, short}, // W = 6
		"2026-09-10": {0, 0, 0, true, noHRV}, // W = 9, a phone: nothing tonight either
		"2026-09-11": {0, 0, 0, true, short}, // the watch's first morning
		"2026-09-12": {1, 1, 1, true, short},
		"2026-10-25": {0, 0, 0, true, noHRV}, // 30 phone days in the window
		"2026-10-26": {0, 0, 0, true, short}, // the watch is back
	} {
		if got := calibOf(t, days[day]); got != want {
			t.Errorf("%s: %+v, want %+v", day, got, want)
		}
	}
}

// A day scored before its history, then the history (the case PR #29 fixes):
// the re-score in the same pass also brings the counts and the reason along,
// from short_history with an empty window to null with a full one.
func TestEngineCalibratingReasonFollowsTheRescore(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedSignals(t, st, user, true, true)
	if err := engine.ComputeDay(ctx, user, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]); got !=
		(calib{0, 0, 0, true, model.CalibratingShortHistory}) {
		t.Fatalf("scored alone: %+v", got)
	}
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-09"); err != nil {
		t.Fatal(err)
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]); got !=
		(calib{30, 30, 30, false, ""}) {
		t.Fatalf("after its history: %+v", got)
	}
}

// The safety net sees a stale count behind baseline_days: HRV every night, a
// resting HR on 4 mornings only (09-15 .. 09-18; 09-19 takes 09-18's), so
// baseline_days is the resting-HR count and does not move when the HRV count
// does. 10-10 is scored when 20 days lie before it (H 20, R 5), then the days
// between are scored without the re-score after them (a lost cascade): 10-10's
// window now has H 30, R 5. Neither baseline_days nor a missing baseline shows
// it; baseline_hrv_days 20 < 30 does, and once re-scored it is not picked again.
func TestEngineRescoreBehindSeesAStaleCount(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedSignals(t, st, user, true, false)
	var rhr []model.Sample
	for d := time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC); d.Day() <= 18; d = d.AddDate(0, 0, 1) {
		rhr = append(rhr, model.Sample{Type: model.SampleRestingHR, Start: d, End: d, Value: 55, Unit: "count/min", Source: "watch"})
	}
	if _, err := st.UpsertSamples(ctx, user, rhr); err != nil {
		t.Fatal(err)
	}
	order := []string{}
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); d.Day() <= 20; d = d.AddDate(0, 0, 1) {
		order = append(order, d.Format(dayLayout))
	}
	order = append(order, "2026-10-10")
	for d := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC); d.Before(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		order = append(order, d.Format(dayLayout))
	}
	for _, d := range order {
		if _, err := engine.computeDay(ctx, user, d); err != nil {
			t.Fatal(err)
		}
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]); got !=
		(calib{5, 20, 5, true, model.CalibratingShortHistory}) {
		t.Fatalf("10-10 before the safety net: %+v", got)
	}
	behind, err := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 50)
	if err != nil || !slices.Equal(behind, []string{"2026-10-10"}) {
		t.Fatalf("behind: %v %v, want only 2026-10-10", behind, err)
	}
	if _, err := engine.RescoreBehind(ctx, user, "2026-10-10", 50); err != nil {
		t.Fatal(err)
	}
	if got := calibOf(t, scoresByDay(t, st, user, "2026-10-10", "2026-10-10")["2026-10-10"]); got !=
		(calib{5, 30, 5, true, model.CalibratingShortHistory}) {
		t.Fatalf("10-10 after the safety net: %+v", got)
	}
	if again, _ := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 50); len(again) != 0 {
		t.Fatalf("picked again after its re-score: %v", again)
	}
}

// Freshly scored days are never "behind": for every stored row, the counts the
// engine wrote (from ScoreHistory, in Go) equal the counts of the 30 stored rows
// before it, and store.BehindHistoryDays (the same window in SQL) picks nothing,
// so the 15-minute pass cannot re-pick a day it, or a sync, just scored. Five
// accounts: both signals in order; no HRV source; days missing in between (no
// row: the window is rows, not dates); 31 empty days before the first data; a
// backfill newest first, settled by the re-score after each day.
func TestEngineSafetyNetConvergesOnFreshScores(t *testing.T) {
	var all []string
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !d.After(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		all = append(all, d.Format(dayLayout))
	}
	inOrder := func(from string) func(context.Context, *Engine, uuid.UUID) error {
		return func(ctx context.Context, e *Engine, u uuid.UUID) error {
			return e.ComputeRange(ctx, u, from, "2026-10-10")
		}
	}
	for _, c := range []struct {
		name  string
		hrv   bool
		from  string
		score func(context.Context, *Engine, uuid.UUID) error
	}{
		{"both signals, in order", true, "2026-09-01", inOrder("2026-09-01")},
		{"no HRV source", false, "2026-09-01", inOrder("2026-09-01")},
		{"days missing in between", true, "2026-09-01", func(ctx context.Context, e *Engine, u uuid.UUID) error {
			var some []string
			for k, d := range all {
				if k%5 != 2 {
					some = append(some, d)
				}
			}
			return e.ComputeDays(ctx, u, some)
		}},
		{"empty days before the data", true, "2026-07-31", inOrder("2026-07-31")},
		{"backfill newest first", true, "2026-09-01", func(ctx context.Context, e *Engine, u uuid.UUID) error {
			for k := len(all) - 1; k >= 0; k-- {
				if err := e.ComputeDay(ctx, u, all[k]); err != nil {
					return err
				}
			}
			return nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, engine, user := engineStore(t)
			ctx := context.Background()
			seedSignals(t, st, user, c.hrv, true)
			if err := c.score(ctx, engine, user); err != nil {
				t.Fatal(err)
			}
			rows, err := st.DailyScores(ctx, user, c.from, "2026-10-10")
			if err != nil || len(rows) == 0 {
				t.Fatalf("%d rows, %v", len(rows), err)
			}
			for k, r := range rows {
				var h, n int
				for _, p := range rows[max(0, k-baselineDays):k] {
					if p.HRVms != nil && *p.HRVms > 0 {
						h++
					}
					if p.RHR != nil && *p.RHR > 0 {
						n++
					}
				}
				if got := calibOf(t, r); got.hrv != h || got.rhr != n || got.days != min(h, n) {
					t.Errorf("%s: stored H/R %d/%d (baseline_days %d), its 30 rows before hold %d/%d",
						r.Day, got.hrv, got.rhr, got.days, h, n)
				}
			}
			behind, err := st.BehindHistoryDays(ctx, user, c.from, baselineDays, 1000)
			if err != nil || len(behind) != 0 {
				t.Fatalf("freshly scored days picked as behind: %v %v", behind, err)
			}
		})
	}
}
