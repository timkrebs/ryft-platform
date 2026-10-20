package insights

import (
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func f(v float64) *float64 { return &v }
func n(v int) *int         { return &v }

var today = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

// hist builds a calendar of len(days) days ending today from per-day setters.
func hist(days ...func(*Day)) History {
	h := History{Days: make([]Day, len(days))}
	for k, set := range days {
		h.Days[k] = Day{Day: today.AddDate(0, 0, k-(len(days)-1)).Format("2006-01-02")}
		if set != nil {
			set(&h.Days[k])
		}
	}
	return h
}

func hrv(v, base float64) func(*Day) {
	return func(d *Day) { d.HRV, d.HRVBase = f(v), f(base) }
}

func ids(in []model.Insight) string {
	var s []string
	for _, x := range in {
		s = append(s, x.ID)
	}
	return strings.Join(s, ",")
}

func TestHRVBelowBaseline(t *testing.T) {
	in := hrvBelowBaseline(hist(hrv(60, 55), hrv(50, 55), hrv(49, 55), hrv(47, 55)))
	if in == nil || in.ID != "hrv-low-3d" || in.Severity != "warn" || in.Day != "2026-10-18" {
		t.Fatalf("3 low nights must warn: %+v", in)
	}
	if in.Title != "HRV below your baseline for 3 nights" {
		t.Fatalf("title %q", in.Title)
	}
	if hrvBelowBaseline(hist(hrv(50, 55), hrv(60, 55), hrv(49, 55), hrv(47, 55))) != nil {
		t.Fatal("2 low nights must not fire")
	}
	// Today not synced yet: the streak ends yesterday.
	if in := hrvBelowBaseline(hist(hrv(50, 55), hrv(49, 55), hrv(47, 55), nil)); in == nil || in.Day != "2026-10-17" {
		t.Fatalf("streak ending yesterday must fire for yesterday: %+v", in)
	}
	// Two days without data: stale, silent.
	if hrvBelowBaseline(hist(hrv(50, 55), hrv(49, 55), hrv(47, 55), nil, nil)) != nil {
		t.Fatal("a streak that ended 2+ days ago must not fire")
	}
	// A night without HRV in between breaks consecutiveness.
	if hrvBelowBaseline(hist(hrv(50, 55), nil, hrv(49, 55), hrv(47, 55))) != nil {
		t.Fatal("a missing night breaks the streak")
	}
	calibrating := hist(hrv(50, 55), hrv(49, 55), func(d *Day) { hrv(47, 55)(d); d.Calibrating = true })
	if hrvBelowBaseline(calibrating) != nil {
		t.Fatal("no baseline-relative cards while calibrating")
	}
}

func TestRHRElevated(t *testing.T) {
	up := hist(func(d *Day) { d.RHR, d.RHRBase = f(56), f(50.5) })
	if in := rhrElevated(up); in == nil || in.Title != "Resting heart rate up 6 bpm" {
		t.Fatalf("5.5 bpm above must warn: %+v", in)
	}
	if rhrElevated(hist(func(d *Day) { d.RHR, d.RHRBase = f(54), f(50) })) != nil {
		t.Fatal("4 bpm above must not fire")
	}
}

func TestSleepDebt(t *testing.T) {
	if in := sleepDebt(hist(func(d *Day) { d.SleepDebtMin = n(95) })); in == nil || in.Title != "Sleep debt: 1h 35m" {
		t.Fatalf("95 min debt: %+v", in)
	}
	if sleepDebt(hist(func(d *Day) { d.SleepDebtMin = n(89) })) != nil {
		t.Fatal("89 min must not fire")
	}
}

func day(rec int, strain float64) func(*Day) {
	return func(d *Day) { d.Recovery, d.Strain = n(rec), f(strain) }
}

func TestStrainWithoutRecovery(t *testing.T) {
	in := strainWithoutRecovery(hist(day(70, 15), day(50, 8), day(45, 16), day(40, 14.2), nil))
	if in == nil || in.Title != "3 hard days without recovering" || in.Day != "2026-10-17" {
		t.Fatalf("3 hard days since the last green: %+v", in)
	}
	if strainWithoutRecovery(hist(day(50, 15), day(50, 16), day(70, 8), day(50, 15))) != nil {
		t.Fatal("a green recovery in between resets the count")
	}
	// A green morning followed by a hard day starts a new run with that day.
	if in := strainWithoutRecovery(hist(day(30, 15), day(75, 15), day(50, 15), day(50, 17))); in == nil {
		t.Fatal("green-morning hard day counts as the first of the new run")
	}
}

func bed(h, m int) func(*Day) {
	return func(d *Day) {
		t, _ := time.Parse("2006-01-02", d.Day)
		b := t.Add(-24 * time.Hour).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		if h < 12 { // after midnight: same calendar day as the wake-up
			b = b.Add(24 * time.Hour)
		}
		d.Bedtime = &b
	}
}

func TestBedtimeConsistency(t *testing.T) {
	steady := hist(bed(22, 45), bed(23, 0), bed(23, 15), bed(22, 50), bed(23, 30), bed(23, 5), bed(23, 0))
	if bedtimeConsistency(steady) != nil {
		t.Fatal("45 min spread is consistent")
	}
	// Midnight wrap: 23:00 → 01:00 is 2 h, not 22 h.
	late := hist(bed(23, 0), bed(23, 0), bed(1, 0), bed(23, 15), bed(22, 50), nil, bed(23, 0))
	in := bedtimeConsistency(late)
	if in == nil || !strings.Contains(in.Body, "varied by 2h 10m") || !strings.Contains(in.Body, "latest 01:00") {
		t.Fatalf("2h10 spread across midnight: %+v", in)
	}
	if bedtimeConsistency(hist(bed(21, 0), bed(1, 0), nil, nil, nil, bed(23, 0), bed(23, 0))) != nil {
		t.Fatal("fewer than 5 nights is not enough to judge")
	}
}

func ready(v int) func(*Day) { return func(d *Day) { d.Readiness = n(v) } }

func TestReadinessTrendingUp(t *testing.T) {
	in := readinessTrendingUp(hist(ready(55), ready(58), ready(60), ready(62), nil, ready(66), ready(70)))
	if in == nil || in.Severity != "info" || !strings.Contains(in.Body, "climbed about 14 points") {
		t.Fatalf("rising readiness: %+v", in)
	}
	if readinessTrendingUp(hist(ready(70), ready(60), ready(72), ready(61), ready(70), ready(62), ready(71))) != nil {
		t.Fatal("noisy flat readiness must not fire")
	}
}

func TestBestHRV30d(t *testing.T) {
	days := make([]func(*Day), Window)
	for k := range days {
		v := 48.0 + float64(k%5)
		days[k] = func(d *Day) { d.HRV = f(v) }
	}
	days[Window-1] = func(d *Day) { d.HRV = f(61) }
	if in := bestHRV30d(hist(days...)); in == nil || in.ID != "hrv-best-30d" {
		t.Fatalf("highest of 30 nights: %+v", in)
	}
	days[3] = func(d *Day) { d.HRV = f(61) } // a tie earlier in the window
	if bestHRV30d(hist(days...)) != nil {
		t.Fatal("matching an earlier night is not a new best")
	}
	if bestHRV30d(hist(hrv(40, 50), hrv(70, 50))) != nil {
		t.Fatal("two nights are not enough history")
	}
}

func TestEvaluateOrdersBySeverity(t *testing.T) {
	h := hist(
		func(d *Day) { hrv(50, 55)(d); ready(50)(d) },
		func(d *Day) { hrv(49, 55)(d); ready(55)(d) },
		func(d *Day) { hrv(48, 55)(d); ready(60)(d) },
		func(d *Day) { hrv(47, 55)(d); ready(64)(d) },
		func(d *Day) { hrv(46, 55)(d); ready(70)(d); d.SleepDebtMin = n(120) },
	)
	if got := ids(Evaluate(h)); got != "hrv-low-3d,sleep-debt,readiness-up" {
		t.Fatalf("got %s", got)
	}
	if got := Evaluate(hist(nil, nil)); got == nil || len(got) != 0 {
		t.Fatal("no data → empty (non-nil) list")
	}
}

func TestNewHistoryFillsTheCalendar(t *testing.T) {
	scores := []model.DailyScore{{Day: "2026-10-16", Readiness: n(70)}, {Day: "2026-10-18", Readiness: n(80)}}
	b := today.Add(-90 * time.Minute)
	h := NewHistory(today, 4, scores, map[string]time.Time{"2026-10-18": b})
	if len(h.Days) != 4 || h.Days[0].Day != "2026-10-15" || h.Days[3].Day != "2026-10-18" {
		t.Fatalf("calendar %+v", h.Days)
	}
	if h.Days[0].Readiness != nil || *h.Days[1].Readiness != 70 || h.Days[2].Readiness != nil || *h.Days[3].Readiness != 80 {
		t.Fatal("scores must land on their days, gaps stay empty")
	}
	if h.Days[3].Bedtime == nil || !h.Days[3].Bedtime.Equal(b) {
		t.Fatal("bedtime not attached")
	}
}

func TestHelpers(t *testing.T) {
	if hm(50) != "50m" || hm(95) != "1h 35m" || hm(125) != "2h 05m" {
		t.Fatal("hm")
	}
	if clock(660) != "23:00" || clock(780) != "01:00" {
		t.Fatalf("clock: %s %s", clock(660), clock(780))
	}
	if s, i := linearFit([]float64{0, 1, 2}, []float64{1, 3, 5}); s != 2 || i != 1 {
		t.Fatalf("fit %v %v", s, i)
	}
}

func TestPersonalRecord(t *testing.T) {
	pr := func(prs ...PR) func(*Day) { return func(d *Day) { d.PRs = prs } }
	squat := PR{Exercise: "Back squat", Kind: model.PRMaxWeight, Value: 140}
	e1rm := PR{Exercise: "Back squat", Kind: model.PRE1RM, Value: 152.5}
	got := personalRecord(hist(nil, pr(squat, e1rm)))
	if got == nil || got.ID != "pr" || got.Severity != model.SeverityInfo || got.Metric != model.MetricVolumeKG ||
		!strings.Contains(got.Body, "152.5 kg") || !strings.Contains(got.Body, "2 records") {
		t.Fatalf("an e1RM record leads the card: %+v", got)
	}
	if got := personalRecord(hist(pr(squat), nil)); got == nil || !strings.Contains(got.Body, "140 kg on Back squat") {
		t.Fatalf("yesterday's record still shows: %+v", got)
	}
	if personalRecord(hist(pr(squat), nil, nil)) != nil {
		t.Fatal("a record two days ago is old news")
	}
	h := NewHistory(today, 3, nil, nil).WithPRs(map[string][]PR{"2026-10-18": {squat}})
	if ids(Evaluate(h)) != "pr" {
		t.Fatalf("WithPRs places records on their day: %s", ids(Evaluate(h)))
	}
}

func TestSleepConsistencyLow(t *testing.T) {
	cons := func(v int) func(*Day) { return func(d *Day) { d.Consistency = n(v) } }
	if in := sleepConsistencyLow(hist(cons(80), cons(55))); in == nil || in.ID != "sleep-consistency-low" ||
		in.Severity != model.SeverityNotice || in.Metric != "sleep_consistency" {
		t.Fatalf("55 must fire: %+v", in)
	}
	if sleepConsistencyLow(hist(cons(55), cons(60))) != nil {
		t.Fatal("today's 60 is fine, whatever yesterday was")
	}
	if in := sleepConsistencyLow(hist(cons(55), nil)); in == nil || in.Day != "2026-10-17" {
		t.Fatalf("before today syncs, yesterday counts: %+v", in)
	}
}

func TestNapCredit(t *testing.T) {
	nap := func(v int) func(*Day) { return func(d *Day) { d.NapMin = n(v) } }
	in := napCredit(hist(nil, nap(50)))
	if in == nil || in.ID != "nap-credit" || in.Metric != "nap_min" || in.Title != "50m nap" ||
		!strings.Contains(in.Body, "tonight's sleep need is 50m lower") {
		t.Fatalf("a 50-min nap today: %+v", in)
	}
	if napCredit(hist(nap(60), nap(0))) != nil {
		t.Fatal("today synced without a nap: yesterday's is old news")
	}
	if in := napCredit(hist(nap(60), nil)); in == nil || !strings.Contains(in.Body, "last night's") {
		t.Fatalf("yesterday's nap before today syncs: %+v", in)
	}
	if napCredit(hist(nil, nap(40))) != nil {
		t.Fatal("under 45 min: no card")
	}
}

func TestBaselineLearned(t *testing.T) {
	at := today.Add(5 * time.Hour)
	base := func(v int) func(*Day) { return func(d *Day) { d.SleepBase = n(v) } }
	// The day before the rescored week, then the week (learned today).
	week := func(before int) History {
		days := []func(*Day){base(before)}
		for range 7 {
			days = append(days, base(475))
		}
		return hist(days...)
	}
	learned := Baseline{Source: model.BaseLearned, Min: 475, LearnedAt: &at}
	in := baselineLearned(week(460).WithBaseline(learned))
	if in == nil || in.ID != "baseline-learned" || in.Title != "Your sleep need: 7h 55m" || in.Day != "2026-10-18" {
		t.Fatalf("a new learned baseline: %+v", in)
	}
	if baselineLearned(week(475).WithBaseline(learned)) != nil {
		t.Fatal("the same estimate as last week: no card")
	}
	manual := learned
	manual.Source = model.BaseManual
	if baselineLearned(week(460).WithBaseline(manual)) != nil {
		t.Fatal("set by hand: the estimate is not in use")
	}
	old := at.AddDate(0, 0, -2)
	stale := learned
	stale.LearnedAt = &old
	if baselineLearned(week(460).WithBaseline(stale)) != nil {
		t.Fatal("learned 2 days ago: no card")
	}
	if baselineLearned(hist(nil, nil).WithBaseline(learned)) == nil {
		t.Fatal("no older scores (first estimate): card")
	}
}

func TestBehaviorImpact(t *testing.T) {
	eff := func(d, lo, hi float64, sig bool) *model.BehaviorEffect {
		return &model.BehaviorEffect{Delta: d, CILo: lo, CIHi: hi, Significant: sig, NYes: 12, NNo: 31}
	}
	h := hist(nil, nil, nil).WithBehaviors([]BehaviorSignal{
		{Name: "Late meal", Recovery: eff(-6, -9, -2, true)},
		{Name: "Alcohol", Recovery: eff(-14.2, -19.8, -8.1, true)},
		{Name: "Sauna", Recovery: eff(5, 2, 8, true)},
		{Name: "Meditation", Recovery: nil},
	})
	in := behaviorImpact(h)
	if in == nil || in.ID != "behavior-impact" || in.Title != "Alcohol yesterday" || in.Day != "2026-10-17" ||
		!strings.Contains(in.Body, "14 points lower") || !strings.Contains(in.Body, "12 days with it, 31 without") {
		t.Fatalf("the largest negative effect: %+v", in)
	}
	if behaviorImpact(hist(nil, nil).WithBehaviors([]BehaviorSignal{{Name: "Alcohol", Recovery: eff(-8, -15, 1, false)}})) != nil {
		t.Fatal("an interval that includes 0: no card")
	}
	if behaviorImpact(hist(nil, nil)) != nil {
		t.Fatal("nothing logged: no card")
	}
}

func TestMaxHRObserved(t *testing.T) {
	with := func(current int, peaks map[string]float64) History {
		return NewHistory(today, 3, nil, nil).WithMaxHR(MaxHR{Current: current, Peaks: peaks})
	}
	in := maxHRObserved(with(190, map[string]float64{"2026-08-01": 201, "2026-09-12": 196.4, "2026-10-01": 192}))
	if in == nil || in.ID != "max-hr-observed" || in.Severity != model.SeverityInfo || in.Metric != model.MetricStrain ||
		in.Day != "2026-09-12" || !strings.Contains(in.Title, "196") || !strings.Contains(in.Body, "190 bpm") {
		t.Fatalf("the lower of the two highest days, 196: %+v", in)
	}
	if maxHRObserved(with(190, map[string]float64{"2026-08-01": 205, "2026-09-12": 192.9})) != nil {
		t.Fatal("one day above is a glitch, not a pattern (the second is under 193)")
	}
	if in := maxHRObserved(with(190, map[string]float64{"2026-08-01": 193, "2026-09-12": 193})); in == nil ||
		in.Day != "2026-09-12" || !strings.Contains(in.Title, "193") {
		t.Fatalf("exactly 3 above counts; the later day of a tie: %+v", in)
	}
	if maxHRObserved(with(190, map[string]float64{"2026-08-01": 199})) != nil || maxHRObserved(hist(nil)) != nil {
		t.Fatal("needs two days of workouts")
	}
	if ids(Evaluate(with(180, map[string]float64{"2026-08-01": 185, "2026-08-02": 184}))) != "max-hr-observed" {
		t.Fatal("Evaluate runs the rule")
	}
}
