package scoring

import (
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func tp(t time.Time) *time.Time { return &t }

// The brand board's Sleep screen: 7h48m asleep, deep 1h52 24 %, light 4h21 56 %,
// REM 1h35 20 %, awake 10 m 2 %, quality 82 % "Good" — which is performance 72,
// efficiency 95 and restorative 100 (deep + REM 44 % of sleep).
func TestNightSummaryMatchesBrandBoard(t *testing.T) {
	bed := time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC)
	s, ok := NightSummary(store.SleepNight{
		Day: "2026-10-18", Start: tp(bed), End: tp(bed.Add(492 * time.Minute)),
		TotalMin: i(468), InBedMin: i(492), DeepMin: i(112), LightMin: i(261), REMMin: i(95), AwakeMin: i(10),
		HasStages: true, Performance: i(72),
	})
	if !ok {
		t.Fatal("night with totals must summarise")
	}
	want := []model.SleepStage{
		{Stage: "deep", Min: 112, Pct: 24}, {Stage: "light", Min: 261, Pct: 56},
		{Stage: "rem", Min: 95, Pct: 20}, {Stage: "awake", Min: 10, Pct: 2},
	}
	for k, w := range want {
		if s.Stages[k] != w {
			t.Errorf("stage %d = %+v, want %+v", k, s.Stages[k], w)
		}
	}
	if s.TotalMin != 468 || s.InBedMin != 492 || s.Efficiency != 95 || s.Quality != 82 || s.QualityLabel != "good" ||
		s.Restorative != 100 {
		t.Fatalf("unexpected summary %+v", s)
	}
}

func TestNightSummaryWithoutTypedColumns(t *testing.T) {
	if _, ok := NightSummary(store.SleepNight{Day: "2026-10-18", TotalMin: i(400)}); ok {
		t.Fatal("rows scored before migration 0003 have no start/end and must not summarise")
	}
}

func TestSharesOf100(t *testing.T) {
	cases := []struct{ parts, want []int }{
		{[]int{112, 261, 95}, []int{24, 56, 20}},
		{[]int{1, 1, 1}, []int{34, 33, 33}},
		{[]int{0, 0, 0}, []int{0, 0, 0}},
		{[]int{0, 50, 0}, []int{0, 100, 0}},
	}
	for _, c := range cases {
		got := sharesOf100(c.parts)
		sum := 0
		for k := range got {
			sum += got[k]
			if got[k] != c.want[k] {
				t.Errorf("sharesOf100(%v) = %v, want %v", c.parts, got, c.want)
				break
			}
		}
		if sum != 0 && sum != 100 {
			t.Errorf("sharesOf100(%v) sums to %d", c.parts, sum)
		}
	}
}

func TestSleepQualityLabel(t *testing.T) {
	for q, want := range map[int]string{100: "good", 80: "good", 79: "fair", 60: "fair", 59: "poor", 0: "poor"} {
		if got := SleepQualityLabel(q); got != want {
			t.Errorf("SleepQualityLabel(%d) = %q, want %q", q, got, want)
		}
	}
}

func TestInBedMinutes(t *testing.T) {
	bed := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	withInBed := SleepTotals{Start: bed, End: bed.Add(8 * time.Hour), InBedMin: 480, AsleepMin: 440, AwakeMin: 20}
	if got := InBedMinutes(withInBed); got != 480 {
		t.Errorf("recorded inbed should win, got %d", got)
	}
	stagesOnly := SleepTotals{Start: bed, End: bed.Add(7 * time.Hour), AsleepMin: 400, AwakeMin: 10}
	if got := InBedMinutes(stagesOnly); got != 420 {
		t.Errorf("without inbed the span counts, got %d", got)
	}
	short := SleepTotals{Start: bed, End: bed.Add(8 * time.Hour), InBedMin: 300, AsleepMin: 440, AwakeMin: 20}
	if got := InBedMinutes(short); got != 460 {
		t.Errorf("in bed is never less than asleep + awake, got %d", got)
	}
}

func TestSleepRangeSummary(t *testing.T) {
	bed := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	night := func(day string, total, inBed, deep, light, rem, awake, perf int, staged bool) store.SleepNight {
		return store.SleepNight{Day: day, Start: tp(bed), End: tp(bed.Add(time.Duration(inBed) * time.Minute)),
			TotalMin: i(total), InBedMin: i(inBed), DeepMin: i(deep), LightMin: i(light), REMMin: i(rem),
			AwakeMin: i(awake), HasStages: staged, Performance: i(perf)}
	}
	n1, n2 := night("2026-10-13", 420, 460, 80, 260, 80, 20, 80, true), night("2026-10-14", 480, 500, 100, 280, 100, 10, 90, true)
	n1.Consistency, n2.Consistency, n2.NapMin = i(80), i(91), i(40)
	r := SleepRangeSummary("week", "2026-10-12", "2026-10-18", []store.SleepNight{
		n1, n2,
		night("2026-10-15", 390, 430, 0, 0, 0, 0, 70, false), // source without stages, too few nights for consistency
		{Day: "2026-10-16", TotalMin: i(400)},                // scored before migration 0003: skipped
	})
	if r.AvgConsistency == nil || *r.AvgConsistency != 86 || *r.Days[1].NapMin != 40 || r.Days[2].Consistency != nil {
		t.Fatalf("consistency %v, days %+v", r.AvgConsistency, r.Days)
	}
	if len(r.Bedtimes) != 3 || r.Bedtimes[1].Day != "2026-10-14" || !r.Bedtimes[1].Wake.Equal(bed.Add(500*time.Minute)) {
		t.Fatalf("bedtimes %+v", r.Bedtimes)
	}
	if r.Nights != 3 || len(r.Days) != 3 {
		t.Fatalf("want 3 nights, got %d (%d days)", r.Nights, len(r.Days))
	}
	// quality 85, 93, 74 (0.6·perf + 0.2·eff + 0.2·restorative)
	if *r.AvgTotalMin != 430 || *r.AvgQuality != 84 || *r.AvgInBedMin != 463 {
		t.Fatalf("averages: total %d quality %d in bed %d", *r.AvgTotalMin, *r.AvgQuality, *r.AvgInBedMin)
	}
	// Stage averages ignore the unstaged night: deep (80+100)/2 = 90.
	if len(r.AvgStages) != 4 || r.AvgStages[0].Min != 90 || r.AvgStages[1].Min != 270 {
		t.Fatalf("avg stages %+v", r.AvgStages)
	}
	if empty := SleepRangeSummary("month", "a", "b", nil); empty.AvgTotalMin != nil || empty.Days == nil || empty.AvgStages == nil ||
		empty.Bedtimes == nil || empty.AvgConsistency != nil {
		t.Fatalf("empty range must have null averages and empty (not null) arrays: %+v", empty)
	}
}

func TestHypnogram(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	got := Hypnogram([]model.SleepSegment{
		{Start: at(60), End: at(90), Stage: model.SleepDeep},
		{Start: at(0), End: at(480), Stage: model.SleepInBed},
		{Start: at(10), End: at(60), Stage: model.SleepCore},
		{Start: at(90), End: at(120), Stage: model.SleepDeep}, // touches the previous deep: merged
		{Start: at(120), End: at(150), Stage: model.SleepREM},
	})
	want := []model.SleepInterval{
		{Stage: "light", Start: at(10), End: at(60)},
		{Stage: "deep", Start: at(60), End: at(120)},
		{Stage: "rem", Start: at(120), End: at(150)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("interval %d = %+v, want %+v", k, got[k], want[k])
		}
	}
}
