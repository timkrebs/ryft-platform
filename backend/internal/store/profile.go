package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/model"
)

// ErrProfileConsentRequired means the current policy grant does not cover a profile body write.
var ErrProfileConsentRequired = errors.New("profile consent required")

func requireProfileConsent(ctx context.Context, tx pgx.Tx, user uuid.UUID, namesProfileBody func(string) bool) error {
	// GrantConsent and RevokeConsent take this lock. At Read Committed the
	// SELECT sees any consent change that committed while we waited for it.
	if _, err := tx.Exec(ctx, lockApply, user); err != nil {
		return err
	}
	var version string
	err := tx.QueryRow(ctx, `SELECT policy_version FROM consents
		WHERE user_id=$1 AND kind='health_processing' AND revoked_at IS NULL
		ORDER BY granted_at DESC LIMIT 1`, user).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !namesProfileBody(version) {
		return ErrProfileConsentRequired
	}
	return err
}

// FitnessTypesEnabled evaluates the account's sync.fitness_types flag. A missing row is off.
func (s *Store) FitnessTypesEnabled(ctx context.Context, user uuid.UUID) (bool, error) {
	f, err := scanFlag(s.pool.QueryRow(ctx, `SELECT `+flagColumns+` FROM feature_flags WHERE key=$1`, model.FlagSyncFitnessTypes))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && flags.On(f, user), err
}

// HealthKitWeighIn returns the newest HealthKit body mass and its measurement time.
// A nil weight means the account has no eligible sample.
func (s *Store) HealthKitWeighIn(ctx context.Context, user uuid.UUID) (*float64, time.Time, error) {
	var kg float64
	var measuredAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT value, start_at FROM samples
		WHERE user_id=$1 AND type='body_mass' AND origin='healthkit' AND start_at <= now()
		ORDER BY start_at DESC, id DESC LIMIT 1`, user).Scan(&kg, &measuredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return &kg, measuredAt.UTC(), nil
}
