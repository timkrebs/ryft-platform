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
	"github.com/timkrebs/ryft/internal/events"
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
	// maxAgain bounds the days one call scores beyond the ones it was asked
	// for (ComputeDays): MaxRangeDays, smaller in tests.
	maxAgain int
}

// NewEngine scores with the consent check on (CONSENT_ENFORCE's default).
func NewEngine(st *store.Store, log *slog.Logger) *Engine {
	return &Engine{st: st, log: log, consent: config.ModeOn, maxAgain: MaxRangeDays}
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
// one hour — the api and scoring must agree exactly on MaxRangeDays (D22).
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

// ErrNoConsent means the user has withdrawn (or never given) consent to processing
// their health data, so nothing is scored (D78). Callers count it, not fail on it.
var ErrNoConsent = errors.New("no consent to health-data processing")

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
	_, err = e.walk(ctx, userID, loc, days)
	return err
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
// further than the days asked for; at most MaxRangeDays beyond them.
//
// On a redelivered event (events.Redelivered: a retry, the broker's
// redelivery, or a dead letter replayed with scripts/dlq.sh) "nothing
// changed" proves nothing: the attempt before may have written the change and
// failed before the days after. Then every asked-for day counts as changed, so
// the retry finishes what the attempt began (D100).
func (e *Engine) ComputeDays(ctx context.Context, userID uuid.UUID, days []string) error {
	_, err := e.computeDays(ctx, userID, days)
	return err
}

func (e *Engine) computeDays(ctx context.Context, userID uuid.UUID, days []string) (walked, error) {
	if len(days) == 0 {
		return walked{}, nil
	}
	user, loc, err := e.st.GetUser(ctx, userID)
	if err != nil {
		return walked{}, err
	}
	if !e.Admits(user) {
		metrics.DaysScored.WithLabelValues("no_consent").Add(float64(len(days)))
		return walked{}, ErrNoConsent
	}
	return e.walk(ctx, userID, loc, days)
}

// walked counts the days a walk scored: the ones asked for, and the stored
// days after a change scored again.
type walked struct{ asked, again int }

// walk scores the asked-for days and the stored days their changes reach,
// oldest first, each once. reach is how many stored rows after the last
// change are still to be scored; every day scored after it is one of them.
// Stored days are fetched only where the asked-for days leave a gap, and as
// late as possible, so a row another consumer wrote meanwhile is included.
func (e *Engine) walk(ctx context.Context, userID uuid.UUID, loc *time.Location, asked []string) (walked, error) {
	var w walked
	for _, d := range asked {
		if _, err := time.Parse(dayLayout, d); err != nil {
			return w, fmt.Errorf("bad day %q: %w", d, err)
		}
	}
	asked = slices.Compact(slices.Sorted(slices.Values(asked)))
	today := time.Now().In(loc).Format(dayLayout)
	redo := events.Redelivered(ctx)
	var (
		last   string   // the day scored last
		reach  int      // stored rows after the last change still to score
		stored []string // fetched for that, all before the next asked-for day
		capped bool
	)
	for {
		if len(stored) == 0 && reach > 0 && last < today && (len(asked) == 0 || asked[0] > addDays(last, 1)) {
			through := today
			if len(asked) > 0 && asked[0] <= today {
				through = addDays(asked[0], -1)
			}
			if room := e.maxAgain - w.again; room > 0 {
				var err error
				if stored, err = e.st.ScoredDaysAfter(ctx, userID, last, through, min(reach, room)); err != nil {
					return w, fmt.Errorf("days after %s: %w", last, err)
				}
			} else if !capped {
				capped = true
				e.log.WarnContext(ctx, "rescore after history change capped", "after", last, "days", w.again)
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
			if w.again > 0 {
				e.log.InfoContext(ctx, "rescored days after a history change", "days", w.again, "through", last)
			}
			return w, nil
		}
		changed, err := e.scoreDay(ctx, userID, day)
		if err != nil {
			return w, fmt.Errorf("day %s: %w", day, err)
		}
		if again {
			w.again++
			metrics.DaysRescored.WithLabelValues("history").Inc()
		} else {
			w.asked++
			changed = changed || redo
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

// RescoreBehind is the 15-minute pass's safety net for stored days whose
// night counts or baselines show they were scored before their history and
// never again (two consumers scoring neighbouring days at once, the cap, rows
// from before the re-score existed): up to n of the user's days within the
// last MaxRangeDays (store.BehindHistoryDays), oldest first, with the days
// their re-score reaches. It sees counts and missing baselines only; a stale
// value behind a right count is for ComputeDays to prevent. It returns how many
// days it scored, the re-score after them included, also on an error.
func (e *Engine) RescoreBehind(ctx context.Context, userID uuid.UUID, today string, n int) (int, error) {
	from := addDays(today, -(MaxRangeDays - 1))
	if from == "" {
		return 0, fmt.Errorf("bad day %q", today)
	}
	days, err := e.st.BehindHistoryDays(ctx, userID, from, baselineDays, n)
	if err != nil || len(days) == 0 {
		return 0, err
	}
	w, err := e.computeDays(ctx, userID, days)
	metrics.DaysRescored.WithLabelValues("behind").Add(float64(w.asked))
	if w.asked > 0 {
		e.log.InfoContext(ctx, "rescored days behind their history", "days", w.asked, "from", days[0])
	}
	return w.asked + w.again, err
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
// (store.ScoreWrite.InputsChanged). Its steps (compute_steps.go) run as
// docs/SCORING.md reads: the night, the baselines from the stored days before
// it, the sleep need, recovery, the day's strain with its workouts and
// strength sessions, steps and energy, then the row. One source is picked per
// night (loadNight) and per hour of heart rate, steps and energy (strain,
// activity), SCORING.md → Several sources. The store reads and writes keep
// this order: the golden files score every month twice (D149) and the
// strength pass must follow the duplicate pass (strain).
func (e *Engine) computeDay(ctx context.Context, userID uuid.UUID, day string) (bool, error) {
	user, loc, err := e.admitted(ctx, userID)
	if err != nil {
		return false, err
	}
	d, err := time.ParseInLocation(dayLayout, day, loc)
	if err != nil {
		return false, fmt.Errorf("bad day %q: %w", day, err)
	}
	p := dayScore{day: day, loc: loc}
	if p.night, err = e.loadNight(ctx, userID, d); err != nil {
		return false, err
	}
	hist, err := e.st.ScoreHistory(ctx, userID, day, baselineDays)
	if err != nil {
		return false, err
	}
	p.base = baselines(hist, p.night.sleep, d)
	p.sleep = sleepNeed(user.SleepBaseMin, hist, p.base, p.night, loc)
	p.rec = recoveryOf(p.night, p.base, p.sleep.performance)
	if p.strain, err = e.strain(ctx, userID, day, d, strainRHR(p.night.rhr, p.base), float64(user.MaxHR)); err != nil {
		return false, err
	}
	if p.steps, p.kcal, err = e.activity(ctx, userID, d); err != nil {
		return false, err
	}
	return e.persist(ctx, userID, p)
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
