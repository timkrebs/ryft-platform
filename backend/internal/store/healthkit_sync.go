package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/model"
)

// ErrHealthKitConsentChanged means the grant that admitted an upload is no
// longer active. Regranting consent requires a new request.
var ErrHealthKitConsentChanged = errors.New("HealthKit consent changed")

// HealthKitBodyMassAllowed restricts optional weight collection to the
// protected Nutrition pilot. Ordinary HealthKit streams use the consent gate.
func (s *Store) HealthKitBodyMassAllowed(ctx context.Context, user uuid.UUID) (bool, error) {
	var allowlisted bool
	var percentage int
	err := s.pool.QueryRow(ctx, `SELECT allowlisted, rollout_pct FROM public.healthkit_body_mass_rollout($1)`, user).Scan(&allowlisted, &percentage)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && (allowlisted || flags.Bucket(user, model.FlagNutrition) < percentage), err
}

func (s *Store) healthKitTx(ctx context.Context, user uuid.UUID, grant *model.Consent, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockApply, user); err != nil {
			return err
		}
		if err := requireGrant(ctx, tx, user, grant); err != nil {
			return err
		}
		return fn(tx)
	})
}

// lockGrant is what a write of health data outside healthKitTx does first: with a grant, take lockApply (the lock
// RevokeConsent takes) and check the grant under it. A nil grant takes no lock: consent is not enforced.
func lockGrant(ctx context.Context, tx pgx.Tx, user uuid.UUID, grant *model.Consent) error {
	if grant == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, lockApply, user); err != nil {
		return err
	}
	return requireGrant(ctx, tx, user, grant)
}

// requireGrant checks, inside tx, that grant is still the user's current health-processing consent, as
// ErrHealthKitConsentChanged when it was revoked or replaced. A nil grant is reserved for explicit unenforced modes.
// The caller holds lockApply, the lock RevokeConsent takes, so a withdrawal either committed before the check
// (the write is refused) or waits for the write's transaction. The recovery writes use it too (D300).
func requireGrant(ctx context.Context, tx pgx.Tx, user uuid.UUID, grant *model.Consent) error {
	if grant == nil {
		return nil
	}
	var current model.Consent
	err := tx.QueryRow(ctx, `SELECT policy_version, granted_at FROM public.consents
		WHERE user_id=$1 AND kind='health_processing' AND revoked_at IS NULL
		ORDER BY granted_at DESC LIMIT 1`, user).Scan(&current.PolicyVersion, &current.GrantedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHealthKitConsentChanged
	}
	if err != nil {
		return err
	}
	if current.PolicyVersion != grant.PolicyVersion || !current.GrantedAt.Equal(grant.GrantedAt) {
		return ErrHealthKitConsentChanged
	}
	return nil
}

// WriteHealthKitRecords stores the request's device and records under the same
// lock as consent withdrawal. A nil grant is reserved for explicit unenforced modes.
func (s *Store) WriteHealthKitRecords(ctx context.Context, user uuid.UUID, grant *model.Consent, req model.SyncRequest) (counts model.SyncCounts, sourceErr, err error) {
	err = s.healthKitTx(ctx, user, grant, func(tx pgx.Tx) error {
		if err := touchDevice(ctx, tx, user, req.DeviceID, req.DeviceName, req.AppVersion, req.OSVersion); err != nil {
			return fmt.Errorf("device: %w", err)
		}
		run := func(ctx context.Context, batch *pgx.Batch) (int, error) { return runBatch(ctx, tx, batch) }
		var err error
		if counts.Samples, err = upsertSamples(ctx, user, req.Samples, run); err != nil {
			return fmt.Errorf("samples: %w", err)
		}
		if counts.Sleep, err = upsertSleep(ctx, user, req.Sleep, run); err != nil {
			return fmt.Errorf("sleep: %w", err)
		}
		if counts.Workouts, err = upsertWorkouts(ctx, user, req.Workouts, run); err != nil {
			return fmt.Errorf("workouts: %w", err)
		}
		// Device source metadata remains best effort. A savepoint keeps a failure
		// from aborting storage of the health records.
		if sources := SourcesOf(req.Samples, req.Sleep, req.Workouts); len(sources) > 0 {
			sourceErr = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
				return upsertSources(ctx, savepoint, user, sources)
			})
		}
		return nil
	})
	return
}

// ApplyHealthKit commits deletions, queue updates and the device cursor together.
// Raw upserts already committed while the grant was active can be retried safely.
func (s *Store) ApplyHealthKit(ctx context.Context, user uuid.UUID, grant *model.Consent, device uuid.UUID, cursor *model.HealthKitCursor, a Apply) (Applied, error) {
	var out Applied
	del := dedupeDeletions(a.Deleted)
	err := s.healthKitTx(ctx, user, grant, func(tx pgx.Tx) error {
		var err error
		if len(a.Days) > 0 || len(a.Deleted) > 0 {
			if out, err = applyLocked(ctx, tx, user, a, del); err != nil {
				return err
			}
		}
		if cursor != nil {
			if err := setNutritionScope(ctx, tx, user); err != nil {
				return err
			}
			return recordHealthKitCursor(ctx, tx, user, device, *cursor)
		}
		return nil
	})
	if err != nil {
		return Applied{}, err
	}
	countFold(out)
	return out, nil
}

// RecordHealthKitSync writes the admin sync summary under the current grant.
func (s *Store) RecordHealthKitSync(ctx context.Context, user uuid.UUID, grant *model.Consent, device uuid.UUID, summary map[string]any) error {
	return s.healthKitTx(ctx, user, grant, func(tx pgx.Tx) error {
		return recordSync(ctx, tx, user, device, summary)
	})
}
