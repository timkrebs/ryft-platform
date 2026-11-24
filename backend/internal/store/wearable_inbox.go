package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/wearables"
)

// EnqueueWearable records authenticated deliveries for linked, consenting accounts.
func (s *Store) EnqueueWearable(ctx context.Context, provider, policy string, deliveries []wearables.Delivery) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		batch := new(pgx.Batch)
		for _, d := range deliveries {
			data, err := json.Marshal(d)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			batch.Queue(`SELECT nutrition.enqueue_wearable($1,$2,$3,$4::jsonb,$5)`, provider, d.User, hash[:], string(data), policy)
		}
		return tx.SendBatch(ctx, batch).Close()
	})
}

// WearableJob is a leased partner delivery and its associated account.
type WearableJob struct {
	ID       int64
	Source   string
	User     uuid.UUID
	Delivery wearables.Delivery
	Attempt  int16
}

// ClaimWearable leases one due delivery without blocking parallel consumers.
func (s *Store) ClaimWearable(ctx context.Context, now time.Time, policy string) (*WearableJob, error) {
	var j WearableJob
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT job_id,src,owner_id,payload,attempt FROM nutrition.claim_wearable($1,$2)`, now, policy).Scan(&j.ID, &j.Source, &j.User, &data, &j.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, json.Unmarshal(data, &j.Delivery)
}

// FinishWearable completes or reschedules the current attempt.
func (s *Store) FinishWearable(ctx context.Context, j WearableJob, now time.Time, ok bool) error {
	_, err := s.pool.Exec(ctx, `SELECT nutrition.finish_wearable($1,$2,$3,$4)`, j.ID, j.Attempt, now, ok)
	return err
}

// ApplyWearable applies a partner delivery under the linked account scope.
func (s *Store) ApplyWearable(ctx context.Context, j WearableJob, data wearables.Delivery, policy string, now time.Time) error {
	return s.nutritionTx(ctx, j.User, pgx.TxOptions{}, func(tx pgx.Tx) error {
		p, err := readCoachingProfile(ctx, tx, j.User, true)
		if err != nil || p == nil {
			return err
		}
		if ok, err := coachingConsent(ctx, tx, j.User, policy); err != nil || !ok {
			return err
		}
		for _, w := range data.Workouts {
			if !w.Valid() || !validWearableTime(w.Start, now) {
				continue
			}
			var extras *string
			if len(w.SuuntoPlus) > 0 {
				data, err := json.Marshal(w.SuuntoPlus)
				if err != nil {
					return err
				}
				s := string(data)
				extras = &s
			}
			_, err := tx.Exec(ctx, `INSERT INTO nutrition.canonical_workouts AS old
				(user_id,source_provider,source_priority,external_id,parent_workout_id,start_at,end_at,tz_offset_sec,
				was_user_entered,sport_type,duration_sec,active_kcal_raw,mechanical_work_kj,distance_m,avg_hr,max_hr,
				normalized_power_w,ftp_w,rr_intervals_available,suunto_plus)
				VALUES($1,$2,$3,$4,nullif($5,''),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20::jsonb)
				ON CONFLICT(user_id,source_provider,external_id) DO UPDATE SET
				source_priority=excluded.source_priority,parent_workout_id=excluded.parent_workout_id,start_at=excluded.start_at,
				end_at=excluded.end_at,tz_offset_sec=excluded.tz_offset_sec,was_user_entered=excluded.was_user_entered,
				sport_type=excluded.sport_type,duration_sec=excluded.duration_sec,active_kcal_raw=excluded.active_kcal_raw,
				mechanical_work_kj=excluded.mechanical_work_kj,distance_m=excluded.distance_m,avg_hr=excluded.avg_hr,max_hr=excluded.max_hr,
				normalized_power_w=excluded.normalized_power_w,ftp_w=excluded.ftp_w,rr_intervals_available=excluded.rr_intervals_available,suunto_plus=excluded.suunto_plus
				WHERE excluded.duration_sec>old.duration_sec`, j.User, w.Provider, w.Priority(), w.ExternalID, w.ParentID, w.Start.UTC(), w.End.UTC(),
				w.OffsetSec, w.UserEntered, wearables.Sport(w.Sport), w.Seconds(), w.RawKcal, w.WorkKJ, w.DistanceM, w.AvgHR, w.MaxHR, w.NormalizedPower, w.FTP, w.HasRR, extras)
			if err != nil {
				return err
			}
		}
		for _, n := range data.Nights {
			if !validWearableTime(n.WakeAt, now) {
				continue
			}
			_, err := tx.Exec(ctx, `INSERT INTO nutrition.wearable_nights AS old(user_id,provider,wake_at,rmssd_ms,rhr,sws_min,rem_min)
				VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id,provider,wake_at) DO UPDATE SET
				rmssd_ms=coalesce(excluded.rmssd_ms,old.rmssd_ms),rhr=coalesce(excluded.rhr,old.rhr),
				sws_min=coalesce(excluded.sws_min,old.sws_min),rem_min=coalesce(excluded.rem_min,old.rem_min)`,
				j.User, j.Source, n.WakeAt.UTC(), wearableRange(n.RMSSD, 1, 300), wearableRange(n.RHR, 25, 130), wearableRange(n.Deep, 0, 720), wearableRange(n.REM, 0, 720))
			if err != nil {
				return err
			}
		}
		for _, w := range data.Weights {
			if !validWearableTime(w.At, now) || w.Mass < 25 || w.Mass > 350 {
				continue
			}
			_, err := tx.Exec(ctx, `INSERT INTO nutrition.wearable_weights(user_id,provider,at_time,mass_kg) VALUES($1,$2,$3,$4)
				ON CONFLICT(user_id,provider,at_time) DO UPDATE SET mass_kg=excluded.mass_kg`, j.User, j.Source, w.At.UTC(), w.Mass)
			if err != nil {
				return err
			}
		}
		for at, kcal := range wearables.TenMinuteActivity(data.Activity) {
			if !validWearableTime(at, now) {
				continue
			}
			_, err := tx.Exec(ctx, `INSERT INTO nutrition.wearable_activity(user_id,provider,slot,active_kcal) VALUES($1,$2,$3,$4)
				ON CONFLICT(user_id,provider,slot) DO UPDATE SET active_kcal=excluded.active_kcal`, j.User, j.Source, at, kcal)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func validWearableTime(at, now time.Time) bool {
	return !at.IsZero() && !at.Before(now.AddDate(-1, 0, 0)) && !at.After(now.Add(10*time.Minute))
}

func wearableRange(value *float64, low, high float64) *float64 {
	if value != nil && *value >= low && *value <= high {
		return value
	}
	return nil
}

func coachingConsent(ctx context.Context, tx pgx.Tx, user uuid.UUID, policy string) (bool, error) {
	if policy == "" {
		return true, nil
	}
	var granted bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT policy_version FROM public.consents WHERE user_id=$1
		AND kind='health_processing' AND revoked_at IS NULL ORDER BY granted_at DESC LIMIT 1)=$2,false)`, user, policy).Scan(&granted)
	return granted, err
}

// PurgeWearableInbox removes expired payloads and replay markers.
func (s *Store) PurgeWearableInbox(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `SELECT nutrition.purge_wearable($1)`, now)
	return err
}
