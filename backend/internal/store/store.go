// Package store is the only place SQL lives. Services call these methods;
// nothing else touches the pool directly.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

type Store struct{ pool *pgxpool.Pool }

// ErrNotFound is returned by single-row reads when the row does not exist.
var ErrNotFound = errors.New("not found")

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---- users / devices -------------------------------------------------------

func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (model.User, *time.Location, error) {
	var u model.User
	err := s.pool.QueryRow(ctx, `SELECT id, name, tz, max_hr, sleep_base_min FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Name, &u.TZ, &u.MaxHR, &u.SleepBaseMin)
	if err != nil {
		return u, nil, err
	}
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		return u, nil, fmt.Errorf("user tz %q: %w", u.TZ, err)
	}
	return u, loc, nil
}

func (s *Store) UpdateUser(ctx context.Context, id uuid.UUID, p model.UserPatch) error {
	if p.TZ != nil {
		if _, err := time.LoadLocation(*p.TZ); err != nil {
			return fmt.Errorf("invalid tz: %w", err)
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET
		tz = COALESCE($2, tz),
		max_hr = COALESCE($3, max_hr),
		sleep_base_min = COALESCE($4, sleep_base_min)
		WHERE id=$1`, id, p.TZ, p.MaxHR, p.SleepBaseMin)
	return err
}

func (s *Store) TouchDevice(ctx context.Context, userID, deviceID uuid.UUID, name string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO devices (id, user_id, name, last_sync_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (id) DO UPDATE SET last_sync_at = now(),
		  name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE devices.name END`,
		deviceID, userID, name)
	return err
}

// ---- ingest ----------------------------------------------------------------

func (s *Store) UpsertSamples(ctx context.Context, userID uuid.UUID, in []model.Sample) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, x := range in {
		b.Queue(`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_id, type, start_at, end_at, source) DO UPDATE SET value = EXCLUDED.value`,
			userID, x.Type, x.Start, x.End, x.Value, x.Unit, x.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) UpsertSleep(ctx context.Context, userID uuid.UUID, in []model.SleepSegment) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, x := range in {
		b.Queue(`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			userID, x.Start, x.End, x.Stage, x.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) UpsertWorkouts(ctx context.Context, userID uuid.UUID, in []model.Workout) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, w := range in {
		b.Queue(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (id) DO UPDATE SET activity=EXCLUDED.activity, start_at=EXCLUDED.start_at, end_at=EXCLUDED.end_at,
			  avg_hr=EXCLUDED.avg_hr, max_hr=EXCLUDED.max_hr, energy_kcal=EXCLUDED.energy_kcal,
			  distance_m=EXCLUDED.distance_m, source=EXCLUDED.source`,
			w.ID, userID, w.Activity, w.Start, w.End, w.AvgHR, w.MaxHR, w.EnergyKcal, w.DistanceM, w.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) runBatch(ctx context.Context, b *pgx.Batch) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res := tx.SendBatch(ctx, b)
	n := 0
	for i := 0; i < b.Len(); i++ {
		tag, err := res.Exec()
		if err != nil {
			_ = res.Close()
			return 0, fmt.Errorf("batch item %d: %w", i, err)
		}
		n += int(tag.RowsAffected())
	}
	if err := res.Close(); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// ---- reads for scoring -----------------------------------------------------

func (s *Store) SamplesBetween(ctx context.Context, userID uuid.UUID, typ string, from, to time.Time) ([]model.Sample, error) {
	rows, err := s.pool.Query(ctx, `SELECT type, start_at, end_at, value, unit, source FROM samples
		WHERE user_id=$1 AND type=$2 AND start_at >= $3 AND start_at < $4 ORDER BY start_at`,
		userID, typ, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Sample, error) {
		var x model.Sample
		err := r.Scan(&x.Type, &x.Start, &x.End, &x.Value, &x.Unit, &x.Source)
		return x, err
	})
}

// SleepEndingBetween returns segments whose end falls in [from,to).
func (s *Store) SleepEndingBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]model.SleepSegment, error) {
	rows, err := s.pool.Query(ctx, `SELECT start_at, end_at, stage, source FROM sleep_segments
		WHERE user_id=$1 AND end_at >= $2 AND end_at < $3 ORDER BY start_at`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.SleepSegment, error) {
		var x model.SleepSegment
		err := r.Scan(&x.Start, &x.End, &x.Stage, &x.Source)
		return x, err
	})
}

func (s *Store) WorkoutsBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]model.Workout, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m, source, strain
		FROM workouts WHERE user_id=$1 AND start_at >= $2 AND start_at < $3 ORDER BY start_at`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Workout, error) {
		var w model.Workout
		err := r.Scan(&w.ID, &w.Activity, &w.Start, &w.End, &w.AvgHR, &w.MaxHR, &w.EnergyKcal, &w.DistanceM, &w.Source, &w.Strain)
		return w, err
	})
}

// SetWorkoutLoad stores what scoring computed for one workout: strain, TRIMP
// and minutes in the five heart-rate-reserve zones.
func (s *Store) SetWorkoutLoad(ctx context.Context, id uuid.UUID, strain, trimp float64, zones [5]float64) error {
	z, err := json.Marshal(roundZones(zones[:]))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE workouts SET strain=$2, trimp=$3, zone_minutes=$4 WHERE id=$1`,
		id, strain, math.Round(trimp*10)/10, z)
	return err
}

// WorkoutRow is a workout with the load scoring attached to it. ZoneMinutes and
// TRIMP are nil until the workout's day has been scored.
type WorkoutRow struct {
	model.Workout
	ZoneMinutes []float64
	TRIMP       *float64
}

func (s *Store) Workout(ctx context.Context, userID, id uuid.UUID) (WorkoutRow, error) {
	var w WorkoutRow
	var zones []byte
	err := s.pool.QueryRow(ctx, `SELECT id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m,
		source, strain, zone_minutes, trimp FROM workouts WHERE user_id=$1 AND id=$2`, userID, id).
		Scan(&w.ID, &w.Activity, &w.Start, &w.End, &w.AvgHR, &w.MaxHR, &w.EnergyKcal, &w.DistanceM,
			&w.Source, &w.Strain, &zones, &w.TRIMP)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	if len(zones) > 0 {
		if err := json.Unmarshal(zones, &w.ZoneMinutes); err != nil {
			return w, fmt.Errorf("workout %s zone_minutes: %w", id, err)
		}
	}
	return w, nil
}

func roundZones(z []float64) []float64 {
	out := make([]float64, len(z))
	for k, v := range z {
		out[k] = math.Round(v*10) / 10
	}
	return out
}

// History is the per-day input the scoring baselines need.
type History struct {
	Day            string
	HRV            *float64
	RHR            *float64
	RespRate       *float64
	Strain         *float64
	SleepNeedMin   *int
	SleepActualMin *int
}

// ScoreHistory returns up to n scored days strictly before `day`, newest first.
func (s *Store) ScoreHistory(ctx context.Context, userID uuid.UUID, day string, n int) ([]History, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), hrv_ms, rhr, resp_rate, strain, sleep_need_min, sleep_actual_min
		FROM daily_scores WHERE user_id=$1 AND day < $2::date ORDER BY day DESC LIMIT $3`, userID, day, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (History, error) {
		var h History
		err := r.Scan(&h.Day, &h.HRV, &h.RHR, &h.RespRate, &h.Strain, &h.SleepNeedMin, &h.SleepActualMin)
		return h, err
	})
}

// ---- daily scores ----------------------------------------------------------

// SleepNight is the stored summary of the night that ended on Day. Pointers
// are nil for rows scored before migration 0003.
type SleepNight struct {
	Day         string
	Start, End  *time.Time
	TotalMin    *int
	InBedMin    *int
	DeepMin     *int
	LightMin    *int // HealthKit "core"
	REMMin      *int
	AwakeMin    *int
	HasStages   bool
	Performance *int
}

// DayLoad is the heart-rate load behind a day's strain. Nil fields mean no
// heart-rate data that day (or a row scored before migration 0004).
type DayLoad struct {
	TRIMP       *float64
	HRMin       *int
	ZoneMinutes []float64
}

// ScoreRow is everything scoring writes for one day: the score the API serves
// plus the typed columns other views are assembled from.
type ScoreRow struct {
	model.DailyScore
	Sleep SleepNight // zero when no sleep was recorded
	Load  DayLoad
}

func (s *Store) UpsertDailyScore(ctx context.Context, userID uuid.UUID, row ScoreRow) error {
	d, n, l := row.DailyScore, row.Sleep, row.Load
	details, err := json.Marshal(d.Details)
	if err != nil {
		return err
	}
	var zones []byte
	if l.ZoneMinutes != nil {
		if zones, err = json.Marshal(roundZones(l.ZoneMinutes)); err != nil {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO daily_scores
		(user_id, day, recovery, strain, sleep_performance, sleep_need_min, sleep_actual_min, sleep_debt_min,
		 hrv_ms, rhr, resp_rate, spo2, calibrating, details, readiness, state,
		 sleep_start, sleep_end, in_bed_min, deep_min, light_min, rem_min, awake_min, sleep_has_stages,
		 zone_minutes, trimp, hr_min, hrv_baseline_ms, rhr_baseline, baseline_days, computed_at)
		VALUES ($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
		 $25,$26,$27,$28,$29,$30,now())
		ON CONFLICT (user_id, day) DO UPDATE SET
		 readiness=EXCLUDED.readiness, state=EXCLUDED.state, recovery=EXCLUDED.recovery,
		 strain=EXCLUDED.strain, sleep_performance=EXCLUDED.sleep_performance,
		 sleep_need_min=EXCLUDED.sleep_need_min, sleep_actual_min=EXCLUDED.sleep_actual_min, sleep_debt_min=EXCLUDED.sleep_debt_min,
		 hrv_ms=EXCLUDED.hrv_ms, rhr=EXCLUDED.rhr, resp_rate=EXCLUDED.resp_rate, spo2=EXCLUDED.spo2,
		 calibrating=EXCLUDED.calibrating, details=EXCLUDED.details,
		 sleep_start=EXCLUDED.sleep_start, sleep_end=EXCLUDED.sleep_end, in_bed_min=EXCLUDED.in_bed_min,
		 deep_min=EXCLUDED.deep_min, light_min=EXCLUDED.light_min, rem_min=EXCLUDED.rem_min,
		 awake_min=EXCLUDED.awake_min, sleep_has_stages=EXCLUDED.sleep_has_stages,
		 zone_minutes=EXCLUDED.zone_minutes, trimp=EXCLUDED.trimp, hr_min=EXCLUDED.hr_min,
		 hrv_baseline_ms=EXCLUDED.hrv_baseline_ms, rhr_baseline=EXCLUDED.rhr_baseline,
		 baseline_days=EXCLUDED.baseline_days, computed_at=now()`,
		userID, d.Day, d.Recovery, d.Strain, d.SleepPerformance, d.SleepNeedMin, d.SleepActualMin, d.SleepDebtMin,
		d.HRVms, d.RHR, d.RespRate, d.SpO2, d.Calibrating, details, d.Readiness, d.State,
		n.Start, n.End, n.InBedMin, n.DeepMin, n.LightMin, n.REMMin, n.AwakeMin, n.HasStages,
		zones, l.TRIMP, l.HRMin, d.HRVBaselineMS, d.RHRBaseline, d.BaselineDays)
	return err
}

// DayLoadOf returns the stored heart-rate load of one day (zero DayLoad when
// the day is not scored).
func (s *Store) DayLoadOf(ctx context.Context, userID uuid.UUID, day string) (DayLoad, error) {
	var l DayLoad
	var zones []byte
	err := s.pool.QueryRow(ctx, `SELECT trimp, hr_min, zone_minutes FROM daily_scores WHERE user_id=$1 AND day=$2::date`,
		userID, day).Scan(&l.TRIMP, &l.HRMin, &zones)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if len(zones) > 0 {
		if err := json.Unmarshal(zones, &l.ZoneMinutes); err != nil {
			return l, fmt.Errorf("day %s zone_minutes: %w", day, err)
		}
	}
	return l, nil
}

// SleepNights returns the nights in [from, to] (inclusive local days) that have
// recorded sleep, oldest first.
func (s *Store) SleepNights(ctx context.Context, userID uuid.UUID, from, to string) ([]SleepNight, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), sleep_start, sleep_end, sleep_actual_min,
		in_bed_min, deep_min, light_min, rem_min, awake_min, sleep_has_stages, sleep_performance
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND sleep_actual_min IS NOT NULL
		ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SleepNight, error) {
		var n SleepNight
		err := r.Scan(&n.Day, &n.Start, &n.End, &n.TotalMin, &n.InBedMin, &n.DeepMin, &n.LightMin,
			&n.REMMin, &n.AwakeMin, &n.HasStages, &n.Performance)
		return n, err
	})
}

func (s *Store) DailyScores(ctx context.Context, userID uuid.UUID, from, to string) ([]model.DailyScore, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), readiness, state, recovery, strain, sleep_performance, sleep_need_min,
		sleep_actual_min, sleep_debt_min, hrv_ms, rhr, resp_rate, spo2, hrv_baseline_ms, rhr_baseline, baseline_days,
		calibrating, details, computed_at
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DailyScore, error) {
		var d model.DailyScore
		var details []byte
		err := r.Scan(&d.Day, &d.Readiness, &d.State, &d.Recovery, &d.Strain, &d.SleepPerformance, &d.SleepNeedMin, &d.SleepActualMin,
			&d.SleepDebtMin, &d.HRVms, &d.RHR, &d.RespRate, &d.SpO2, &d.HRVBaselineMS, &d.RHRBaseline,
			&d.BaselineDays, &d.Calibrating, &details, &d.ComputedAt)
		if err != nil {
			return d, err
		}
		if len(details) > 0 {
			_ = json.Unmarshal(details, &d.Details)
		}
		if d.Details == nil {
			d.Details = map[string]any{}
		}
		return d, nil
	})
}

// ---- trends ----------------------------------------------------------------

// trendColumns maps a trend metric to its daily_scores column. It is the
// whitelist: only these names are ever interpolated into SQL.
var trendColumns = map[string]string{
	model.MetricReadiness: "readiness",
	model.MetricRecovery:  "recovery",
	model.MetricStrain:    "strain",
	model.MetricSleep:     "sleep_performance",
	model.MetricSleepMin:  "sleep_actual_min",
	model.MetricHRV:       "hrv_ms",
	model.MetricRHR:       "rhr",
}

var baselineColumns = map[string]string{
	model.MetricHRV: "hrv_baseline_ms",
	model.MetricRHR: "rhr_baseline",
}

// Trend returns metric's non-null values in [from, to] (inclusive local days),
// oldest first, and for hrv/rhr the baseline stored on the last day in the
// range that has one.
func (s *Store) Trend(ctx context.Context, userID uuid.UUID, metric, from, to string) ([]model.TrendPoint, *float64, error) {
	col, ok := trendColumns[metric]
	if !ok {
		return nil, nil, fmt.Errorf("unknown trend metric %q", metric)
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT to_char(day,'YYYY-MM-DD'), %[1]s::double precision
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND %[1]s IS NOT NULL
		ORDER BY day`, col), userID, from, to)
	if err != nil {
		return nil, nil, err
	}
	points, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.TrendPoint, error) {
		var p model.TrendPoint
		err := r.Scan(&p.Day, &p.Value)
		return p, err
	})
	if err != nil {
		return nil, nil, err
	}
	bcol, ok := baselineColumns[metric]
	if !ok {
		return points, nil, nil
	}
	var baseline *float64
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT %[1]s FROM daily_scores
		WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND %[1]s IS NOT NULL
		ORDER BY day DESC LIMIT 1`, bcol), userID, from, to).Scan(&baseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return points, nil, nil
	}
	return points, baseline, err
}
