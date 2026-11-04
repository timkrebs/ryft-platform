package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// computeDay's pure steps, one table each. The values are the ones computeDay
// stored before it was split (compute_steps_engine_test.go pins those through
// the database); where a case repeats one of those, it says so.

func wall(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestLocalDay(t *testing.T) {
	loc := berlin
	for _, c := range []struct {
		day  string
		loc  *time.Location
		want time.Duration
	}{
		{"2026-10-14", time.UTC, 24 * time.Hour},
		{"2026-10-25", loc, 25 * time.Hour}, // back to CET
		{"2026-03-29", loc, 23 * time.Hour}, // on to CEST
	} {
		d, _ := time.ParseInLocation(dayLayout, c.day, c.loc)
		start, end := localDay(d)
		if !start.Equal(d) || end.Sub(start) != c.want || end.In(c.loc).Format("15:04") != "00:00" {
			t.Errorf("%s %s: [%v, %v), want %v to the next midnight", c.day, c.loc, start, end, c.want)
		}
	}
}

func TestSplitSleepAndOvernightWindow(t *testing.T) {
	u := func(s string) time.Time { return wall(t, time.UTC, s) }
	seg := func(from, to string) model.SleepSegment {
		return model.SleepSegment{Start: u(from), End: u(to), Stage: model.SleepCore, Source: "watch"}
	}
	d := u("2026-10-14 00:00")
	for _, c := range []struct {
		name                 string
		segs                 []model.SleepSegment
		asleep, nap, napYday int
		from, to             string // overnightWindow
	}{
		{"no sleep: the night window", nil, 0, 0, 0, "2026-10-13 20:00", "2026-10-14 13:00"},
		{"the night, an hour before to two after", []model.SleepSegment{seg("2026-10-13 23:00", "2026-10-14 07:00")},
			480, 0, 0, "2026-10-13 22:00", "2026-10-14 09:00"},
		{"yesterday's nap is credited, today's is the day's", []model.SleepSegment{
			seg("2026-10-13 14:00", "2026-10-13 15:00"), seg("2026-10-13 23:00", "2026-10-14 07:00"),
			seg("2026-10-14 15:00", "2026-10-14 15:30")}, 480, 30, 60, "2026-10-13 22:00", "2026-10-14 09:00"},
	} {
		n := splitSleep(c.segs, d)
		from, to := overnightWindow(n.sleep, d)
		if n.sleep.AsleepMin != c.asleep || n.napMin != c.nap || n.napYesterday != c.napYday ||
			!from.Equal(u(c.from)) || !to.Equal(u(c.to)) {
			t.Errorf("%s: asleep %d nap %d yesterday %d window %v–%v", c.name, n.sleep.AsleepMin, n.napMin,
				n.napYesterday, from, to)
		}
	}
}

func TestBaselines(t *testing.T) {
	u := func(s string) time.Time { return wall(t, time.UTC, s) }
	d := u("2026-10-14 00:00")
	night := func(day string) (*time.Time, *time.Time) {
		bed, wake := u(addDays(day, -1)+" 23:00"), u(day+" 07:00")
		return &bed, &wake
	}
	row := func(day string, hrv, rhr, resp, strain float64, data bool) store.History {
		h := store.History{Day: day, HasData: data}
		h.SleepStart, h.SleepEnd = night(day)
		h.HRV, h.RHR, h.RespRate, h.Strain = &hrv, &rhr, &resp, &strain
		return h
	}
	tonight := SleepTotals{Start: u("2026-10-13 23:00"), End: u("2026-10-14 07:00"), AsleepMin: 480}
	for _, c := range []struct {
		name     string
		hist     []store.History // newest first, as ScoreHistory
		sleep    SleepTotals
		hrvN     int
		rhrMean  float64
		yStrain  float64
		window   int
		bedtimes int
	}{
		{"no history", nil, SleepTotals{}, 0, 0, 0, 0, 0},
		{"this night counts toward consistency only", nil, tonight, 0, 0, 0, 0, 1},
		{"yesterday's strain, the week's bedtimes, zero values left out", []store.History{
			row("2026-10-13", 60, 50, 14, 16.5, true),
			row("2026-10-08", 0, 54, 0, 3, true),   // the 7th night back: still the week
			row("2026-10-07", 70, 0, 15, 2, false), // the 8th: not the week; no data
		}, tonight, 2, 52, 16.5, 2, 3},
	} {
		b := baselines(c.hist, c.sleep, d)
		if b.lnHRVBase.N != c.hrvN || b.rhrBase.Mean != c.rhrMean || b.yesterdayStrain != c.yStrain ||
			b.window != c.window || len(b.bedtimes) != c.bedtimes {
			t.Errorf("%s: hrv n %d rhr mean %v yesterday %v window %d bedtimes %d", c.name, b.lnHRVBase.N,
				b.rhrBase.Mean, b.yesterdayStrain, b.window, len(b.bedtimes))
		}
		if c.sleep.AsleepMin > 0 && !b.bedtimes[0].Bed.Equal(c.sleep.Start) {
			t.Errorf("%s: this night is not first: %v", c.name, b.bedtimes)
		}
	}
}

func TestSleepNeedStep(t *testing.T) {
	night := overnight{sleep: SleepTotals{AsleepMin: 480, InBedMin: 480, CoreMin: 480}, napYesterday: 60}
	for _, c := range []struct {
		name               string
		yStrain            float64
		n                  overnight
		strainMin, nap     int
		performance, total int // performance -1: none
	}{
		{"no night: a need, no performance", 0, overnight{}, 0, 0, -1, 480},
		// 16.5 is the strain the engine case stores for 60 min at 165 bpm.
		{"yesterday's strain and nap", 16.5, night, 50, 60, 100, 470},
	} {
		s := sleepNeed(480, nil, baselineSet{yesterdayStrain: c.yStrain}, c.n, time.UTC)
		perf := -1
		if s.performance != nil {
			perf = *s.performance
		}
		if s.need.Strain != c.strainMin || s.need.Nap != c.nap || s.need.Total() != c.total || perf != c.performance ||
			(s.performance == nil) != (s.quality == nil) {
			t.Errorf("%s: need %+v total %d performance %d quality %v", c.name, s.need, s.need.Total(), perf, s.quality)
		}
	}
}

func TestStrainRHR(t *testing.T) {
	rhr := 48.0
	withBase := baselineSet{rhrs: []float64{45, 45}, rhrBase: NewBaseline([]float64{45, 45}, fallbackSDRHR)}
	for _, c := range []struct {
		name string
		rhr  *float64
		b    baselineSet
		want float64
	}{
		{"the day's", &rhr, withBase, 48},
		{"else the baseline's mean", nil, withBase, 45},
		{"else 60", nil, baselineSet{}, 60},
	} {
		if got := strainRHR(c.rhr, c.b); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWorkoutLoad(t *testing.T) {
	start := wall(t, time.UTC, "2026-10-14 18:00")
	run := model.Workout{ID: uuid.New(), Activity: "running", Start: start, End: start.Add(time.Hour), Source: "watch"}
	withAvg := func(avg float64) model.Workout { w := run; w.AvgHR = &avg; return w }
	hr := hrEvery("watch", start, start.Add(time.Hour), time.Minute, 150) // 18:00..19:00, both ends
	for _, c := range []struct {
		name          string
		w             model.Workout
		around        []model.Sample
		strain, trimp float64
	}{
		// 60 minutes at 150 bpm (RHR 60, max 190); the average is not used.
		{"its heart rate", withAvg(100), hr, 12.3, 70.3},
		// The engine case's workout (D9): 60 minutes at the average, the same
		// load as the curve it stands in for.
		{"fewer than two samples: its average", withAvg(150), hr[:1], 12.3, 70.3},
		{"neither: no load", run, nil, 0, 0},
	} {
		got := workoutLoad(c.w, c.around, 60, 190)
		if got.Score != c.strain || math.Round(got.TRIMP*10)/10 != c.trimp {
			t.Errorf("%s: strain %v trimp %v, want %v %v", c.name, got.Score, got.TRIMP, c.strain, c.trimp)
		}
	}
}

func TestReadinessOf(t *testing.T) {
	perf := 100
	for _, c := range []struct {
		name      string
		rec       RecoveryResult
		perf      *int
		yStrain   float64
		readiness int // 0: none
		state     string
	}{
		{"no recovery, no readiness", RecoveryResult{}, &perf, 0, 0, ReadinessState(nil, false)},
		// The engine cases: recovery 50 without a night, 63 with a full one.
		{"without a night, recovery alone", RecoveryResult{Score: 50, Calibrating: true}, nil, 0, 50, model.StateCalibrating},
		{"with a night", RecoveryResult{Score: 63}, &perf, 0, 78, ReadinessState(ptrTo(78), false)},
	} {
		r, _, state := readinessOf(c.rec, c.perf, c.yStrain)
		if deref(r) != c.readiness || (r == nil) != (c.readiness == 0) || state != c.state {
			t.Errorf("%s: readiness %v state %q, want %d %q", c.name, r, state, c.readiness, c.state)
		}
	}
}

func TestStrainScore(t *testing.T) {
	hr := StrainResult{TRIMP: 69, Score: StrainFromTRIMP(69), Minutes: 59}
	for _, c := range []struct {
		name     string
		hr       StrainResult
		strength strengthDay
		want     float64 // -1: none
		hrMin    int     // -1: none
	}{
		{"neither", StrainResult{}, strengthDay{}, -1, -1},
		{"heart rate", hr, strengthDay{}, 12.1, 59},
		{"a logged session only", StrainResult{}, strengthDay{TRIMP: 40}, StrainFromTRIMP(40), -1},
		{"both, added before the compression", hr, strengthDay{TRIMP: 40}, StrainFromTRIMP(109), 59},
	} {
		s, load := strainScore(c.hr, c.strength)
		got, hrMin := -1.0, -1
		if s != nil {
			got = *s
		}
		if load.HRMin != nil {
			hrMin = *load.HRMin
		}
		if got != c.want || hrMin != c.hrMin || (load.MuscularTRIMP != nil) != (c.strength.TRIMP > 0) {
			t.Errorf("%s: strain %v hr min %d load %+v", c.name, got, hrMin, load)
		}
	}
}

func TestScoreRow(t *testing.T) {
	loc := berlin
	hrv, rhr := 64.0, 52.0
	sleep := SleepTotals{Start: wall(t, loc, "2026-10-13 23:00"), End: wall(t, loc, "2026-10-14 07:00"),
		AsleepMin: 480, InBedMin: 480, CoreMin: 480}
	b := baselineSet{lnHRV: []float64{math.Log(64), math.Log(64)}, rhrs: []float64{52}, window: 2}
	b.lnHRVBase, b.rhrBase = NewBaseline(b.lnHRV, fallbackSDlnHRV), NewBaseline(b.rhrs, fallbackSDRHR)
	perf := 100
	p := dayScore{day: "2026-10-14", loc: loc,
		night: overnight{sleep: sleep, hrv: &hrv, rhr: &rhr, napMin: 20},
		base:  b,
		sleep: sleepScores{need: NeedParts{Base: 480, Strain: 10, Nap: 30}, debt: 0, performance: &perf},
		rec:   RecoveryResult{Score: 63},
		strain: strainDay{strength: strengthDay{TRIMP: 40, Minutes: 45, VolumeKG: 6600.04, Sessions: 1},
			workouts: []model.Workout{{ID: uuid.New()}}},
	}
	row := scoreRow(p)
	d := row.DailyScore
	switch {
	case deref(d.SleepNeedMin) != 460, deref(d.SleepActualMin) != 480, deref(d.NapMin) != 20:
		t.Errorf("sleep: need %v actual %v nap %v", d.SleepNeedMin, d.SleepActualMin, d.NapMin)
	case d.BaselineDays != 1 || deref(d.BaselineHRVDays) != 2 || d.Details["baseline_days"] != 2:
		t.Errorf("baseline days %d hrv %v details %v (D20: the typed column is the min, details HRV only)",
			d.BaselineDays, d.BaselineHRVDays, d.Details["baseline_days"])
	case d.Recovery == nil || *d.Recovery != 63 || deref(d.Readiness) != 78:
		t.Errorf("recovery %v readiness %v", d.Recovery, d.Readiness)
	case deref(d.Strain) != StrainFromTRIMP(40) || deref(d.StrengthMin) != 45 || deref(d.VolumeKG) != 6600:
		t.Errorf("strain %v strength %v volume %v", d.Strain, d.StrengthMin, d.VolumeKG)
	case d.Details["sleep_start"] != "2026-10-13T23:00:00+02:00" || d.Details["workouts"] != 1:
		t.Errorf("details %v", d.Details)
	case row.Sleep.Day != p.day || deref(row.Sleep.TotalMin) != 480 || row.Load.MuscularTRIMP == nil:
		t.Errorf("sleep %+v load %+v", row.Sleep, row.Load)
	case d.CalibratingReason == nil || d.ModelVersion == nil || *d.ModelVersion != ModelVersion:
		t.Errorf("reason %v version %v", d.CalibratingReason, d.ModelVersion)
	}
	if empty := scoreRow(dayScore{day: p.day, loc: loc}); empty.Sleep != (store.SleepNight{}) ||
		empty.Recovery != nil || empty.Readiness != nil || empty.Strain != nil || empty.SleepActualMin != nil {
		t.Errorf("a day without data: %+v", empty)
	}
}
