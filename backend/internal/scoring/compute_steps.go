package scoring

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The steps of computeDay, in its order. The Engine methods among them read
// and write the store; the functions take what those loaded and return values
// (compute_steps_test.go pins each, compute_steps_engine_test.go the stored
// result).

// dayScore is a day as its steps leave it, for the row (scoreRow).
type dayScore struct {
	day         string
	loc         *time.Location
	night       overnight
	base        baselineSet
	sleep       sleepScores
	rec         RecoveryResult
	strain      strainDay
	steps, kcal *int
}

// admitted loads the user and refuses one without consent (ErrNoConsent,
// D78). In warn mode the day is scored and the refusal it would have been is
// counted (D87).
func (e *Engine) admitted(ctx context.Context, userID uuid.UUID) (model.User, *time.Location, error) {
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return user, nil, err
	}
	if !e.Admits(user) {
		return user, nil, ErrNoConsent
	}
	if user.Consent == nil && e.consent == config.ModeWarn {
		metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(config.ModeWarn), "scoring").Inc()
	}
	return user, loc, nil
}

// localDay is the local calendar day that starts at d (midnight in the user's
// zone): up to the next midnight, not 24 h on, so a DST day has its 23 or 25
// hours (calendar days, as RangeDays, D22).
func localDay(d time.Time) (start, end time.Time) {
	return d, d.AddDate(0, 0, 1)
}

// overnight is what loadNight read for a day: its night from one source
// (SplitDay), the nap credits of the day and the day before, and the
// overnight signals (not de-duplicated: SCORING.md → What is left alone).
type overnight struct {
	sleep                SleepTotals
	napMin, napYesterday int
	hrv, resp, spo2, rhr *float64
}

// loadNight reads the sleep ending in yesterday's and today's sleep windows,
// the HRV, respiratory rate and SpO2 around the night (overnightWindow), and
// the day's last resting HR.
func (e *Engine) loadNight(ctx context.Context, userID uuid.UUID, d time.Time) (overnight, error) {
	// Yesterday's naps lower last night's need (D66), so both days' sleep.
	from, _ := DaySleepWindow(d.AddDate(0, 0, -1))
	_, to := DaySleepWindow(d)
	segs, err := e.st.SleepEndingBetween(ctx, userID, from, to)
	if err != nil {
		return overnight{}, err
	}
	n := splitSleep(segs, d)
	from, to = overnightWindow(n.sleep, d)
	n.hrv = e.mean(ctx, userID, model.SampleHRVSDNN, from, to)
	n.resp = e.mean(ctx, userID, model.SampleRespiratoryRate, from, to)
	n.spo2 = e.mean(ctx, userID, model.SampleSpO2, from, to)
	dayStart, dayEnd := localDay(d)
	n.rhr = e.last(ctx, userID, model.SampleRestingHR, dayStart, dayEnd)
	if n.rhr == nil {
		// A watch writes the resting HR later in the day (SCORING.md →
		// Calibrating and its reason), so a morning score takes yesterday's
		// last. Yesterday by the calendar, which is 23 or 25 h around DST.
		n.rhr = e.last(ctx, userID, model.SampleRestingHR, dayStart.AddDate(0, 0, -1), dayStart)
	}
	return n, nil
}

// splitSleep is the night of the day that starts at d and its nap credit, and
// yesterday's nap credit, from both days' segments (SplitDay).
func splitSleep(segs []model.SleepSegment, d time.Time) overnight {
	nightSegs, napMin := SplitDay(segs, d)
	_, napYesterday := SplitDay(segs, d.AddDate(0, 0, -1))
	return overnight{sleep: SummarizeSleep(nightSegs), napMin: napMin, napYesterday: napYesterday}
}

// overnightWindow is where the overnight signals are averaged: the night as
// slept, from an hour before it to two hours after, not the whole night window
// (20:00 to 13:00), which also holds the evening before a late night. Without
// a sleep, the night window.
func overnightWindow(sleep SleepTotals, d time.Time) (from, to time.Time) {
	if sleep.Start.IsZero() {
		return NightWindow(d)
	}
	return sleep.Start.Add(-1 * time.Hour), sleep.End.Add(2 * time.Hour)
}

// baselineSet is what the stored days before a day contribute.
type baselineSet struct {
	lnHRV, rhrs                  []float64 // the values behind the baselines
	lnHRVBase, rhrBase, respBase Baseline
	yesterdayStrain              float64
	window                       int       // the days with any data: calibrating_reason's W
	bedtimes                     []Bedtime // the consistency week, this night first
}

// baselines draws the personal baselines from hist (ScoreHistory: the stored
// days before the day that starts at d, newest first), with yesterday's strain
// and the bedtimes of the last 7 nights; sleep is the day's own night.
func baselines(hist []store.History, sleep SleepTotals, d time.Time) baselineSet {
	var b baselineSet
	var resps []float64
	yesterday := d.AddDate(0, 0, -1).Format(dayLayout)
	// Consistency: the last 7 nights, this one included.
	firstNight := d.AddDate(0, 0, -(consistencyDays - 1)).Format(dayLayout)
	if sleep.AsleepMin > 0 {
		b.bedtimes = append(b.bedtimes, Bedtime{Bed: sleep.Start, Wake: sleep.End})
	}
	for _, h := range hist {
		if h.Day >= firstNight && h.SleepStart != nil && h.SleepEnd != nil {
			b.bedtimes = append(b.bedtimes, Bedtime{Bed: *h.SleepStart, Wake: *h.SleepEnd})
		}
		if h.HRV != nil && *h.HRV > 0 {
			b.lnHRV = append(b.lnHRV, math.Log(*h.HRV))
		}
		if h.RHR != nil && *h.RHR > 0 {
			b.rhrs = append(b.rhrs, *h.RHR)
		}
		if h.RespRate != nil && *h.RespRate > 0 {
			resps = append(resps, *h.RespRate)
		}
		if h.Day == yesterday && h.Strain != nil {
			b.yesterdayStrain = *h.Strain
		}
		if h.HasData {
			b.window++
		}
	}
	b.lnHRVBase, b.rhrBase = NewBaseline(b.lnHRV, fallbackSDlnHRV), NewBaseline(b.rhrs, fallbackSDRHR)
	b.respBase = NewBaseline(resps, fallbackSDResp)
	return b
}

// sleepScores is the night's need, and how the night measured up to it.
type sleepScores struct {
	need                              NeedParts
	debt                              int
	performance, quality, consistency *int
}

// sleepNeed is the night's need term by term (SCORING.md → Sleep) and, for a
// night with sleep, its performance against that need and its quality; the
// consistency needs 5 of the last 7 nights (SleepConsistency).
func sleepNeed(baseMin int, hist []store.History, b baselineSet, n overnight, loc *time.Location) sleepScores {
	s := sleepScores{debt: SleepDebtFrom(hist)}
	s.need = SleepNeedParts(baseMin, b.yesterdayStrain, s.debt, n.napYesterday)
	if n.sleep.AsleepMin > 0 {
		p := SleepPerformance(n.sleep.AsleepMin, s.need.Total())
		q, _ := NightQuality(n.sleep.AsleepMin, InBedMinutes(n.sleep), n.sleep.DeepMin, n.sleep.REMMin, n.sleep.HasStages, p)
		s.performance, s.quality = &p, &q
	}
	if c, ok := SleepConsistency(b.bedtimes, loc); ok {
		s.consistency = &c
	}
	return s
}

// recoveryOf scores the night's signals against their baselines (Recovery).
func recoveryOf(n overnight, b baselineSet, sleepPerf *int) RecoveryResult {
	return Recovery(RecoveryInputs{
		HRVms: n.hrv, RHR: n.rhr, RespRate: n.resp,
		LnHRVBase:        b.lnHRVBase,
		RHRBase:          b.rhrBase,
		RespBase:         b.respBase,
		SleepPerformance: sleepPerf,
	})
}

// strainRHR is the resting HR the day's heart-rate reserve starts from: the
// day's (loadNight's, so possibly yesterday's), else the baseline's mean, else
// 60, so a day without any resting HR still has a strain.
func strainRHR(rhr *float64, b baselineSet) float64 {
	switch {
	case rhr != nil:
		return *rhr
	case len(b.rhrs) > 0:
		return b.rhrBase.Mean
	}
	return 60.0
}

// strainDay is the day's load: its heart rate, its strength sessions, and its
// workouts with the cross-source duplicates among them.
type strainDay struct {
	hr       StrainResult
	dups     map[uuid.UUID]uuid.UUID // the day's hidden duplicate → primary
	strength strengthDay
	workouts []model.Workout // hidden duplicates included
}

// strain scores the day's heart rate, decides its cross-source duplicates,
// scores its strength sessions and stores every workout's load, in that order.
func (e *Engine) strain(ctx context.Context, userID uuid.UUID, day string, d time.Time, rhr, maxHR float64) (strainDay, error) {
	var out strainDay
	dayStart, dayEnd := localDay(d)
	hrSamples, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, dayStart, dayEnd)
	if err != nil {
		return out, err
	}
	out.hr = Strain(hrPoints(DedupSources(hrSamples)), rhr, maxHR)

	// Cross-source duplicates before the sessions: a session attaches to a
	// workout the user sees, so the hiding must be decided first (D147, D60;
	// TestEngineAttachesSessionsToTheVisibleWorkout and the golden files fail
	// the other order). Day strain above is unaffected (de-duplicated heart rate).
	if out.dups, err = e.workoutDuplicates(ctx, userID, day, dayStart, dayEnd); err != nil {
		return out, fmt.Errorf("workout duplicates: %w", err)
	}
	// Strength sessions next: they may create the workouts the loop below scores.
	if out.strength, err = e.strength(ctx, userID, dayStart, dayEnd); err != nil {
		return out, fmt.Errorf("strength: %w", err)
	}
	// Every workout of the day, hidden duplicates included: each keeps a load
	// from its own window (GET /v1/workouts/{id} still answers for a hidden one).
	if out.workouts, err = e.st.WorkoutsBetween(ctx, userID, dayStart, dayEnd); err != nil {
		return out, err
	}
	return out, e.workoutLoads(ctx, userID, out.workouts, rhr, maxHR)
}

// workoutLoads stores each workout's load (workoutLoad) with its strain.
func (e *Engine) workoutLoads(ctx context.Context, userID uuid.UUID, workouts []model.Workout, rhr, maxHR float64) error {
	for _, w := range workouts {
		// Each workout loads its own window rather than filtering the day's
		// samples (D22): a 23:20–00:40 run must count its minutes after midnight too,
		// and match the curve GET /v1/workouts/{id} draws (same whole-hour
		// window, same source per hour).
		from, to := SampleWindow(w.Start, WorkoutWindowEnd(w))
		around, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, from, to)
		if err != nil {
			return err
		}
		load := workoutLoad(w, around, rhr, maxHR)
		if err := e.st.SetWorkoutLoad(ctx, userID, w.ID, strainOf(load, w), load.TRIMP, load.ZoneMinutes, ModelVersion); err != nil {
			return err
		}
	}
	return nil
}

// workoutLoad is a workout's load from the heart-rate samples around it (cut
// to its window, WorkoutSamples). With fewer than two inside it there is no
// curve to integrate, so the load comes from the workout's average heart rate
// over its whole duration (D9); without either it is zero.
func workoutLoad(w model.Workout, around []model.Sample, rhr, maxHR float64) StrainResult {
	inWindow := hrPoints(WorkoutSamples(w, around))
	switch {
	case len(inWindow) >= 2:
		return Strain(inWindow, rhr, maxHR)
	case w.AvgHR != nil:
		return LoadFromAverage(*w.AvgHR, rhr, maxHR, w.End.Sub(w.Start))
	}
	return StrainResult{}
}

// activity is the day's steps and active energy (dayTotal).
func (e *Engine) activity(ctx context.Context, userID uuid.UUID, d time.Time) (steps, kcal *int, err error) {
	dayStart, dayEnd := localDay(d)
	if steps, err = e.dayTotal(ctx, userID, model.SampleSteps, dayStart, dayEnd); err != nil {
		return nil, nil, err
	}
	kcal, err = e.dayTotal(ctx, userID, model.SampleActiveEnergy, dayStart, dayEnd)
	return steps, kcal, err
}

// readinessOf is the Today headline, its penalty for yesterday's strain and
// the state it shows. It needs a recovery score; without a night of sleep it
// falls back to recovery alone (the sleep weight renormalises away, as in
// Recovery).
func readinessOf(rec RecoveryResult, sleepPerf *int, yesterdayStrain float64) (readiness *int, penalty int, state string) {
	if rec.Score > 0 {
		sp := rec.Score
		if sleepPerf != nil {
			sp = *sleepPerf
		}
		r := Readiness(rec.Score, sp, yesterdayStrain)
		readiness, penalty = &r, ReadinessPenalty(rec.Score, yesterdayStrain)
	}
	return readiness, penalty, ReadinessState(readiness, rec.Calibrating)
}

// strainScore is the day's strain and the load stored with it; nil without
// heart-rate minutes or a strength session.
func strainScore(hr StrainResult, strength strengthDay) (*float64, store.DayLoad) {
	var score *float64
	var load store.DayLoad
	if hr.Minutes > 0 {
		score = &hr.Score
		trimp, hrMin := hr.TRIMP, int(math.Round(hr.Minutes))
		load = store.DayLoad{TRIMP: &trimp, HRMin: &hrMin, ZoneMinutes: hr.ZoneMinutes[:]}
	}
	// Muscular load joins the heart-rate TRIMP before the compression, so a day
	// with only a logged session has a strain too (SCORING.md → Strength).
	if strength.TRIMP > 0 {
		s, m := StrainFromTRIMP(hr.TRIMP+strength.TRIMP), strength.TRIMP
		score, load.MuscularTRIMP = &s, &m
	}
	return score, load
}

// dayDetails is the row's details. The keys the app reads are frozen (D20);
// its baseline_days counts the HRV nights only, the typed column both signals.
func dayDetails(p dayScore, penalty int) map[string]any {
	sleep, hr, strength := p.night.sleep, p.strain.hr, p.strain.strength
	details := map[string]any{
		"recovery":          p.rec.Components,
		"trimp":             math.Round(hr.TRIMP),
		"hr_minutes":        math.Round(hr.Minutes),
		"zone_minutes":      hr.ZoneMinutes,
		"sleep_stages":      map[string]int{"deep": sleep.DeepMin, "rem": sleep.REMMin, "core": sleep.CoreMin, "awake": sleep.AwakeMin, "inbed": sleep.InBedMin},
		"sleep_has_stages":  sleep.HasStages,
		"yesterday_strain":  p.base.yesterdayStrain,
		"readiness_penalty": penalty,
		"baseline_days":     len(p.base.lnHRV),
		"workouts":          visibleCount(p.strain.workouts, p.strain.dups),
		"muscular_trimp":    math.Round(strength.TRIMP),
		"strength_week":     strength.Week,
	}
	if !sleep.Start.IsZero() {
		details["sleep_start"] = sleep.Start.In(p.loc).Format(time.RFC3339)
		details["sleep_end"] = sleep.End.In(p.loc).Format(time.RFC3339)
	}
	return details
}

// sleepNight is the night as the sleep columns store it; zero without sleep.
func sleepNight(day string, sleep SleepTotals, perf *int) store.SleepNight {
	if sleep.AsleepMin <= 0 {
		return store.SleepNight{}
	}
	start, end, inBed := sleep.Start.UTC(), sleep.End.UTC(), InBedMinutes(sleep)
	return store.SleepNight{Day: day, Start: &start, End: &end, TotalMin: &sleep.AsleepMin,
		InBedMin: &inBed, DeepMin: &sleep.DeepMin, LightMin: &sleep.CoreMin, REMMin: &sleep.REMMin,
		AwakeMin: &sleep.AwakeMin, HasStages: sleep.HasStages, Performance: perf}
}

// calibration is the counts behind baseline_days, and why the day calibrates.
// The window is the stored rows the baselines were drawn from that hold any
// data, not the nights with a value: a phone-only month is 30 days of steps
// without an HRV; a new account has none, even after a range stored empty days
// for it. The day's own signals count too: the first night of a new source is
// not "no HRV".
func calibration(n overnight, b baselineSet) (hrvDays, rhrDays int, reason *string) {
	hrvDays, rhrDays = b.lnHRVBase.N, b.rhrBase.N
	if r := CalibratingReason(Calibration{Window: b.window, HRVDays: hrvDays, RHRDays: rhrDays,
		DayHRV: n.hrv != nil && *n.hrv > 0, DayRHR: n.rhr != nil && *n.rhr > 0}); r != "" {
		reason = &r
	}
	return hrvDays, rhrDays, reason
}

// scoreRow is everything UpsertDailyScore stores for the day.
func scoreRow(p dayScore) store.ScoreRow {
	sleep, s := p.night.sleep, p.sleep
	var recovery *int
	if p.rec.Score > 0 {
		recovery = &p.rec.Score
	}
	readiness, penalty, state := readinessOf(p.rec, s.performance, p.base.yesterdayStrain)
	strain, load := strainScore(p.strain.hr, p.strain.strength)
	var actual *int
	if sleep.AsleepMin > 0 {
		actual = &sleep.AsleepMin
	}
	need := s.need.Total()
	hrvDays, rhrDays, reason := calibration(p.night, p.base)
	version := ModelVersion
	row := model.DailyScore{
		Day: p.day, Readiness: readiness, State: state, Recovery: recovery, Strain: strain, Steps: p.steps, ActiveKcal: p.kcal,
		SleepPerformance: s.performance,
		SleepNeedMin:     &need, SleepActualMin: actual, SleepDebtMin: &s.debt,
		HRVms: p.night.hrv, RHR: p.night.rhr, RespRate: p.night.resp, SpO2: p.night.spo2,
		HRVBaselineMS: HRVBaselineMS(p.base.lnHRVBase), RHRBaseline: RHRBaseline(p.base.rhrBase),
		BaselineDays: min(hrvDays, rhrDays), BaselineHRVDays: &hrvDays, BaselineRHRDays: &rhrDays,
		Calibrating: p.rec.Calibrating, CalibratingReason: reason, Details: dayDetails(p, penalty),
		SleepNeedBaseMin: &s.need.Base, SleepNeedStrainMin: &s.need.Strain, SleepNeedDebtMin: &s.need.Debt,
		SleepNeedNapMin: &s.need.Nap, NapMin: &p.night.napMin, SleepQuality: s.quality, SleepConsistency: s.consistency,
		ModelVersion: &version,
	}
	if st := p.strain.strength; st.Sessions > 0 {
		minutes, volume := st.Minutes, math.Round(st.VolumeKG*10)/10
		row.StrengthMin, row.VolumeKG = &minutes, &volume
	}
	return store.ScoreRow{DailyScore: row, Sleep: sleepNight(p.day, sleep, s.performance), Load: load}
}

// persist stores the day's row and reports whether its history inputs changed
// (store.ScoreWrite.InputsChanged).
func (e *Engine) persist(ctx context.Context, userID uuid.UUID, p dayScore) (bool, error) {
	row := scoreRow(p)
	written, err := e.st.UpsertDailyScore(ctx, userID, row)
	if err != nil {
		return false, err
	}
	// The morning push (OnScored): the first recovery of the user's local
	// today after a night with sleep; a re-score or a past day announces nothing.
	asleep := p.night.sleep.AsleepMin
	if written.FirstRecovery && asleep > 0 && e.onScored != nil && p.day == time.Now().In(p.loc).Format(dayLayout) {
		e.onScored(ctx, model.ScoredEvent{UserID: userID, Day: p.day, Recovery: *row.Recovery, Readiness: row.Readiness,
			State: row.State, SleepPerformance: row.SleepPerformance, SleepMin: asleep})
	}
	// Which day, by which model, and whether it moves the days after it: no
	// score, input or state (docs/OPERATIONS.md → Data protection (logs)). The
	// values are in daily_scores and the admin view, the counts in the metrics.
	e.log.InfoContext(ctx, "scored day", "day", p.day, "model_version", *row.ModelVersion, "inputs_changed", written.InputsChanged)
	return written.InputsChanged, nil
}
