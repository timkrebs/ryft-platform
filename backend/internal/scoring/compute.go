package scoring

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

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

const (
	baselineDays = 30
	debtDays     = 7
	// A "night" for local day D is any sleep ending between D-1 20:00 and D 13:00.
	nightStartOffset = -4 * time.Hour
	nightEndOffset   = 13 * time.Hour
)

// ComputeRange recomputes every day in [from, to], inclusive, oldest first.
func (e *Engine) ComputeRange(ctx context.Context, userID uuid.UUID, from, to string) error {
	_, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	a, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	b, err := time.ParseInLocation("2006-01-02", to, loc)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}
	if b.Sub(a) > 400*24*time.Hour {
		return fmt.Errorf("range too large (max 400 days)")
	}
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		if err := e.ComputeDay(ctx, userID, d.Format("2006-01-02")); err != nil {
			return fmt.Errorf("day %s: %w", d.Format("2006-01-02"), err)
		}
	}
	return nil
}

// ComputeDay scores one local calendar day.
func (e *Engine) ComputeDay(ctx context.Context, userID uuid.UUID, day string) error {
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
	points := make([]HRPoint, len(hrSamples))
	for k, s := range hrSamples {
		points[k] = HRPoint{T: s.Start, BPM: s.Value}
	}
	strain := Strain(points, strainRHR, maxHR)

	workouts, err := e.st.WorkoutsBetween(ctx, userID, dayStart, dayEnd)
	if err != nil {
		return err
	}
	for _, w := range workouts {
		var load StrainResult
		var inWindow []HRPoint
		for _, p := range points {
			if !p.T.Before(w.Start) && !p.T.After(w.End) {
				inWindow = append(inWindow, p)
			}
		}
		if len(inWindow) >= 2 {
			load = Strain(inWindow, strainRHR, maxHR)
		} else if w.AvgHR != nil {
			load = LoadFromAverage(*w.AvgHR, strainRHR, maxHR, w.End.Sub(w.Start))
		}
		if err := e.st.SetWorkoutLoad(ctx, w.ID, load.Score, load.TRIMP, load.ZoneMinutes); err != nil {
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
		Calibrating: rec.Calibrating, Details: details,
	}
	if err := e.st.UpsertDailyScore(ctx, userID, store.ScoreRow{DailyScore: row, Sleep: night, Load: load}); err != nil {
		return err
	}
	e.log.Info("scored day", "day", day, "readiness", deref(readiness), "state", state, "recovery", rec.Score, "strain", strain.Score,
		"sleep_min", sleep.AsleepMin, "hrv", deref(hrv), "rhr", deref(rhr), "calibrating", rec.Calibrating)
	return nil
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
