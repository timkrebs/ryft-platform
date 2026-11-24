package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition/score"
)

func morningN2R(ctx context.Context, tx pgx.Tx, user uuid.UUID, wake time.Time, asOf time.Time) (*model.N2RBiometrics, error) {
	key := wake.Format(time.DateOnly)
	rows, err := tx.Query(ctx, `SELECT calendar.day::date::text,
		coalesce(native.rmssd,CASE WHEN to_jsonb(d)->>'hrv_metric'='rmssd' THEN d.hrv_ms END),
		coalesce(native.rhr,d.rhr)
		FROM generate_series($2::date-7,$2::date,interval '1 day') calendar(day)
		LEFT JOIN daily_scores d ON d.user_id=$1 AND d.day=calendar.day::date AND (d.sleep_end IS NULL OR d.sleep_end<=$3)
		LEFT JOIN LATERAL (SELECT
			(array_agg(n.rmssd_ms ORDER BY n.wake_at DESC,n.provider) FILTER(WHERE n.rmssd_ms IS NOT NULL))[1] AS rmssd,
			(array_agg(n.rhr ORDER BY n.wake_at DESC,n.provider) FILTER(WHERE n.rhr IS NOT NULL))[1] AS rhr
			FROM nutrition.wearable_nights n WHERE n.user_id=$1 AND n.wake_at >= (calendar.day::date::timestamp AT TIME ZONE $4)
			AND n.wake_at < ((calendar.day::date+1)::timestamp AT TIME ZONE $4) AND n.wake_at<=$3) native ON true
		WHERE d.day IS NOT NULL OR native.rmssd IS NOT NULL OR native.rhr IS NOT NULL ORDER BY calendar.day`, user, key, asOf, wake.Location().String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var today *score.Overnight
	var prior []score.Overnight
	for rows.Next() {
		var date string
		var row score.Overnight
		if err := rows.Scan(&date, &row.RMSSDms, &row.RHR); err != nil {
			return nil, err
		}
		row.Date = parseN2RDay(date, wake.Location())
		if date == key {
			today = &row
		} else {
			prior = append(prior, row)
		}
	}
	if err := rows.Err(); err != nil || today == nil {
		return nil, err
	}
	v := score.MorningBaseline(*today, prior)
	return &v, nil
}

type dueNight struct {
	UserID uuid.UUID
	Date   string
}

// RefreshN2RNights persists a bounded pass through eligible wake days on the API tick.
func (s *Store) RefreshN2RNights(ctx context.Context, now time.Time, policy string, enabled func(uuid.UUID) bool) error {
	const pageSize, maxProcessed = 50, 25
	after := dueNight{Date: "1900-01-01"}
	var result error
	for count := 0; count < maxProcessed; {
		rows, err := s.pool.Query(ctx, `SELECT account_id,wake_date::text FROM nutrition.n2r_due($1,$2,$3,$4::date,$5)`, now, policy, after.UserID, after.Date, pageSize)
		if err != nil {
			return errors.Join(result, err)
		}
		due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dueNight])
		if err != nil {
			return errors.Join(result, err)
		}
		for _, night := range due {
			if !enabled(night.UserID) {
				continue
			}
			if err := s.refreshN2RNight(ctx, night, now, policy); err != nil {
				result = errors.Join(result, err)
			}
			count++
			if ctx.Err() != nil || count >= maxProcessed {
				return errors.Join(result, ctx.Err())
			}
		}
		if len(due) < pageSize {
			return result
		}
		after = due[len(due)-1]
	}
	return result
}

func (s *Store) refreshN2RNight(ctx context.Context, night dueNight, now time.Time, policy string) error {
	user := night.UserID
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		p, err := readN2RProfile(ctx, tx, user, true)
		if err != nil || p == nil {
			return err
		}
		if policy != "" {
			var current *string
			err := tx.QueryRow(ctx, `SELECT (SELECT policy_version FROM consents WHERE user_id=$1 AND kind='health_processing'
				AND revoked_at IS NULL ORDER BY granted_at DESC LIMIT 1)`, user).Scan(&current)
			if err != nil || current == nil || *current != policy {
				return err
			}
		}
		wake := parseN2RDay(night.Date, p.Location)
		b, err := morningN2R(ctx, tx, user, wake, now)
		if err != nil || b == nil {
			return err
		}
		intake := wake.AddDate(0, 0, -1).Format(time.DateOnly)
		h, err := readN2RHistory(ctx, tx, user, *p, intake, intake, now)
		if err != nil {
			return err
		}
		input := h.input(intake, true)
		var savedHalf float64
		err = tx.QueryRow(ctx, `SELECT effective_caffeine_half_life_h FROM nutrition.daily_biometric_baselines
			WHERE user_id=$1 AND wake_date=$2::date`, user, night.Date).Scan(&savedHalf)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if savedHalf > 0 {
			input.CalibratedHalfH = savedHalf
		}
		n2r := score.N2R(input)
		breakdown, err := json.Marshal(n2r)
		if err != nil {
			return err
		}
		// Use a fixed five-hour exposure scale across nights, including settings changes.
		input.CalibratedHalfH = 0
		input.Profile.CaffeineHalfH = nil
		input.CoachingHalfH = nil
		input.AlcoholSensitivity = 1
		comparison := score.N2R(input)
		_, err = tx.Exec(ctx, `INSERT INTO nutrition.daily_biometric_baselines AS baseline
			(user_id,wake_date,ln_rmssd_7d_mean,ln_rmssd_7d_sd,ln_rmssd_7d_cv,swc,ln_rmssd_delta,rhr_delta,change,
			alcohol_g_kg,caffeine_bed_mg,caffeine_calibration_mg,late_meal_penalty,n2r_score,effective_caffeine_half_life_h,n2r_score_breakdown)
			VALUES ($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)
			ON CONFLICT (user_id,wake_date) DO UPDATE SET ln_rmssd_7d_mean=excluded.ln_rmssd_7d_mean,
			ln_rmssd_7d_sd=excluded.ln_rmssd_7d_sd,ln_rmssd_7d_cv=excluded.ln_rmssd_7d_cv,
			swc=excluded.swc,ln_rmssd_delta=excluded.ln_rmssd_delta,rhr_delta=excluded.rhr_delta,change=excluded.change,
			alcohol_g_kg=excluded.alcohol_g_kg,caffeine_bed_mg=excluded.caffeine_bed_mg,caffeine_calibration_mg=excluded.caffeine_calibration_mg,
			late_meal_penalty=excluded.late_meal_penalty,n2r_score=excluded.n2r_score,
			effective_caffeine_half_life_h=excluded.effective_caffeine_half_life_h,n2r_score_breakdown=excluded.n2r_score_breakdown,processed_at=now()`,
			user, night.Date, b.RMSSDMean, b.RMSSDSD, b.RMSSDCV, b.SWC, b.RMSSDDelta, b.RHRDelta, b.Change,
			n2r.AlcoholGKG, n2r.CaffeineBedMg, comparison.CaffeineBedMg, n2r.LateMealPenalty, n2r.Score, n2r.CaffeineHalfH, string(breakdown))
		if err != nil {
			return err
		}
		if p.LastCalibratedDay != nil && p.LastCalibratedDay.Format(time.DateOnly) >= night.Date {
			return nil
		}
		if comparison.CaffeineBedMg == nil || b.RMSSDDelta == nil && b.RHRDelta == nil {
			return nil
		}
		if input.Coaching != nil {
			return nil
		}
		return tuneN2R(ctx, tx, user, *p, wake)
	})
}

func tuneN2R(ctx context.Context, tx pgx.Tx, user uuid.UUID, p n2rProfile, wake time.Time) error {
	date := wake.Format(time.DateOnly)
	rows, err := tx.Query(ctx, `SELECT wake_date::text,caffeine_calibration_mg,ln_rmssd_delta,swc,rhr_delta
		FROM nutrition.daily_biometric_baselines WHERE user_id=$1 AND wake_date BETWEEN $2::date-29 AND $2::date
		AND caffeine_calibration_mg IS NOT NULL ORDER BY wake_date`, user, date)
	if err != nil {
		return err
	}
	pairs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (score.CaffeinePair, error) {
		var pair score.CaffeinePair
		var day string
		err := r.Scan(&day, &pair.CaffeineMg, &pair.RMSSDDelta, &pair.SWC, &pair.RHRDelta)
		pair.WakeDate = parseN2RDay(day, wake.Location())
		return pair, err
	})
	if err != nil {
		return err
	}
	base := score.DefaultCaffeineHalfH
	if p.CaffeineHalfH != nil {
		base = *p.CaffeineHalfH
	}
	current := max(base, p.Calibrated)
	calibrated := score.CalibrateCaffeine(wake, current, pairs)
	var raised *float64
	if calibrated > base && calibrated <= 7.5 {
		raised = &calibrated
	}
	_, err = tx.Exec(ctx, `UPDATE nutrition.n2r_profiles SET calibrated_half_life_h=$2,last_calibrated_day=$3::date WHERE user_id=$1`, user, raised, date)
	if err != nil {
		return fmt.Errorf("persist N2R calibration: %w", err)
	}
	return nil
}

func n2rSleepLatency(ctx context.Context, tx pgx.Tx, user uuid.UUID, wakeDate string, asOf time.Time) (*float64, error) {
	var delta *float64
	err := tx.QueryRow(ctx, `SELECT (
		SELECT extract(epoch FROM (d.sleep_start-min(x.start_at)))/60
		FROM sleep_segments x WHERE x.user_id=$1 AND x.stage='inbed'
		AND x.start_at BETWEEN d.sleep_start-interval '2 hours' AND d.sleep_start AND x.end_at >= d.sleep_start)
		FROM daily_scores d WHERE d.user_id=$1 AND d.day=$2::date AND d.sleep_actual_min>0 AND d.sleep_end<=$3`, user, wakeDate, asOf).Scan(&delta)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return delta, err
}
