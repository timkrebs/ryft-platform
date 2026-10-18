package scoring

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Engine loads a day's raw data from the store, runs the algorithms and writes
// the daily_scores row. Days must be computed in chronological order because
// each day's baselines and sleep debt come from the rows before it.
type Engine struct {
	st  *store.Store
	log *slog.Logger
}

func NewEngine(st *store.Store, log *slog.Logger) *Engine { return &Engine{st: st, log: log} }

// MaxRangeDays bounds a recompute range (to − from); POST /v1/recompute rejects
// larger ranges up front instead of letting the event fail ten times.
const MaxRangeDays = 400

// RangeDays is the number of calendar days from `from` to `to` (YYYY-MM-DD).
// It counts dates, not hours, so a range across a DST change is not off by
// one hour — the api and scoring must agree exactly on MaxRangeDays.
func RangeDays(from, to string) (int, error) {
	a, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0, fmt.Errorf("from: %w", err)
	}
	b, err := time.Parse("2006-01-02", to)
	if err != nil {
		return 0, fmt.Errorf("to: %w", err)
	}
	return int(b.Sub(a) / (24 * time.Hour)), nil
}

const (
	baselineDays = 30
	debtDays     = 7
	// A "night" for local day D is any sleep ending between D-1 20:00 and D 13:00.
	nightStartOffset = -4 * time.Hour
	nightEndOffset   = 13 * time.Hour
)

// ComputeRange recomputes every day in [from, to], inclusive, oldest first.
func (e *Engine) ComputeRange(ctx context.Context, userID uuid.UUID, from, to string) error {
	n, err := RangeDays(from, to)
	if err != nil {
		return err
	}
	if n > MaxRangeDays {
		return fmt.Errorf("range too large (max %d days)", MaxRangeDays)
	}
	_, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	a, _ := time.ParseInLocation("2006-01-02", from, loc)
	b, _ := time.ParseInLocation("2006-01-02", to, loc)
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		if err := e.ComputeDay(ctx, userID, d.Format("2006-01-02")); err != nil {
			return fmt.Errorf("day %s: %w", d.Format("2006-01-02"), err)
		}
	}
	return nil
}

// ComputeDay scores one local calendar day.
func (e *Engine) ComputeDay(ctx context.Context, userID uuid.UUID, day string) error {
	start := time.Now()
	err := e.computeDay(ctx, userID, day)
	result := metrics.Result(err)
	metrics.DaysScored.WithLabelValues(result).Inc()
	metrics.ScoringDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
	return err
}

func (e *Engine) computeDay(ctx context.Context, userID uuid.UUID, day string) error {
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return fmt.Errorf("bad day %q: %w", day, err)
	}
	dayStart, dayEnd := d, d.AddDate(0, 0, 1)
	nightFrom, nightTo := NightWindow(d)

	// ---- sleep -------------------------------------------------------------
	segs, err := e.st.SleepEndingBetween(ctx, userID, nightFrom, nightTo)
	if err != nil {
		return err
	}
	sleep := SummarizeSleep(segs)

	hrvFrom, hrvTo := nightFrom, nightTo
	if !sleep.Start.IsZero() {
		hrvFrom, hrvTo = sleep.Start.Add(-1*time.Hour), sleep.End.Add(2*time.Hour)
	}

	// ---- overnight physiology ------------------------------------------------
	hrv := e.mean(ctx, userID, model.SampleHRVSDNN, hrvFrom, hrvTo)
	resp := e.mean(ctx, userID, model.SampleRespiratoryRate, hrvFrom, hrvTo)
	spo2 := e.mean(ctx, userID, model.SampleSpO2, hrvFrom, hrvTo)
	rhr := e.last(ctx, userID, model.SampleRestingHR, dayStart, dayEnd)
	if rhr == nil {
		rhr = e.last(ctx, userID, model.SampleRestingHR, dayStart.AddDate(0, 0, -1), dayStart)
	}

	// ---- baselines from history --------------------------------------------
	hist, err := e.st.ScoreHistory(ctx, userID, day, baselineDays)
	if err != nil {
		return err
	}
	var lnHRV, rhrs, resps []float64
	var needs, actuals []int
	var yesterdayStrain float64
	yesterday := dayStart.AddDate(0, 0, -1).Format("2006-01-02")
	for idx, h := range hist {
		if h.HRV != nil && *h.HRV > 0 {
			lnHRV = append(lnHRV, math.Log(*h.HRV))
		}
		if h.RHR != nil && *h.RHR > 0 {
			rhrs = append(rhrs, *h.RHR)
		}
		if h.RespRate != nil && *h.RespRate > 0 {
			resps = append(resps, *h.RespRate)
		}
		if idx < debtDays && h.SleepNeedMin != nil && h.SleepActualMin != nil {
			needs = append(needs, *h.SleepNeedMin)
			actuals = append(actuals, *h.SleepActualMin)
		}
		if h.Day == yesterday && h.Strain != nil {
			yesterdayStrain = *h.Strain
		}
	}
	debt := SleepDebt(needs, actuals)
	need := SleepNeed(user.SleepBaseMin, yesterdayStrain, debt)

	var sleepPerf *int
	if sleep.AsleepMin > 0 {
		p := SleepPerformance(sleep.AsleepMin, need)
		sleepPerf = &p
	}

	lnHRVBase, rhrBase := NewBaseline(lnHRV, fallbackSDlnHRV), NewBaseline(rhrs, fallbackSDRHR)
	rec := Recovery(RecoveryInputs{
		HRVms: hrv, RHR: rhr, RespRate: resp,
		LnHRVBase:        lnHRVBase,
		RHRBase:          rhrBase,
		RespBase:         NewBaseline(resps, fallbackSDResp),
		SleepPerformance: sleepPerf,
	})

	// ---- strain ------------------------------------------------------------------
	strainRHR := 60.0
	if rhr != nil {
		strainRHR = *rhr
	} else if len(rhrs) > 0 {
		strainRHR = rhrBase.Mean
	}
	maxHR := float64(user.MaxHR)

	hrSamples, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, dayStart, dayEnd)
	if err != nil {
		return err
	}
	strain := Strain(hrPoints(hrSamples), strainRHR, maxHR)

	workouts, err := e.st.WorkoutsBetween(ctx, userID, dayStart, dayEnd)
	if err != nil {
		return err
	}
	for _, w := range workouts {
		// Each workout loads its own window rather than filtering the day's
		// samples: a 23:20–00:40 run must count its minutes after midnight too,
		// and match the curve GET /v1/workouts/{id} draws.
		wkSamples, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, w.Start, WorkoutWindowEnd(w))
		if err != nil {
			return err
		}
		var load StrainResult
		inWindow := hrPoints(wkSamples)
		if len(inWindow) >= 2 {
			load = Strain(inWindow, strainRHR, maxHR)
		} else if w.AvgHR != nil {
			load = LoadFromAverage(*w.AvgHR, strainRHR, maxHR, w.End.Sub(w.Start))
		}
		if err := e.st.SetWorkoutLoad(ctx, userID, w.ID, load.Score, load.TRIMP, load.ZoneMinutes); err != nil {
			return err
		}
	}

	// ---- persist -----------------------------------------------------------------
	var recovery *int
	if rec.Score > 0 {
		recovery = &rec.Score
	}
	// Readiness needs a recovery score; without a night of sleep it falls back
	// to recovery alone (the sleep weight renormalises away, as in Recovery).
	var readiness *int
	var readinessPenalty int
	if recovery != nil {
		sp := *recovery
		if sleepPerf != nil {
			sp = *sleepPerf
		}
		r := Readiness(*recovery, sp, yesterdayStrain)
		readiness = &r
		readinessPenalty = ReadinessPenalty(*recovery, yesterdayStrain)
	}
	state := ReadinessState(readiness, rec.Calibrating)

	var strainScore *float64
	var load store.DayLoad
	if strain.Minutes > 0 {
		strainScore = &strain.Score
		trimp, hrMin := strain.TRIMP, int(math.Round(strain.Minutes))
		load = store.DayLoad{TRIMP: &trimp, HRMin: &hrMin, ZoneMinutes: strain.ZoneMinutes[:]}
	}
	var actual *int
	if sleep.AsleepMin > 0 {
		actual = &sleep.AsleepMin
	}
	details := map[string]any{
		"recovery":          rec.Components,
		"trimp":             math.Round(strain.TRIMP),
		"hr_minutes":        math.Round(strain.Minutes),
		"zone_minutes":      strain.ZoneMinutes,
		"sleep_stages":      map[string]int{"deep": sleep.DeepMin, "rem": sleep.REMMin, "core": sleep.CoreMin, "awake": sleep.AwakeMin, "inbed": sleep.InBedMin},
		"sleep_has_stages":  sleep.HasStages,
		"yesterday_strain":  yesterdayStrain,
		"readiness_penalty": readinessPenalty,
		"baseline_days":     len(lnHRV),
		"workouts":          len(workouts),
	}
	if !sleep.Start.IsZero() {
		details["sleep_start"] = sleep.Start.In(loc).Format(time.RFC3339)
		details["sleep_end"] = sleep.End.In(loc).Format(time.RFC3339)
	}

	var night store.SleepNight
	if sleep.AsleepMin > 0 {
		start, end, inBed := sleep.Start.UTC(), sleep.End.UTC(), InBedMinutes(sleep)
		night = store.SleepNight{Day: day, Start: &start, End: &end, TotalMin: &sleep.AsleepMin,
			InBedMin: &inBed, DeepMin: &sleep.DeepMin, LightMin: &sleep.CoreMin, REMMin: &sleep.REMMin,
			AwakeMin: &sleep.AwakeMin, HasStages: sleep.HasStages, Performance: sleepPerf}
	}

	hrvBaseline, rhrBaseline := HRVBaselineMS(lnHRVBase), RHRBaseline(rhrBase)

	row := model.DailyScore{
		Day: day, Readiness: readiness, State: state, Recovery: recovery, Strain: strainScore, SleepPerformance: sleepPerf,
		SleepNeedMin: &need, SleepActualMin: actual, SleepDebtMin: &debt,
		HRVms: hrv, RHR: rhr, RespRate: resp, SpO2: spo2, HRVBaselineMS: hrvBaseline, RHRBaseline: rhrBaseline,
		BaselineDays: min(lnHRVBase.N, rhrBase.N),
		Calibrating:  rec.Calibrating, Details: details,
	}
	if err := e.st.UpsertDailyScore(ctx, userID, store.ScoreRow{DailyScore: row, Sleep: night, Load: load}); err != nil {
		return err
	}
	e.log.Info("scored day", "day", day, "readiness", deref(readiness), "state", state, "recovery", rec.Score, "strain", strain.Score,
		"sleep_min", sleep.AsleepMin, "hrv", deref(hrv), "rhr", deref(rhr), "calibrating", rec.Calibrating)
	return nil
}

func hrPoints(samples []model.Sample) []HRPoint {
	pts := make([]HRPoint, len(samples))
	for k, s := range samples {
		pts[k] = HRPoint{T: s.Start, BPM: s.Value}
	}
	return pts
}

func (e *Engine) mean(ctx context.Context, userID uuid.UUID, typ string, from, to time.Time) *float64 {
	xs, err := e.st.SamplesBetween(ctx, userID, typ, from, to)
	if err != nil || len(xs) == 0 {
		return nil
	}
	var sum float64
	for _, x := range xs {
		sum += x.Value
	}
	m := math.Round(sum/float64(len(xs))*10) / 10
	return &m
}

func (e *Engine) last(ctx context.Context, userID uuid.UUID, typ string, from, to time.Time) *float64 {
	xs, err := e.st.SamplesBetween(ctx, userID, typ, from, to)
	if err != nil || len(xs) == 0 {
		return nil
	}
	v := math.Round(xs[len(xs)-1].Value*10) / 10
	return &v
}

func deref[T int | float64](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
