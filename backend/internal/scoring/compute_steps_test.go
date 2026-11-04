package scoring

import (
	"math"
	"reflect"
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
	// row is a stored day in loc: its night 23:00–07:00 and its signals (0: none).
	row := func(loc *time.Location, day string, hrv, rhr, resp, strain float64, data bool) store.History {
		bed, wake := wall(t, loc, addDays(day, -1)+" 23:00"), wall(t, loc, day+" 07:00")
		h := store.History{Day: day, HasData: data, SleepStart: &bed, SleepEnd: &wake}
		h.HRV, h.RHR, h.RespRate, h.Strain = &hrv, &rhr, &resp, &strain
		return h
	}
	utc := time.UTC
	tonight := SleepTotals{Start: wall(t, utc, "2026-10-13 23:00"), End: wall(t, utc, "2026-10-14 07:00"), AsleepMin: 480}
	for _, c := range []struct {
		name     string
		loc      *time.Location
		day      string
		hist     []store.History // newest first, as ScoreHistory
		sleep    SleepTotals
		hrvN     int
		hrvMS    float64 // the geometric mean (HRVBaselineMS); 0: none
		rhrMean  float64
		respN    int
		respMean float64
		yStrain  float64
		window   int
		bedtimes int
	}{
		{"no history", utc, "2026-10-14", nil, SleepTotals{}, 0, 0, 0, 0, 0, 0, 0, 0},
		{"this night counts toward consistency only", utc, "2026-10-14", nil, tonight, 0, 0, 0, 0, 0, 0, 0, 1},
		{"yesterday's strain, the week's bedtimes, zero values left out", utc, "2026-10-14", []store.History{
			row(utc, "2026-10-13", 60, 50, 14, 16.5, true),
			row(utc, "2026-10-08", 0, 54, 0, 3, true),   // the 7th night back: still the week
			row(utc, "2026-10-07", 70, 0, 15, 2, false), // the 8th: not the week; no data
		}, tonight, 2, 64.8, 52, 2, 14.5, 16.5, 2, 3},
		// The day after the spring change (Berlin's 2026-03-29 has 23 h): 24 h
		// before the 30th's midnight is the 28th and 6 × 24 h the 23rd, so both
		// labels must come from the calendar, or yesterday's strain is the 28th's
		// (none) and the 23rd's night is an 8th one in the consistency week.
		{"yesterday and the week by the calendar after a 23-hour day", berlin, "2026-03-30", []store.History{
			row(berlin, "2026-03-29", 58, 51, 15, 9.5, true),
			row(berlin, "2026-03-23", 0, 0, 0, 0, true), // the 8th night back
		}, SleepTotals{}, 1, 58, 51, 1, 15, 9.5, 2, 1},
	} {
		d, _ := time.ParseInLocation(dayLayout, c.day, c.loc)
		b := baselines(c.hist, c.sleep, d)
		for _, f := range []struct {
			what      string
			got, want any
		}{
			{"hrv days", b.lnHRVBase.N, c.hrvN}, {"hrv ms", deref(HRVBaselineMS(b.lnHRVBase)), c.hrvMS},
			{"rhr mean", b.rhrBase.Mean, c.rhrMean}, {"resp days", b.respBase.N, c.respN},
			{"resp mean", b.respBase.Mean, c.respMean}, {"yesterday's strain", b.yesterdayStrain, c.yStrain},
			{"window", b.window, c.window}, {"bedtimes", len(b.bedtimes), c.bedtimes},
		} {
			if f.got != f.want {
				t.Errorf("%s: %s %v, want %v", c.name, f.what, f.got, f.want)
			}
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
		// 60 minutes at 150 bpm (RHR 48, max 190); the average is not used.
		// RHR 48, not strainRHR's fallback of 60: the load must use the RHR
		// it is given.
		{"its heart rate", withAvg(100), hr, 13.2, 79.5},
		// 60 minutes at the average (D9), the same load as the curve it
		// stands in for.
		{"fewer than two samples: its average", withAvg(150), hr[:1], 13.2, 79.5},
		{"neither: no load", run, nil, 0, 0},
	} {
		got := workoutLoad(c.w, c.around, 48, 190)
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
		penalty   int
		state     string
	}{
		{"no recovery, no readiness", RecoveryResult{}, &perf, 0, 0, 0, "calibrating"},
		// The engine cases: recovery 50 without a night, 63 with a full one.
		{"without a night, recovery alone", RecoveryResult{Score: 50, Calibrating: true}, nil, 0, 50, 0, "calibrating"},
		{"with a night", RecoveryResult{Score: 63}, &perf, 0, 78, 0, "balanced"},
		// Strain 17.5 after a recovery under 50: half the 10-point penalty.
		{"an overreach costs", RecoveryResult{Score: 40}, &perf, 17.5, 59, 5, "strained"},
	} {
		r, penalty, state := readinessOf(c.rec, c.perf, c.yStrain)
		if deref(r) != c.readiness || (r == nil) != (c.readiness == 0) || penalty != c.penalty || state != c.state {
			t.Errorf("%s: readiness %v penalty %d state %q, want %d %d %q", c.name, r, penalty, state,
				c.readiness, c.penalty, c.state)
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
	hrv, rhr, resp, spo2 := 64.0, 52.0, 14.5, 96.0
	sleep := SleepTotals{Start: wall(t, loc, "2026-10-13 23:00"), End: wall(t, loc, "2026-10-14 07:00"),
		AsleepMin: 480, InBedMin: 480, CoreMin: 480}
	b := baselineSet{lnHRV: []float64{math.Log(64), math.Log(64)}, rhrs: []float64{52}, window: 2, yesterdayStrain: 17.5}
	b.lnHRVBase, b.rhrBase = NewBaseline(b.lnHRV, fallbackSDlnHRV), NewBaseline(b.rhrs, fallbackSDRHR)
	perf := 100
	p := dayScore{day: "2026-10-14", loc: loc,
		night: overnight{sleep: sleep, hrv: &hrv, rhr: &rhr, resp: &resp, spo2: &spo2, napMin: 20},
		base:  b,
		sleep: sleepScores{need: NeedParts{Base: 480, Strain: 10, Nap: 30}, debt: 0, performance: &perf},
		rec:   RecoveryResult{Score: 40},
		strain: strainDay{strength: strengthDay{TRIMP: 40, Minutes: 45, VolumeKG: 6600.04, Sessions: 1},
			workouts: []model.Workout{{ID: uuid.New()}}},
	}
	row := scoreRow(p)
	d := row.DailyScore
	for _, f := range []struct {
		what      string
		got, want any
	}{
		{"sleep need", deref(d.SleepNeedMin), 460}, {"sleep actual", deref(d.SleepActualMin), 480},
		{"nap", deref(d.NapMin), 20},
		// D20: the typed column is the min of both signals, details' copy the HRV nights.
		{"baseline days", d.BaselineDays, 1}, {"baseline hrv days", deref(d.BaselineHRVDays), 2},
		{"details.baseline_days", d.Details["baseline_days"], 2},
		{"hrv", d.HRVms, &hrv}, {"rhr", d.RHR, &rhr}, {"resp", d.RespRate, &resp}, {"spo2", d.SpO2, &spo2},
		{"recovery", deref(d.Recovery), 40}, {"readiness", deref(d.Readiness), 59}, {"state", d.State, "strained"},
		{"details.readiness_penalty", d.Details["readiness_penalty"], 5},
		{"details.yesterday_strain", d.Details["yesterday_strain"], 17.5},
		{"strain", deref(d.Strain), StrainFromTRIMP(40)}, {"strength min", deref(d.StrengthMin), 45},
		{"volume", deref(d.VolumeKG), 6600.0},
		{"details.sleep_start", d.Details["sleep_start"], "2026-10-13T23:00:00+02:00"},
		{"details.workouts", d.Details["workouts"], 1},
		{"sleep row day", row.Sleep.Day, p.day}, {"sleep row total", deref(row.Sleep.TotalMin), 480},
		{"muscular trimp", deref(row.Load.MuscularTRIMP), 40.0},
		{"calibrating reason set", d.CalibratingReason != nil, true},
		{"model version", *d.ModelVersion, ModelVersion},
	} {
		if !reflect.DeepEqual(f.got, f.want) {
			t.Errorf("%s: %v, want %v", f.what, f.got, f.want)
		}
	}
	if empty := scoreRow(dayScore{day: p.day, loc: loc}); empty.Sleep != (store.SleepNight{}) ||
		empty.Recovery != nil || empty.Readiness != nil || empty.Strain != nil || empty.SleepActualMin != nil {
		t.Errorf("a day without data: %+v", empty)
	}
}
