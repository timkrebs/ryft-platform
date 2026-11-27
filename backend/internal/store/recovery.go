package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
)

// Recovery without a watch (D291–D295, D298, D299; docs/API.md → Recovery without a watch): the morning check-in, the session
// RPE of a workout, today's recovery status and the flag that switches them.
//
// The pure check-in score lives in internal/scoring, which imports this package, so the store cannot call it: it
// gathers what the score depends on (CheckinFacts) and takes the function as a CheckinScorer, which the api
// injects. That keeps one transaction around a write and the rescoring of the later check-ins (D292, D299).

const (
	// checkinHistoryDays is how far back a check-in's score looks (the personal blend: 14 days), and how far
	// forward a change rescores.
	checkinHistoryDays = 14
	// checkinPriorNights is how many nights before a check-in feed its regularity (D292).
	checkinPriorNights = 7
	// dayWatchHours is the heart-rate coverage of the day before that makes a check-in's confidence medium (D291).
	dayWatchHours = 8
	// askFromHour is the local hour from which a day without a wearable night asks for its check-in.
	askFromHour = 4
	// suggestDays and suggestMin: the check-ins of the 7 days before today that pre-fill the next one, and the
	// least of them that make a suggestion (D291).
	suggestDays = 7
	suggestMin  = 3
)

// ErrHiddenDuplicate is returned by PutWorkoutRating for a workout the server hides behind its primary: rate the
// primary (answered 422).
var ErrHiddenDuplicate = errors.New("hidden duplicate workout")

// ---- the flag (kill switch) ---------------------------------------------------------------

// recoveryOn says whether the flag recovery.checkin is on for the user. It is the kill switch of D295: every read
// of a stored check-in or rating that feeds a number asks it, so while it is off they count nowhere (status,
// readiness, load, the N2R bedtime) and stay stored and readable.
func recoveryOn(ctx context.Context, q querier, user uuid.UUID) (bool, error) {
	f, err := scanFlag(q.QueryRow(ctx, `SELECT `+flagColumns+` FROM feature_flags WHERE key=$1`, model.FlagRecoveryCheckin))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return flags.On(f, user), nil
}

// RecoveryEnabled says whether the user has the flag recovery.checkin (the api's gate for the two writes and the
// status route).
func (s *Store) RecoveryEnabled(ctx context.Context, user uuid.UUID) (bool, error) {
	return recoveryOn(ctx, s.pool, user)
}

// ---- the wearable night (D291) --------------------------------------------------------------

// wearableNightSQL is the one rule, on a daily_scores row aliased d (the tiers, needs_checkin, superseded, the
// regularity's nights and the readiness path all use it, D291): DailyScore.recovery is not null, and the night
// ending on the day has asleep stages (a segment of stage asleep, core, deep or rem ending in scoring's night window,
// the day's local 20:00 before to 13:00, scoring.NightWindow; an inbed-only night, the iPhone's, has none) or an
// overnight HRV (hrv_ms, the sleep-window value scoring stores). A resting heart rate alone does not make one: a
// watch worn by day writes it every day. sleep_has_stages (core, deep or rem seen when it was scored) and hrv_ms
// answer first, so the sleep_segments lookup (index sleep_user_end) runs only for a day with a recovery and neither.
const wearableNightSQL = `d.recovery IS NOT NULL AND (d.sleep_has_stages OR d.hrv_ms IS NOT NULL OR EXISTS (
	SELECT 1 FROM sleep_segments g WHERE g.user_id = d.user_id AND g.stage IN ('asleep', 'core', 'deep', 'rem')
	AND g.end_at >= (d.day::timestamp AT TIME ZONE (SELECT u.tz FROM users u WHERE u.id = d.user_id)) - interval '4 hours'
	AND g.end_at < (d.day::timestamp AT TIME ZONE (SELECT u.tz FROM users u WHERE u.id = d.user_id)) + interval '13 hours'))`

// ---- what a check-in's score depends on ------------------------------------------------------

// CheckinReport is what the user reports.
type CheckinReport struct {
	Bedtime, WakeTime time.Time
	TZOffsetSec       int
	Quality           int
	Freshness         int
	Soreness          bool
}

// PriorNight is an earlier night's sleep as the regularity part reads it: a wearable night's measured times, or a
// check-in's reported ones, and the UTC offset of the clock at its midpoint.
type PriorNight struct {
	Bedtime, Wake time.Time
	TZOffsetSec   int
}

// CheckinFacts is everything one check-in's score depends on, as plain data (the pure function is
// scoring.CheckinScore, called by the api through a CheckinScorer).
type CheckinFacts struct {
	Date            string // the wake day
	Report          CheckinReport
	SleepBaseMin    int
	SleepBaseSource string
	// Prior is quality and freshness of the check-ins of the 14 days before the wake day.
	Prior [][2]int
	// PriorNights are up to 7 nights before it, newest first, the wearable night where a day has one.
	PriorNights []PriorNight
	// N2R is yesterday's Nutrition-to-Recovery score (computed with this check-in's bedtime) and its tags; nil
	// and empty without a profile.
	N2R     *int
	N2RTags []string
	// Journal is the journal of the day before the wake day (never the wake day itself, D294).
	Journal periodization.JournalFacts
}

// CheckinResult is what a CheckinScorer computes: the derived columns of a stored check-in.
type CheckinResult struct {
	SleepDurationMin int
	RecoveryScore    int
	Components       model.RecoveryComponents
	AcuteDrop        bool
	DropReason       string // "" without a drop
	Confounders      []string
	ModelVersion     string
}

// CheckinScorer computes a check-in's derived values from its facts (scoring.CheckinScore with the confounders).
type CheckinScorer func(CheckinFacts) CheckinResult

// ---- reading check-ins ------------------------------------------------------------------------

const checkinColumns = `c.wake_date::text, c.bedtime, c.wake_time, c.tz_offset_sec, c.perceived_sleep_quality,
	c.perceived_freshness, c.soreness, c.sleep_duration_min, c.recovery_score, c.components, c.acute_drop,
	coalesce(c.drop_reason, ''), c.confounders, c.model_version, c.created_at, c.updated_at`

// readCheckins lists the check-ins with wake days in [from, to], oldest first, with what is derived on read:
// confidence (the day before's wearable heart rate in 8 distinct hours), superseded (the day has a wearable
// night) and n2r_bedtime_applied (yesterday's N2R uses this bedtime). It does not look at the flag: stored
// check-ins stay readable while it is off.
func readCheckins(ctx context.Context, tx pgx.Tx, user uuid.UUID, loc *time.Location, from, to string) ([]model.RecoveryCheckin, error) {
	on, err := recoveryOn(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	enrolled := false
	if on {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nutrition.n2r_profiles WHERE user_id=$1)`, user).Scan(&enrolled); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+checkinColumns+`,
		EXISTS (SELECT 1 FROM daily_scores d WHERE d.user_id = c.user_id AND d.day = c.wake_date AND `+wearableNightSQL+`),
		(SELECT count(DISTINCT date_trunc('hour', s.start_at AT TIME ZONE $4::text)) FROM samples s
			WHERE s.user_id = c.user_id AND s.type = 'heart_rate'
			AND s.start_at >= ((c.wake_date - 1)::timestamp AT TIME ZONE $4::text)
			AND s.start_at < (c.wake_date::timestamp AT TIME ZONE $4::text)),
		$5::bool AND NOT EXISTS (SELECT 1 FROM daily_scores d WHERE d.user_id = c.user_id AND d.day = c.wake_date
			AND d.sleep_start IS NOT NULL AND d.sleep_actual_min > 0)
		FROM recovery_checkins c WHERE c.user_id=$1 AND c.wake_date BETWEEN $2::date AND $3::date
		ORDER BY c.wake_date`, user, from, to, loc.String(), on && enrolled)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.RecoveryCheckin, error) {
		var c model.RecoveryCheckin
		var components []byte
		var reason string
		var hours int
		if err := r.Scan(&c.Date, &c.Bedtime, &c.WakeTime, &c.TZOffsetSec, &c.PerceivedSleepQuality, &c.PerceivedFreshness,
			&c.Soreness, &c.SleepDurationMin, &c.RecoveryScore, &components, &c.AcuteDrop, &reason, &c.Confounders,
			&c.ModelVersion, &c.CreatedAt, &c.UpdatedAt, &c.Superseded, &hours, &c.N2RBedtimeApplied); err != nil {
			return c, err
		}
		if err := json.Unmarshal(components, &c.Components); err != nil {
			return c, fmt.Errorf("check-in components: %w", err)
		}
		if c.Confounders == nil {
			c.Confounders = []string{}
		}
		c.Confidence = model.RecoveryConfidenceLow
		if hours >= dayWatchHours {
			c.Confidence = model.RecoveryConfidenceMedium
		}
		c.Bedtime, c.WakeTime = c.Bedtime.UTC(), c.WakeTime.UTC()
		c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
		return c, nil
	})
}

// Checkins lists the check-ins with wake days in [from, to], oldest first (the range route and the export).
func (s *Store) Checkins(ctx context.Context, user uuid.UUID, loc *time.Location, from, to string) ([]model.RecoveryCheckin, error) {
	var out []model.RecoveryCheckin
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = readCheckins(ctx, tx, user, loc, from, to)
		return err
	})
	if out == nil {
		out = []model.RecoveryCheckin{}
	}
	return out, err
}

// Checkin returns the check-in of one wake day; ErrNotFound without one.
func (s *Store) Checkin(ctx context.Context, user uuid.UUID, loc *time.Location, date string) (model.RecoveryCheckin, error) {
	list, err := s.Checkins(ctx, user, loc, date, date)
	if err != nil {
		return model.RecoveryCheckin{}, err
	}
	if len(list) == 0 {
		return model.RecoveryCheckin{}, ErrNotFound
	}
	return list[0], nil
}

// readCheckinStates are the check-ins that readiness may use on the days in [from, to], by day: the ones without a
// wearable night on their day (a superseded check-in counts nowhere, D291). Callers ask only while the flag is on.
func readCheckinStates(ctx context.Context, q querier, user uuid.UUID, from, to string) (map[string]*periodization.CheckinState, error) {
	rows, err := q.Query(ctx, `SELECT c.wake_date::text, c.acute_drop, coalesce(c.drop_reason, ''), c.confounders, c.soreness
		FROM recovery_checkins c WHERE c.user_id=$1 AND c.wake_date BETWEEN $2::date AND $3::date
		AND NOT EXISTS (SELECT 1 FROM daily_scores d WHERE d.user_id = c.user_id AND d.day = c.wake_date AND `+wearableNightSQL+`)`,
		user, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*periodization.CheckinState{}
	for rows.Next() {
		var day string
		var s periodization.CheckinState
		if err := rows.Scan(&day, &s.AcuteDrop, &s.DropReason, &s.Confounders, &s.Soreness); err != nil {
			return nil, err
		}
		out[day] = &s
	}
	return out, rows.Err()
}

// ---- writing check-ins ------------------------------------------------------------------------

type recoveryUser struct {
	loc             *time.Location
	sleepBaseMin    int
	sleepBaseSource string
}

func readRecoveryUser(ctx context.Context, q querier, user uuid.UUID) (recoveryUser, error) {
	var ru recoveryUser
	var zone string
	if err := q.QueryRow(ctx, `SELECT tz, sleep_base_min, sleep_base_source FROM users WHERE id=$1`, user).
		Scan(&zone, &ru.sleepBaseMin, &ru.sleepBaseSource); err != nil {
		return ru, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return ru, fmt.Errorf("user tz %q: %w", zone, err)
	}
	ru.loc = loc
	return ru, nil
}

func addDays(date string, n int) string {
	d, _ := time.Parse(time.DateOnly, date)
	return d.AddDate(0, 0, n).Format(time.DateOnly)
}

// PutCheckin saves the check-in of one wake day (an idempotent upsert: the same report twice changes nothing,
// created_at stays, updated_at moves only when the report does) and rescores it and the later check-ins within
// 14 days, all in one transaction (D292). The handler has validated the report and the date.
func (s *Store) PutCheckin(ctx context.Context, user uuid.UUID, date string, rep CheckinReport, now time.Time, score CheckinScorer) (model.RecoveryCheckin, error) {
	var out model.RecoveryCheckin
	err := s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockCheckin, user); err != nil {
			return err
		}
		ru, err := readRecoveryUser(ctx, tx, user)
		if err != nil {
			return err
		}
		// The columns a new row needs now; the rescoring below computes them for real, with the history.
		first := score(CheckinFacts{Date: date, Report: rep, SleepBaseMin: ru.sleepBaseMin, SleepBaseSource: ru.sleepBaseSource})
		comps, err := json.Marshal(first.Components)
		if err != nil {
			return err
		}
		reason := &first.DropReason
		if first.DropReason == "" {
			reason = nil
		}
		if first.Confounders == nil {
			first.Confounders = []string{}
		}
		// updated_at moves with the report, nothing else: the right-hand sides read the old row.
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_checkins AS c (user_id, wake_date, bedtime, wake_time, tz_offset_sec,
			perceived_sleep_quality, perceived_freshness, soreness, sleep_duration_min, recovery_score, components,
			acute_drop, drop_reason, confounders, model_version)
			VALUES ($1, $2::date, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14, $15)
			ON CONFLICT (user_id, wake_date) DO UPDATE SET
			  updated_at = CASE WHEN (c.bedtime, c.wake_time, c.tz_offset_sec, c.perceived_sleep_quality, c.perceived_freshness, c.soreness)
			    IS DISTINCT FROM (EXCLUDED.bedtime, EXCLUDED.wake_time, EXCLUDED.tz_offset_sec, EXCLUDED.perceived_sleep_quality,
			    EXCLUDED.perceived_freshness, EXCLUDED.soreness) THEN now() ELSE c.updated_at END,
			  bedtime = EXCLUDED.bedtime, wake_time = EXCLUDED.wake_time, tz_offset_sec = EXCLUDED.tz_offset_sec,
			  perceived_sleep_quality = EXCLUDED.perceived_sleep_quality, perceived_freshness = EXCLUDED.perceived_freshness,
			  soreness = EXCLUDED.soreness`,
			user, date, rep.Bedtime, rep.WakeTime, rep.TZOffsetSec, rep.Quality, rep.Freshness, rep.Soreness,
			first.SleepDurationMin, first.RecoveryScore, string(comps), first.AcuteDrop, reason, first.Confounders, first.ModelVersion); err != nil {
			return err
		}
		if _, err := rescoreRange(ctx, tx, user, ru, date, addDays(date, checkinHistoryDays), now, score); err != nil {
			return err
		}
		list, err := readCheckins(ctx, tx, user, ru.loc, date, date)
		if err != nil || len(list) != 1 {
			return errors.Join(err, errors.New("saved check-in not found"))
		}
		out = list[0]
		return nil
	})
	return out, err
}

// DeleteCheckin removes the check-in of one wake day (also when there is none) and rescores the later ones within
// 14 days. The day falls back to whatever else it has; yesterday's N2R returns to the expected bedtime (D293).
func (s *Store) DeleteCheckin(ctx context.Context, user uuid.UUID, date string, now time.Time, score CheckinScorer) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockCheckin, user); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM recovery_checkins WHERE user_id=$1 AND wake_date=$2::date`, user, date)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ru, err := readRecoveryUser(ctx, tx, user)
		if err != nil {
			return err
		}
		_, err = rescoreRange(ctx, tx, user, ru, date, addDays(date, checkinHistoryDays), now, score)
		return err
	})
}

// RescoreStaleCheckins rescores the user's check-ins that a different model version computed (a model bump, D292).
// It returns how many it rescored.
func (s *Store) RescoreStaleCheckins(ctx context.Context, user uuid.UUID, version string, now time.Time, score CheckinScorer) (int, error) {
	n := 0
	err := s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var from, to *string
		if err := tx.QueryRow(ctx, `SELECT min(wake_date)::text, max(wake_date)::text FROM recovery_checkins
			WHERE user_id=$1 AND model_version <> $2`, user, version).Scan(&from, &to); err != nil {
			return err
		}
		if from == nil || to == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, lockCheckin, user); err != nil {
			return err
		}
		ru, err := readRecoveryUser(ctx, tx, user)
		if err != nil {
			return err
		}
		n, err = rescoreRange(ctx, tx, user, ru, *from, *to, now, score)
		return err
	})
	return n, err
}

type storedCheckin struct {
	date        string
	rep         CheckinReport
	duration    int
	score       int
	components  []byte
	drop        bool
	dropReason  string
	confounders []string
	version     string
}

// rescoreRange computes the check-ins with wake days in [from, through] from their reports and what is around them,
// and writes the derived columns that changed (never updated_at: that is the report's). Each reads the check-ins of
// the 14 days before it, so it runs oldest first. It returns how many rows it wrote.
func rescoreRange(ctx context.Context, tx pgx.Tx, user uuid.UUID, ru recoveryUser, from, through string, now time.Time, score CheckinScorer) (int, error) {
	rows, err := tx.Query(ctx, `SELECT c.wake_date::text, c.bedtime, c.wake_time, c.tz_offset_sec, c.perceived_sleep_quality,
		c.perceived_freshness, c.soreness, c.sleep_duration_min, c.recovery_score, c.components, c.acute_drop,
		coalesce(c.drop_reason, ''), c.confounders, c.model_version
		FROM recovery_checkins c WHERE c.user_id=$1 AND c.wake_date BETWEEN $2::date AND $3::date ORDER BY c.wake_date`,
		user, addDays(from, -checkinHistoryDays), through)
	if err != nil {
		return 0, err
	}
	stored, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedCheckin, error) {
		var c storedCheckin
		err := r.Scan(&c.date, &c.rep.Bedtime, &c.rep.WakeTime, &c.rep.TZOffsetSec, &c.rep.Quality, &c.rep.Freshness,
			&c.rep.Soreness, &c.duration, &c.score, &c.components, &c.drop, &c.dropReason, &c.confounders, &c.version)
		return c, err
	})
	if err != nil {
		return 0, err
	}
	byDate := make(map[string]storedCheckin, len(stored))
	for _, c := range stored {
		byDate[c.date] = c
	}
	if len(stored) == 0 || stored[len(stored)-1].date < from {
		return 0, nil // nothing at or after from: a delete of the newest one
	}
	through = min(through, stored[len(stored)-1].date) // no N2R for the days after the last check-in
	nights, err := readWearableNights(ctx, tx, user, ru.loc, addDays(from, -checkinPriorNights), addDays(through, -1))
	if err != nil {
		return 0, err
	}
	journal, err := readJournalFacts(ctx, tx, user, ru.loc, addDays(from, -1), addDays(through, -1))
	if err != nil {
		return 0, err
	}
	n2r, err := checkinN2R(ctx, tx, user, addDays(from, -1), addDays(through, -1), now)
	if err != nil {
		return 0, err
	}
	wrote := 0
	for _, c := range stored {
		if c.date < from {
			continue
		}
		f := CheckinFacts{Date: c.date, Report: c.rep, SleepBaseMin: ru.sleepBaseMin, SleepBaseSource: ru.sleepBaseSource,
			Journal: journal[addDays(c.date, -1)]}
		for k := 1; k <= checkinHistoryDays; k++ {
			if p, ok := byDate[addDays(c.date, -k)]; ok {
				f.Prior = append(f.Prior, [2]int{p.rep.Quality, p.rep.Freshness})
			}
		}
		for k := 1; k <= checkinPriorNights; k++ {
			day := addDays(c.date, -k)
			if night, ok := nights[day]; ok {
				f.PriorNights = append(f.PriorNights, night)
			} else if p, ok := byDate[day]; ok {
				f.PriorNights = append(f.PriorNights, PriorNight{Bedtime: p.rep.Bedtime, Wake: p.rep.WakeTime, TZOffsetSec: p.rep.TZOffsetSec})
			}
		}
		if yesterday, ok := n2r[addDays(c.date, -1)]; ok {
			f.N2R, f.N2RTags = yesterday.score, yesterday.tags
		}
		// The bedtime of yesterday's N2R is this check-in's unless a measured sleep start beats it: the
		// history read above already has the stored bedtimes, this one included.
		res := score(f)
		comps, err := json.Marshal(res.Components)
		if err != nil {
			return wrote, err
		}
		if res.Confounders == nil {
			res.Confounders = []string{}
		}
		if res.SleepDurationMin == c.duration && res.RecoveryScore == c.score && res.AcuteDrop == c.drop &&
			res.DropReason == c.dropReason && res.ModelVersion == c.version && slices.Equal(res.Confounders, c.confounders) &&
			jsonEqual(comps, c.components) {
			continue
		}
		reason := &res.DropReason
		if res.DropReason == "" {
			reason = nil
		}
		if _, err := tx.Exec(ctx, `UPDATE recovery_checkins SET sleep_duration_min=$3, recovery_score=$4, components=$5::jsonb,
			acute_drop=$6, drop_reason=$7, confounders=$8, model_version=$9 WHERE user_id=$1 AND wake_date=$2::date`,
			user, c.date, res.SleepDurationMin, res.RecoveryScore, string(comps), res.AcuteDrop, reason, res.Confounders, res.ModelVersion); err != nil {
			return wrote, err
		}
		wrote++
	}
	return wrote, nil
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ea, _ := json.Marshal(x)
	eb, _ := json.Marshal(y)
	return string(ea) == string(eb)
}

// readWearableNights are the wearable nights ending on the days in [from, to] with their measured times, for the
// regularity part.
func readWearableNights(ctx context.Context, q querier, user uuid.UUID, loc *time.Location, from, to string) (map[string]PriorNight, error) {
	rows, err := q.Query(ctx, `SELECT d.day::text, d.sleep_start, d.sleep_end FROM daily_scores d
		WHERE d.user_id=$1 AND d.day BETWEEN $2::date AND $3::date AND d.sleep_start IS NOT NULL AND d.sleep_end IS NOT NULL
		AND `+wearableNightSQL, user, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PriorNight{}
	for rows.Next() {
		var day string
		var n PriorNight
		if err := rows.Scan(&day, &n.Bedtime, &n.Wake); err != nil {
			return nil, err
		}
		_, n.TZOffsetSec = n.Bedtime.Add(n.Wake.Sub(n.Bedtime) / 2).In(loc).Zone()
		out[day] = n
	}
	return out, rows.Err()
}

// journalSlugs are the built-in behaviors a check-in's confounders read (D294).
var journalSlugs = []string{"alcohol", "alcohol_drinks", "caffeine_after_2pm", "last_caffeine", "late_meal", "sick", "travel", "work_stress"}

// readJournalFacts is the journal of the days in [from, to] as periodization reads it, by day. The journal's own
// day is the evening before the night, which is why a check-in reads the day before its wake day.
func readJournalFacts(ctx context.Context, q querier, user uuid.UUID, loc *time.Location, from, to string) (map[string]periodization.JournalFacts, error) {
	rows, err := q.Query(ctx, `SELECT e.day::text, b.slug, coalesce(e.value_bool, false), e.value_num
		FROM journal_entries e JOIN behaviors b ON b.id = e.behavior_id
		WHERE e.user_id=$1 AND e.day BETWEEN $2::date AND $3::date AND b.user_id IS NULL AND b.slug = ANY($4)`,
		user, from, to, journalSlugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]periodization.JournalFacts{}
	for rows.Next() {
		var day, slug string
		var yes bool
		var num *float64
		if err := rows.Scan(&day, &slug, &yes, &num); err != nil {
			return nil, err
		}
		j := out[day]
		switch slug {
		case "alcohol":
			j.Alcohol = yes
		case "alcohol_drinks":
			if num != nil {
				j.AlcoholDrinks = int(*num)
			}
		case "caffeine_after_2pm":
			j.CaffeineAfter2pm = yes
		case "last_caffeine":
			if num != nil {
				d, err := time.ParseInLocation(time.DateOnly, day, loc)
				if err != nil {
					return nil, err
				}
				at := time.Date(d.Year(), d.Month(), d.Day(), int(*num)/60, int(*num)%60, 0, 0, loc)
				j.LastCaffeine = &at
			}
		case "late_meal":
			j.LateMeal = yes
		case "sick":
			j.Sick = yes
		case "travel":
			j.Travel = yes
		case "work_stress":
			j.WorkStress = yes
		}
		out[day] = j
	}
	return out, rows.Err()
}

type checkinN2RDay struct {
	score *int
	tags  []string
}

// checkinN2R is the live N2R of the days in [from, to] with the stored check-ins' bedtimes applied (D293),
// by day, rounded as the check-in's n2r part reads it. Empty for an account without an N2R profile.
func checkinN2R(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, to string, now time.Time) (map[string]checkinN2RDay, error) {
	p, err := readN2RProfile(ctx, tx, user, false)
	if err != nil || p == nil {
		return nil, err
	}
	// The check-ins are the point here, whatever the flag says: a save cannot happen while it is off, and a delete
	// rescoring the later ones uses what is stored.
	h, err := readN2RHistoryWith(ctx, tx, user, *p, from, to, now, true)
	if err != nil {
		return nil, err
	}
	out := map[string]checkinN2RDay{}
	for d := parseN2RDay(from, p.Location); !d.After(parseN2RDay(to, p.Location)); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		r := h.n2r(key, true)
		day := checkinN2RDay{tags: r.Tags}
		if r.Score != nil {
			v := int(math.Round(*r.Score))
			day.score = &v
		}
		out[key] = day
	}
	return out, nil
}

// ---- bedtimes of the next morning's check-ins (D293) -------------------------------------------

// checkinBedtimes are the reported bedtimes of the check-ins with wake days in [from+1, to+1], keyed by the day
// before: the N2R day whose evening they end.
func checkinBedtimes(ctx context.Context, q querier, user uuid.UUID, from, to string) (map[string]time.Time, error) {
	rows, err := q.Query(ctx, `SELECT (wake_date - 1)::text, bedtime FROM recovery_checkins
		WHERE user_id=$1 AND wake_date BETWEEN $2::date + 1 AND $3::date + 1`, user, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var day string
		var at time.Time
		if err := rows.Scan(&day, &at); err != nil {
			return nil, err
		}
		out[day] = at
	}
	return out, rows.Err()
}

// ---- today's status (D291) ---------------------------------------------------------------------

// RecoveryStatus is today's recovery for the account (the route, and the briefing's recovery): which source, its
// confidence, the check-in, and whether to ask. Callers check the flag first.
func (s *Store) RecoveryStatus(ctx context.Context, user uuid.UUID, now time.Time) (model.RecoveryStatus, error) {
	var out model.RecoveryStatus
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		ru, err := readRecoveryUser(ctx, tx, user)
		if err != nil {
			return err
		}
		p, err := readN2RProfile(ctx, tx, user, false)
		if err != nil {
			return err
		}
		out, err = recoveryStatusTx(ctx, tx, user, ru.loc, p, now)
		return err
	})
	return out, err
}

func recoveryStatusTx(ctx context.Context, tx pgx.Tx, user uuid.UUID, loc *time.Location, p *n2rProfile, now time.Time) (model.RecoveryStatus, error) {
	local := now.In(loc)
	today := local.Format(time.DateOnly)
	st := model.RecoveryStatus{Date: today, Source: model.RecoverySourceNone}
	list, err := readCheckins(ctx, tx, user, loc, today, today)
	if err != nil {
		return st, err
	}
	var checkin *model.RecoveryCheckin
	if len(list) == 1 {
		checkin = &list[0]
		st.Checkin = checkin
	}
	var recovery *int
	err = tx.QueryRow(ctx, `SELECT d.recovery::int FROM daily_scores d WHERE d.user_id=$1 AND d.day=$2::date AND `+wearableNightSQL,
		user, today).Scan(&recovery)
	wearable := err == nil && recovery != nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, err
	}
	switch {
	case wearable:
		st.Source, st.RecoveryScore = model.RecoverySourceWearable, recovery
		high := model.RecoveryConfidenceHigh
		st.Confidence = &high
	case checkin != nil:
		st.Source = model.RecoverySourceCheckin
		score, confidence := checkin.RecoveryScore, checkin.Confidence
		st.RecoveryScore, st.Confidence = &score, &confidence
	}
	st.NeedsCheckin = !wearable && checkin == nil && local.Hour() >= askFromHour
	st.SuggestedBedtime, st.SuggestedWakeTime, st.SuggestionBasis, err = suggestTimes(ctx, tx, user, today, p)
	return st, err
}

// suggestTimes pre-fills the check-in form (never advice): the median bedtime and wake time of the check-ins of
// the 7 days before today (at least 3), else the N2R expected bedtime and 8 hours after it, else nothing (D291).
func suggestTimes(ctx context.Context, tx pgx.Tx, user uuid.UUID, today string, p *n2rProfile) (bed, wake, basis *string, err error) {
	rows, err := tx.Query(ctx, `SELECT bedtime, wake_time, tz_offset_sec FROM recovery_checkins
		WHERE user_id=$1 AND wake_date BETWEEN $2::date AND $3::date`, user, addDays(today, -suggestDays), addDays(today, -1))
	if err != nil {
		return nil, nil, nil, err
	}
	type pair struct {
		bed, wake time.Time
		tz        int
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pair, error) {
		var x pair
		err := r.Scan(&x.bed, &x.wake, &x.tz)
		return x, err
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(list) >= suggestMin {
		var beds, wakes []int
		for _, x := range list {
			beds = append(beds, clockMinutes(x.bed, x.tz))
			wakes = append(wakes, clockMinutes(x.wake, x.tz))
		}
		b, w, basis := clockText(medianClock(beds)), clockText(medianClock(wakes)), model.RecoveryBasisCheckins7d
		return &b, &w, &basis, nil
	}
	if p != nil && p.ExpectedBedtime != "" {
		at, err := time.Parse("15:04", p.ExpectedBedtime)
		if err == nil {
			b, w, basis := at.Format("15:04"), at.Add(8*time.Hour).Format("15:04"), model.RecoveryBasisExpectedBedtime
			return &b, &w, &basis, nil
		}
	}
	return nil, nil, nil, nil
}

// clockMinutes is the minutes after midnight on the clock offset by tz seconds.
func clockMinutes(at time.Time, tz int) int {
	m := int((at.Unix()+int64(tz))%86400) / 60
	if m < 0 {
		m += 1440
	}
	return m
}

// medianClock is the median of clock minutes, taken across midnight: 22:30, 23:30 and 00:40 sit in that order, as
// they are the evening and the night after it (the clock is shifted by 12 h so noon is the cut). An even count
// takes the mean of the two middle ones.
func medianClock(minutes []int) int {
	shifted := make([]int, len(minutes))
	for i, m := range minutes {
		shifted[i] = (m + 720) % 1440
	}
	slices.Sort(shifted)
	mid := len(shifted) / 2
	v := shifted[mid]
	if len(shifted)%2 == 0 {
		v = (shifted[mid-1] + shifted[mid] + 1) / 2
	}
	return (v + 720) % 1440
}

func clockText(m int) string { return fmt.Sprintf("%02d:%02d", m/60%24, m%60) }

// ---- session RPE (D293) --------------------------------------------------------------------------

// withRating sets the read-only rating fields of a workout: the rating, and Foster's session load, the duration in
// whole minutes × the rating.
func withRating(w *model.Workout, rpe *float64) {
	if rpe == nil {
		return
	}
	w.SessionRPE = rpe
	load := energy.SessionRPELoad(w.End.Sub(w.Start).Minutes(), *rpe)
	w.SessionRPELoad = &load
}

// PutWorkoutRating stores (or replaces) the user's rating of one of their workouts and returns the workout.
// ErrNotFound when it is not the user's (or gone), ErrHiddenDuplicate for a hidden duplicate. The composite foreign
// key keeps the rating the workout's owner's.
func (s *Store) PutWorkoutRating(ctx context.Context, user, id uuid.UUID, rpe float64) (model.Workout, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var hidden bool
		err := tx.QueryRow(ctx, `SELECT w.duplicate_of IS NOT NULL AND w.source <> $3 AND EXISTS (SELECT 1 FROM workouts p
			WHERE p.user_id = w.user_id AND p.id = w.duplicate_of AND p.duplicate_of IS NULL)
			FROM workouts w WHERE w.user_id=$1 AND w.id=$2`, user, id, model.SessionSource).Scan(&hidden)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if hidden {
			return ErrHiddenDuplicate
		}
		_, err = tx.Exec(ctx, `INSERT INTO workout_ratings AS r (user_id, workout_id, session_rpe)
			VALUES ($1, $2, $3::float8::numeric(3,1))
			ON CONFLICT (user_id, workout_id) DO UPDATE SET session_rpe = EXCLUDED.session_rpe,
			  updated_at = CASE WHEN r.session_rpe IS DISTINCT FROM EXCLUDED.session_rpe THEN now() ELSE r.updated_at END`,
			user, id, rpe)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23503" { // the workout went away between the two statements
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return model.Workout{}, err
	}
	row, err := s.Workout(ctx, user, id)
	return row.Workout, err
}

// DeleteWorkoutRating removes the rating of one of the user's workouts, rated or not. ErrNotFound when the workout
// is not the user's or is gone.
func (s *Store) DeleteWorkoutRating(ctx context.Context, user, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workouts WHERE user_id=$1 AND id=$2)`, user, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		_, err := tx.Exec(ctx, `DELETE FROM workout_ratings WHERE user_id=$1 AND workout_id=$2`, user, id)
		return err
	})
}

// moveFoldedRatings moves the rating of a dropped duplicate to the copy the fold keeps, before the duplicate is
// deleted (the delete cascades to its rating). A kept copy that already has a rating keeps it, and the dropped
// one's goes with its workout; of several rated copies of one kept workout the latest rating wins. It runs in the
// fold's transaction as ryft_ingest, which may repoint a rating but never write a value (0063).
func moveFoldedRatings(ctx context.Context, tx pgx.Tx, user uuid.UUID, groups []FoldGroup) error {
	var dropped, kept []uuid.UUID
	for _, g := range groups {
		for _, id := range g.Folded {
			dropped, kept = append(dropped, id), append(kept, g.Keep)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, moveFoldedRatingsSQL, user, dropped, kept)
	return err
}

// moveFoldedRatingsSQL is the one statement of moveFoldedRatings; testdata/applysync_trace.golden records it.
const moveFoldedRatingsSQL = `WITH pairs AS (
		SELECT t.drop_id, t.keep_id FROM unnest($2::uuid[], $3::uuid[]) AS t(drop_id, keep_id)
	), pick AS (
		SELECT DISTINCT ON (p.keep_id) r.workout_id AS from_id, p.keep_id
		FROM pairs p JOIN workout_ratings r ON r.user_id = $1 AND r.workout_id = p.drop_id
		WHERE NOT EXISTS (SELECT 1 FROM workout_ratings k WHERE k.user_id = $1 AND k.workout_id = p.keep_id)
		ORDER BY p.keep_id, r.updated_at DESC, r.workout_id
	)
	UPDATE workout_ratings r SET workout_id = pick.keep_id, updated_at = now()
	FROM pick WHERE r.user_id = $1 AND r.workout_id = pick.from_id`
