package scoring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Engine loads a day's raw data from the store, runs the algorithms and writes
// the daily_scores row. Each day's baselines and sleep debt come from the rows
// before it, so days are computed oldest first, and a change to a stored day
// re-scores the days after it (ComputeDays).
type Engine struct {
	st       *store.Store
	log      *slog.Logger
	onScored func(context.Context, model.ScoredEvent)
	consent  config.Mode
}

// NewEngine scores with the consent check on (CONSENT_ENFORCE's default).
func NewEngine(st *store.Store, log *slog.Logger) *Engine {
	return &Engine{st: st, log: log, consent: config.ModeOn}
}

// SetConsentMode is CONSENT_ENFORCE: off or warn score accounts without a
// consent too (warn counts them), before the beta opens (D87).
func (e *Engine) SetConsentMode(m config.Mode) { e.consent = m }

// Admits says whether user's data may be scored: a current consent, or the
// consent check not on.
func (e *Engine) Admits(u model.User) bool { return u.Consent != nil || e.consent != config.ModeOn }

// OnScored sets what runs when the user's local today first gets a recovery
// after a night with sleep: scoring publishes scored (the morning push).
// Rescores, the api's engine and past days never call it.
func (e *Engine) OnScored(f func(context.Context, model.ScoredEvent)) { e.onScored = f }

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

// ErrNoConsent: the user has withdrawn (or never given) consent to processing
// their health data, so nothing is scored (D78). Callers count it, not fail on it.
var ErrNoConsent = errors.New("no consent to health-data processing")

// maxCascadeDays bounds the days one call scores beyond the ones it was asked
// for (ComputeDays); RescoreBehind picks up what it leaves.
const maxCascadeDays = MaxRangeDays

// ComputeRange recomputes every day in [from, to], inclusive, oldest first,
// then the stored days after it whose history changed (ComputeDays).
func (e *Engine) ComputeRange(ctx context.Context, userID uuid.UUID, from, to string) error {
	n, err := RangeDays(from, to)
	if err != nil {
		return err
	}
	if n > MaxRangeDays {
		return fmt.Errorf("range too large (max %d days)", MaxRangeDays)
	}
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if !e.Admits(user) {
		metrics.DaysScored.WithLabelValues("no_consent").Add(float64(n))
		return ErrNoConsent
	}
	a, _ := time.ParseInLocation(dayLayout, from, loc)
	b, _ := time.ParseInLocation(dayLayout, to, loc)
	var days []string
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(dayLayout))
	}
	return e.walk(ctx, userID, loc, days)
}

// ComputeDay scores one local calendar day, then the stored days after it
// whose history it changed (ComputeDays); ErrNoConsent without consent.
func (e *Engine) ComputeDay(ctx context.Context, userID uuid.UUID, day string) error {
	return e.ComputeDays(ctx, userID, []string{day})
}

// ComputeDays scores the given local days, oldest first. A day's baselines,
// sleep debt, consistency and yesterday's strain come from the stored rows
// before it (ScoreHistory), so a day scored before its history (a backfill
// that arrives newest first, a late sample, two consumers at once) would keep
// the history it saw. So when scoring a day writes a new row or changes one
// of its history inputs (store.ScoreWrite), the stored days whose window holds
// it are scored again in the same pass, in order: the next baselineDays stored
// days after the last such change, never past the user's today. Each day is
// scored at most once per call, so a 400-day backfill costs its 400 days and
// at most 30 more, not 400×30, and a re-score that changes nothing goes no
// further than the days asked for. At most maxCascadeDays beyond those.
func (e *Engine) ComputeDays(ctx context.Context, userID uuid.UUID, days []string) error {
	if len(days) == 0 {
		return nil
	}
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if !e.Admits(user) {
		metrics.DaysScored.WithLabelValues("no_consent").Add(float64(len(days)))
		return ErrNoConsent
	}
	return e.walk(ctx, userID, loc, days)
}

// walk scores the asked-for days and the stored days their changes reach,
// oldest first, each once. reach is how many stored rows after the last
// change are still to be scored; every day scored after it is one of them.
// Stored days are fetched only where the asked-for days leave a gap, and as
// late as possible, so a row another consumer wrote meanwhile is included.
func (e *Engine) walk(ctx context.Context, userID uuid.UUID, loc *time.Location, asked []string) error {
	for _, d := range asked {
		if _, err := time.Parse(dayLayout, d); err != nil {
			return fmt.Errorf("bad day %q: %w", d, err)
		}
	}
	asked = slices.Compact(slices.Sorted(slices.Values(asked)))
	today := time.Now().In(loc).Format(dayLayout)
	var (
		last     string   // the day scored last
		reach    int      // stored rows after the last change still to score
		stored   []string // fetched for that, all before the next asked-for day
		cascaded int
		capped   bool
	)
	for {
		if len(stored) == 0 && reach > 0 && last < today && (len(asked) == 0 || asked[0] > addDays(last, 1)) {
			through := today
			if len(asked) > 0 && asked[0] <= today {
				through = addDays(asked[0], -1)
			}
			if room := maxCascadeDays - cascaded; room > 0 {
				var err error
				if stored, err = e.st.ScoredDaysAfter(ctx, userID, last, through, min(reach, room)); err != nil {
					return fmt.Errorf("days after %s: %w", last, err)
				}
			} else if !capped {
				capped = true
				e.log.WarnContext(ctx, "rescore after history change capped", "after", last, "days", cascaded)
			}
		}
		var day string
		again := len(stored) > 0
		switch {
		case again:
			day, stored = stored[0], stored[1:]
		case len(asked) > 0:
			day, asked = asked[0], asked[1:]
		default:
			if cascaded > 0 {
				e.log.InfoContext(ctx, "rescored days after a history change", "days", cascaded, "through", last)
			}
			return nil
		}
		changed, err := e.scoreDay(ctx, userID, day)
		if err != nil {
			return fmt.Errorf("day %s: %w", day, err)
		}
		if again {
			cascaded++
			metrics.DaysRescored.WithLabelValues("history").Inc()
		}
		switch {
		case changed:
			reach = baselineDays
		case reach > 0:
			reach--
		}
		last = day
	}
}

// RescoreBehind is the 15-minute pass's safety net for what ComputeDays
// cannot see: a day scored before its history whose re-score was lost (an
// attempt that failed half-way, whose retry finds nothing changed; two
// consumers scoring neighbouring days at once; the cap). It scores again, oldest
// first, up to n of the user's days within the last MaxRangeDays whose
// baseline_days is below what their stored history now holds
// (store.BehindHistoryDays), and returns how many it found.
func (e *Engine) RescoreBehind(ctx context.Context, userID uuid.UUID, today string, n int) (int, error) {
	from := addDays(today, -(MaxRangeDays - 1))
	if from == "" {
		return 0, fmt.Errorf("bad day %q", today)
	}
	days, err := e.st.BehindHistoryDays(ctx, userID, from, baselineDays, n)
	if err != nil || len(days) == 0 {
		return 0, err
	}
	metrics.DaysRescored.WithLabelValues("behind").Add(float64(len(days)))
	e.log.InfoContext(ctx, "rescoring days behind their history", "days", len(days), "from", days[0])
	return len(days), e.ComputeDays(ctx, userID, days)
}

const dayLayout = "2006-01-02"

// addDays moves a YYYY-MM-DD day by n calendar days ("" if it is not one).
func addDays(day string, n int) string {
	d, err := time.Parse(dayLayout, day)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, n).Format(dayLayout)
}

// scoreDay scores one day with its metrics and reports whether its history
// inputs changed.
func (e *Engine) scoreDay(ctx context.Context, userID uuid.UUID, day string) (bool, error) {
	start := time.Now()
	changed, err := e.computeDay(ctx, userID, day)
	if err == nil { // GET /v1/me/sync: this day is no longer pending (D91)
		queued, ok, merr := e.st.MarkScored(ctx, userID, day, start)
		switch {
		case merr != nil:
			e.log.WarnContext(ctx, "mark scored", "day", day, "err", merr)
		case ok:
			metrics.SyncToScore.Observe(time.Since(queued).Seconds())
		}
	}
	result := metrics.Result(err)
	if errors.Is(err, ErrNoConsent) {
		result = "no_consent"
	}
	metrics.DaysScored.WithLabelValues(result).Inc()
	metrics.ScoringDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
	return changed, err
}

// computeDay scores one day and reports whether its history inputs changed
// (store.ScoreWrite.InputsChanged).
func (e *Engine) computeDay(ctx context.Context, userID uuid.UUID, day string) (bool, error) {
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}
	if !e.Admits(user) {
		return false, ErrNoConsent
	}
	if user.Consent == nil && e.consent == config.ModeWarn {
		metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(config.ModeWarn), "scoring").Inc()
	}
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return false, fmt.Errorf("bad day %q: %w", day, err)
	}
	dayStart, dayEnd := d, d.AddDate(0, 0, 1)
	nightFrom, nightTo := NightWindow(d)

	// ---- sleep -------------------------------------------------------------
	// Yesterday's naps lower last night's need (D66), so both days' sleep.
	from, _ := DaySleepWindow(d.AddDate(0, 0, -1))
	_, to := DaySleepWindow(d)
	segs, err := e.st.SleepEndingBetween(ctx, userID, from, to)
	if err != nil {
		return false, err
	}
	nightSegs, napMin := SplitDay(segs, d)
	_, napYesterday := SplitDay(segs, d.AddDate(0, 0, -1))
	sleep := SummarizeSleep(nightSegs)

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
		return false, err
	}
	var lnHRV, rhrs, resps []float64
	var yesterdayStrain float64
	var window int // the days in hist with any data: calibrating_reason's W
	yesterday := dayStart.AddDate(0, 0, -1).Format("2006-01-02")
	// Consistency: the last 7 nights, this one included.
	firstNight := dayStart.AddDate(0, 0, -(consistencyDays - 1)).Format("2006-01-02")
	var bedtimes []Bedtime
	if sleep.AsleepMin > 0 {
		bedtimes = append(bedtimes, Bedtime{Bed: sleep.Start, Wake: sleep.End})
	}
	for _, h := range hist {
		if h.Day >= firstNight && h.SleepStart != nil && h.SleepEnd != nil {
			bedtimes = append(bedtimes, Bedtime{Bed: *h.SleepStart, Wake: *h.SleepEnd})
		}
		if h.HRV != nil && *h.HRV > 0 {
			lnHRV = append(lnHRV, math.Log(*h.HRV))
		}
		if h.RHR != nil && *h.RHR > 0 {
			rhrs = append(rhrs, *h.RHR)
		}
		if h.RespRate != nil && *h.RespRate > 0 {
			resps = append(resps, *h.RespRate)
		}
		if h.Day == yesterday && h.Strain != nil {
			yesterdayStrain = *h.Strain
		}
		if h.HasData {
			window++
		}
	}
	debt := SleepDebtFrom(hist)
	parts := SleepNeedParts(user.SleepBaseMin, yesterdayStrain, debt, napYesterday)
	need := parts.Total()

	var sleepPerf, quality *int
	if sleep.AsleepMin > 0 {
		p := SleepPerformance(sleep.AsleepMin, need)
		q, _ := NightQuality(sleep.AsleepMin, InBedMinutes(sleep), sleep.DeepMin, sleep.REMMin, sleep.HasStages, p)
		sleepPerf, quality = &p, &q
	}
	var consistency *int
	if c, ok := SleepConsistency(bedtimes, loc); ok {
		consistency = &c
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
		return false, err
	}
	strain := Strain(hrPoints(DedupSources(hrSamples)), strainRHR, maxHR)

	// Strength sessions first: they may create the workouts the loop below scores.
	strength, err := e.strength(ctx, userID, dayStart, dayEnd)
	if err != nil {
		return false, fmt.Errorf("strength: %w", err)
	}

	workouts, err := e.st.WorkoutsBetween(ctx, userID, dayStart, dayEnd)
	if err != nil {
		return false, err
	}
	for _, w := range workouts {
		// Each workout loads its own window rather than filtering the day's
		// samples: a 23:20–00:40 run must count its minutes after midnight too,
		// and match the curve GET /v1/workouts/{id} draws (same whole-hour
		// window, same source per hour).
		from, to := SampleWindow(w.Start, WorkoutWindowEnd(w))
		around, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, from, to)
		if err != nil {
			return false, err
		}
		var load StrainResult
		inWindow := hrPoints(WorkoutSamples(w, around))
		if len(inWindow) >= 2 {
			load = Strain(inWindow, strainRHR, maxHR)
		} else if w.AvgHR != nil {
			load = LoadFromAverage(*w.AvgHR, strainRHR, maxHR, w.End.Sub(w.Start))
		}
		if err := e.st.SetWorkoutLoad(ctx, userID, w.ID, strainOf(load, w), load.TRIMP, load.ZoneMinutes, ModelVersion); err != nil {
			return false, err
		}
	}

	// ---- activity ----------------------------------------------------------------
	steps, err := e.dayTotal(ctx, userID, model.SampleSteps, dayStart, dayEnd)
	if err != nil {
		return false, err
	}
	kcal, err := e.dayTotal(ctx, userID, model.SampleActiveEnergy, dayStart, dayEnd)
	if err != nil {
		return false, err
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
	// Muscular load joins the heart-rate TRIMP before the compression, so a day
	// with only a logged session has a strain too (SCORING.md → Strength).
	if strength.TRIMP > 0 {
		s, m := StrainFromTRIMP(strain.TRIMP+strength.TRIMP), strength.TRIMP
		strainScore, load.MuscularTRIMP = &s, &m
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
		"muscular_trimp":    math.Round(strength.TRIMP),
		"strength_week":     strength.Week,
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
	version := ModelVersion
	// The counts behind baseline_days, and why the day calibrates. The window is
	// the stored rows the baselines were drawn from that hold any data, not the
	// nights with a value: a phone-only month is 30 days of steps without an
	// HRV; a new account has none, even after a range stored empty days for it.
	hrvDays, rhrDays := lnHRVBase.N, rhrBase.N
	var reason *string
	if r := CalibratingReason(window, hrvDays, rhrDays); r != "" {
		reason = &r
	}

	row := model.DailyScore{
		Day: day, Readiness: readiness, State: state, Recovery: recovery, Strain: strainScore, Steps: steps, ActiveKcal: kcal,
		SleepPerformance: sleepPerf,
		SleepNeedMin:     &need, SleepActualMin: actual, SleepDebtMin: &debt,
		HRVms: hrv, RHR: rhr, RespRate: resp, SpO2: spo2, HRVBaselineMS: hrvBaseline, RHRBaseline: rhrBaseline,
		BaselineDays: min(hrvDays, rhrDays), BaselineHRVDays: &hrvDays, BaselineRHRDays: &rhrDays,
		Calibrating: rec.Calibrating, CalibratingReason: reason, Details: details,
		SleepNeedBaseMin: &parts.Base, SleepNeedStrainMin: &parts.Strain, SleepNeedDebtMin: &parts.Debt,
		SleepNeedNapMin: &parts.Nap, NapMin: &napMin, SleepQuality: quality, SleepConsistency: consistency,
		ModelVersion: &version,
	}
	if strength.Sessions > 0 {
		minutes, volume := strength.Minutes, math.Round(strength.VolumeKG*10)/10
		row.StrengthMin, row.VolumeKG = &minutes, &volume
	}
	written, err := e.st.UpsertDailyScore(ctx, userID, store.ScoreRow{DailyScore: row, Sleep: night, Load: load})
	if err != nil {
		return false, err
	}
	if written.FirstRecovery && sleep.AsleepMin > 0 && e.onScored != nil && day == time.Now().In(loc).Format("2006-01-02") {
		e.onScored(ctx, model.ScoredEvent{UserID: userID, Day: day, Recovery: *recovery, Readiness: readiness,
			State: state, SleepPerformance: sleepPerf, SleepMin: sleep.AsleepMin})
	}
	e.log.InfoContext(ctx, "scored day", "day", day, "readiness", deref(readiness), "state", state, "recovery", rec.Score, "strain", deref(strainScore), "muscular_trimp", strength.TRIMP,
		"sleep_min", sleep.AsleepMin, "nap_min", napMin, "sleep_need", need, "hrv", deref(hrv), "rhr", deref(rhr), "calibrating", rec.Calibrating)
	return written.InputsChanged, nil
}

func hrPoints(samples []model.Sample) []HRPoint {
	pts := make([]HRPoint, len(samples))
	for k, s := range samples {
		pts[k] = HRPoint{T: s.Start, BPM: s.Value}
	}
	return pts
}

// dayTotal sums a cumulative sample type (steps, active energy) over the
// local day; nil when there are no samples.
func (e *Engine) dayTotal(ctx context.Context, userID uuid.UUID, typ string, from, to time.Time) (*int, error) {
	xs, err := e.st.SamplesBetween(ctx, userID, typ, from, to)
	if err != nil || len(xs) == 0 {
		return nil, err
	}
	n := DayTotal(xs)
	return &n, nil
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
