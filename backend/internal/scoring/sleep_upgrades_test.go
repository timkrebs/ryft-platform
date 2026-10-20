package scoring

import (
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

// at is a local time on 2026-10-18 (day 0) or the days around it.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, 18+day, hour, minute, 0, 0, berlin)
}

func nseg(from, to time.Time, stage string) model.SleepSegment {
	return model.SleepSegment{Start: from, End: to, Stage: stage, Source: "watch"}
}

func TestSplitNightKeepsTheNightAndTakesTheMorningNap(t *testing.T) {
	d := at(0, 0, 0)
	segs := []model.SleepSegment{
		nseg(at(-1, 20, 30), at(-1, 21, 0), model.SleepCore), // dozing on the sofa: ends before 10:00, stays
		nseg(at(-1, 23, 0), at(0, 3, 0), model.SleepCore),
		nseg(at(0, 3, 0), at(0, 7, 0), model.SleepDeep),
		nseg(at(0, 10, 30), at(0, 11, 15), model.SleepCore), // after a 3.5 h pause, ending 11:15: a nap
	}
	napFrom, _ := NapWindow(d)
	night, naps := SplitNight(segs, napFrom)
	if len(night) != 3 || len(naps) != 1 || !naps[0].Start.Equal(at(0, 10, 30)) {
		t.Fatalf("night %d segments, naps %v", len(night), naps)
	}
	if got := SummarizeSleep(night).AsleepMin; got != 30+240+240 {
		t.Fatalf("night asleep %d", got)
	}
	// A late sleeper waking at 11:30 from one block: that is the night, not a nap.
	late := []model.SleepSegment{nseg(at(0, 3, 0), at(0, 11, 30), model.SleepCore)}
	if n, p := SplitNight(late, napFrom); len(n) != 1 || len(p) != 0 {
		t.Fatal("one long block is the night")
	}
}

func TestSplitDay(t *testing.T) {
	d := at(0, 0, 0)
	segs := []model.SleepSegment{
		nseg(at(-1, 15, 0), at(-1, 15, 50), model.SleepCore), // yesterday's nap: not this day's
		nseg(at(-1, 23, 0), at(0, 7, 0), model.SleepCore),
		nseg(at(0, 12, 40), at(0, 13, 0), model.SleepCore), // one nap across 13:00: 20 + 15 minutes
		nseg(at(0, 13, 0), at(0, 13, 15), model.SleepCore),
		nseg(at(0, 20, 30), at(0, 21, 0), model.SleepCore), // the next night's window
	}
	night, nap := SplitDay(segs, d)
	if SummarizeSleep(night).AsleepMin != 480 || nap != 35 {
		t.Fatalf("night %d min, nap %d", SummarizeSleep(night).AsleepMin, nap)
	}
	if _, nap := SplitDay(segs, at(-1, 0, 0)); nap != 50 {
		t.Fatalf("yesterday's nap: %d", nap)
	}
	// Two sources for the afternoon: one is taken, not both.
	phone := nseg(at(0, 15, 0), at(0, 15, 40), model.SleepAsleep)
	phone.Source = "phone"
	watch := nseg(at(0, 15, 0), at(0, 15, 40), model.SleepCore)
	if _, nap := SplitDay([]model.SleepSegment{phone, watch}, d); nap != 40 {
		t.Fatalf("one source per window: %d", nap)
	}
}

func TestNapMinutes(t *testing.T) {
	forty := []model.SleepSegment{nseg(at(0, 15, 0), at(0, 15, 40), model.SleepCore)}
	if got := NapMinutes(forty); got != 40 {
		t.Fatalf("a 40-min nap: %d", got)
	}
	if got := NapMinutes([]model.SleepSegment{nseg(at(0, 15, 0), at(0, 15, 15), model.SleepCore)}); got != 0 {
		t.Fatalf("under 20 min is no nap: %d", got)
	}
	// A 10-minute wake inside one nap: one block, only the sleep counts.
	broken := []model.SleepSegment{nseg(at(0, 13, 0), at(0, 13, 15), model.SleepCore),
		nseg(at(0, 13, 15), at(0, 13, 25), model.SleepAwake), nseg(at(0, 13, 25), at(0, 13, 40), model.SleepCore)}
	if got := NapMinutes(broken); got != 30 {
		t.Fatalf("one nap with a short wake: %d", got)
	}
	two := []model.SleepSegment{nseg(at(0, 11, 0), at(0, 11, 50), model.SleepCore), nseg(at(0, 16, 0), at(0, 17, 0), model.SleepCore)}
	if got := NapMinutes(two); got != 90 {
		t.Fatalf("the credit is capped at 90: %d", got)
	}
}

func TestSleepNeedParts(t *testing.T) {
	p := SleepNeedParts(460, 12.4, 137, 40)
	if p.Base != 460 || p.Strain != 37 || p.Debt != 41 || p.Nap != 40 || p.Total() != 498 {
		t.Fatalf("parts %+v total %d", p, p.Total())
	}
	if p := SleepNeedParts(460, 30, 500, 200); p.Strain != 60 || p.Debt != 60 || p.Nap != 90 {
		t.Fatalf("caps: %+v", p)
	}
	if SleepNeed(460, 12.4, 137) != 538 {
		t.Fatal("without a nap: the need as before M4 (460 + 37.2 + 41.1)")
	}
}

func ip(v int) *int { return &v }

// history builds newest-first scored days ending 2026-10-18.
func history(n int, fill func(k int, h *store.History)) []store.History {
	out := make([]store.History, n)
	for k := range out {
		out[k].Day = time.Date(2026, 10, 18-k, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		fill(k, &out[k])
	}
	return out
}

func TestLearnedSleepBase(t *testing.T) {
	low, high := 6.0, 14.0
	// 20 nights; even days are green after an easy day, odd days are not.
	hist := history(20, func(k int, h *store.History) {
		h.SleepActualMin, h.Strain = ip(400+5*k), &low
		if k%2 == 0 {
			h.Recovery = ip(75)
		} else {
			h.Recovery = ip(50)
		}
	})
	got, ok := LearnedSleepBase(hist)
	// qualifying: k = 0, 2, …, 18 → 400, 410, …, 490; median of 10 = (440 + 450) / 2.
	if !ok || got != 445 {
		t.Fatalf("learned %d ok=%v", got, ok)
	}
	if _, ok := LearnedSleepBase(hist[:13]); ok {
		t.Fatal("fewer than 14 nights: nothing learned")
	}
	// A hard day before kills the next night: every second day hard leaves too few.
	hard := history(20, func(k int, h *store.History) {
		h.SleepActualMin, h.Recovery, h.Strain = ip(450), ip(80), &low
		if k%2 == 1 || k%4 == 0 {
			h.Strain = &high
		}
	})
	if _, ok := LearnedSleepBase(hard); ok {
		t.Fatal("fewer than 7 qualifying nights: nothing learned")
	}
	long := history(14, func(k int, h *store.History) { h.SleepActualMin, h.Recovery = ip(620), ip(90) })
	if got, ok := LearnedSleepBase(long); !ok || got != 570 {
		t.Fatalf("clamped to 570: %d %v", got, ok)
	}
}

// nights with bedtimes and wake times offset by ±spread minutes around 23:00 and 07:00.
func nights(spread int) []Bedtime {
	var out []Bedtime
	for k := 0; k < 6; k++ {
		off := time.Duration(spread) * time.Minute
		if k%2 == 1 {
			off = -off
		}
		out = append(out, Bedtime{Bed: at(-k-1, 23, 0).Add(off), Wake: at(-k, 7, 0).Add(off)})
	}
	return out
}

func TestSleepConsistencyPins(t *testing.T) {
	// σ 15 min for bedtimes and wake times (spread 30 in all) → 85; σ 45 (90) → 55.
	if got, ok := SleepConsistency(nights(15), berlin); !ok || got != 85 {
		t.Fatalf("30 min: %d %v", got, ok)
	}
	if got, ok := SleepConsistency(nights(45), berlin); !ok || got != 55 {
		t.Fatalf("90 min: %d %v", got, ok)
	}
	// 23:30 and 00:30 are an hour apart, not 23 hours.
	mid := []Bedtime{}
	for k := 0; k < 6; k++ {
		bed := at(-k-1, 23, 30)
		if k%2 == 1 {
			bed = at(-k, 0, 30)
		}
		mid = append(mid, Bedtime{Bed: bed, Wake: at(-k, 7, 0)})
	}
	if got, _ := SleepConsistency(mid, berlin); got != 85 {
		t.Fatalf("midnight on the noon axis: %d", got)
	}
	if _, ok := SleepConsistency(nights(15)[:4], berlin); ok {
		t.Fatal("fewer than 5 nights: no score")
	}
}

func TestRestorativeAndQuality(t *testing.T) {
	if got := Restorative(112, 95, 468, true, 87); got != 100 {
		t.Fatalf("deep + REM above 40 %%: %d", got)
	}
	if got := Restorative(60, 60, 480, true, 87); got != 63 {
		t.Fatalf("25 %% of sleep: %d", got)
	}
	if got := Restorative(0, 0, 400, false, 77); got != 77 {
		t.Fatalf("without stages restorative is the performance: %d", got)
	}
	if got := SleepQuality(87, 95, 100); got != 91 {
		t.Fatalf("the API example: %d", got)
	}
	if got := SleepQuality(70, 85, 63); got != 72 {
		t.Fatalf("a mediocre night: %d", got)
	}
}

func TestSleepPlan(t *testing.T) {
	if PlanEfficiency(nil) != 90 || PlanEfficiency([]int{80, 82}) != 85 || PlanEfficiency([]int{92, 94}) != 93 {
		t.Fatal("plan efficiency: mean, floor 85, default 90")
	}
	wake := time.Date(2026, 10, 20, 5, 0, 0, 0, time.UTC)
	p := SleepPlan(SleepNeedParts(460, 12, 53, 0), wake, 90)
	// The API.md example: need 512, in bed 569, bedtimes 19:31 / 20:56 / 22:22.
	if p.NeedMin != 512 || p.InBedMin != 569 || p.Basis.StrainTopup != 36 || p.Basis.DebtTopup != 16 {
		t.Fatalf("plan: %+v", p)
	}
	for want, got := range map[string]time.Time{"19:31": p.Bedtime100, "20:56": p.Bedtime85, "22:22": p.Bedtime70} {
		if got.Format("15:04") != want {
			t.Errorf("bedtime %s, want %s", got.Format("15:04"), want)
		}
	}
	if !p.Bedtime100.Before(p.Bedtime85) || !p.Bedtime85.Before(p.Bedtime70) || !p.Bedtime70.Before(wake) {
		t.Fatal("bedtimes ascend towards the wake time")
	}
}

func TestLearnDue(t *testing.T) {
	sunday := time.Date(2026, 10, 25, 4, 0, 0, 0, berlin)
	week := sunday.Add(-7 * 24 * time.Hour)
	recent := sunday.Add(-2 * time.Hour)
	for _, c := range []struct {
		now     time.Time
		learned *time.Time
		want    bool
	}{
		{sunday, nil, true},
		{sunday, &week, true},
		{sunday, &recent, false},               // already this Sunday
		{sunday.Add(-time.Minute), nil, false}, // before 04:00
		{sunday.AddDate(0, 0, 1), nil, false},  // Monday
	} {
		if got := LearnDue(c.now, c.learned); got != c.want {
			t.Errorf("LearnDue(%v, %v) = %v", c.now, c.learned, got)
		}
	}
}

func TestNextWake(t *testing.T) {
	now := at(0, 22, 15)
	if w, err := NextWake("07:00", now); err != nil || !w.Equal(at(1, 7, 0)) {
		t.Fatalf("after 22:15, 07:00 is tomorrow: %v %v", w, err)
	}
	if w, _ := NextWake("23:30", now); !w.Equal(at(0, 23, 30)) {
		t.Fatalf("23:30 is still today: %v", w)
	}
	if w, _ := NextWake("22:15", now); !w.Equal(at(1, 22, 15)) {
		t.Fatalf("now itself is tomorrow's: %v", w)
	}
	for _, bad := range []string{"", "7:00", "07:60", "24:00", "0700", "07:00:00", "seven"} {
		if _, err := NextWake(bad, now); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestTonightPlan(t *testing.T) {
	strain, need, actual, eff := 12.0, 500, 460, 88
	// Today scored (strain so far, a 30-min nap), six earlier nights 40 min short.
	hist := history(7, func(k int, h *store.History) {
		h.SleepNeedMin, h.SleepActualMin, h.Efficiency = &need, &actual, &eff
		if k == 0 {
			h.Strain, h.NapMin = &strain, ip(30)
		}
	})
	wake := time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC)
	p := TonightPlan(460, "2026-10-18", hist, wake)
	debt := SleepDebtFrom(hist)
	if p.Basis.Base != 460 || p.Basis.StrainTopup != 36 || p.Basis.NapCredit != 30 || p.Efficiency != 88 ||
		p.Basis.DebtTopup != SleepNeedParts(460, 0, debt, 0).Debt || p.NeedMin != 460+36+p.Basis.DebtTopup-30 {
		t.Fatalf("plan %+v (debt %d)", p, debt)
	}
	// Today not scored yet: no strain, no nap; the newest row is yesterday's.
	if p := TonightPlan(460, "2026-10-19", hist, wake); p.Basis.StrainTopup != 0 || p.Basis.NapCredit != 0 {
		t.Fatalf("yesterday's strain is not today's: %+v", p.Basis)
	}
	if p := TonightPlan(460, "2026-10-18", nil, wake); p.NeedMin != 460 || p.Efficiency != 90 {
		t.Fatalf("no history: %+v", p)
	}
}

func TestNeedOf(t *testing.T) {
	old := model.DailyScore{SleepNeedMin: ip(512)}
	if n := NeedOf(old); n.Base != 512 || n.Total != 512 || n.Strain != 0 {
		t.Fatalf("scored before the terms: %+v", n)
	}
	d := model.DailyScore{SleepNeedMin: ip(498), SleepNeedBaseMin: ip(460), SleepNeedStrainMin: ip(37),
		SleepNeedDebtMin: ip(41), SleepNeedNapMin: ip(40)}
	if n := NeedOf(d); n != (model.SleepNeed{Base: 460, Strain: 37, Debt: 41, NapCredit: 40, Total: 498}) {
		t.Fatalf("terms: %+v", n)
	}
}

func TestChangelog(t *testing.T) {
	c, err := Changelog()
	if err != nil || len(c) == 0 || c[0].Version != ModelVersion {
		t.Fatalf("changelog: %v %v", c, err)
	}
}
