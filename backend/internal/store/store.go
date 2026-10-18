// Package store is the only place SQL lives. Services call these methods;
// nothing else touches the pool directly.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

type Store struct{ pool *pgxpool.Pool }

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
			res.Close()
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

func (s *Store) SetWorkoutStrain(ctx context.Context, id uuid.UUID, strain float64) error {
	_, err := s.pool.Exec(ctx, `UPDATE workouts SET strain=$2 WHERE id=$1`, id, strain)
	return err
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

func (s *Store) UpsertDailyScore(ctx context.Context, userID uuid.UUID, d model.DailyScore) error {
	details, err := json.Marshal(d.Details)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO daily_scores
		(user_id, day, recovery, strain, sleep_performance, sleep_need_min, sleep_actual_min, sleep_debt_min,
		 hrv_ms, rhr, resp_rate, spo2, calibrating, details, computed_at)
		VALUES ($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
		ON CONFLICT (user_id, day) DO UPDATE SET
		 recovery=EXCLUDED.recovery, strain=EXCLUDED.strain, sleep_performance=EXCLUDED.sleep_performance,
		 sleep_need_min=EXCLUDED.sleep_need_min, sleep_actual_min=EXCLUDED.sleep_actual_min, sleep_debt_min=EXCLUDED.sleep_debt_min,
		 hrv_ms=EXCLUDED.hrv_ms, rhr=EXCLUDED.rhr, resp_rate=EXCLUDED.resp_rate, spo2=EXCLUDED.spo2,
		 calibrating=EXCLUDED.calibrating, details=EXCLUDED.details, computed_at=now()`,
		userID, d.Day, d.Recovery, d.Strain, d.SleepPerformance, d.SleepNeedMin, d.SleepActualMin, d.SleepDebtMin,
		d.HRVms, d.RHR, d.RespRate, d.SpO2, d.Calibrating, details)
	return err
}

func (s *Store) DailyScores(ctx context.Context, userID uuid.UUID, from, to string) ([]model.DailyScore, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), recovery, strain, sleep_performance, sleep_need_min,
		sleep_actual_min, sleep_debt_min, hrv_ms, rhr, resp_rate, spo2, calibrating, details, computed_at
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DailyScore, error) {
		var d model.DailyScore
		var details []byte
		err := r.Scan(&d.Day, &d.Recovery, &d.Strain, &d.SleepPerformance, &d.SleepNeedMin, &d.SleepActualMin,
			&d.SleepDebtMin, &d.HRVms, &d.RHR, &d.RespRate, &d.SpO2, &d.Calibrating, &details, &d.ComputedAt)
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
