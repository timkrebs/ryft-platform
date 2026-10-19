package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

const flagColumns = `key, enabled, rollout_pct, allow_user_ids, updated_at`

func scanFlag(r pgx.Row) (model.FeatureFlag, error) {
	var f model.FeatureFlag
	err := r.Scan(&f.Key, &f.Enabled, &f.RolloutPct, &f.AllowUserIDs, &f.UpdatedAt)
	if f.AllowUserIDs == nil {
		f.AllowUserIDs = []uuid.UUID{}
	}
	return f, err
}

// Flags returns every feature flag, by key.
func (s *Store) Flags(ctx context.Context) ([]model.FeatureFlag, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+flagColumns+` FROM feature_flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.FeatureFlag, error) { return scanFlag(r) })
}

// PutFlag creates or replaces a flag; created says whether it was new.
func (s *Store) PutFlag(ctx context.Context, f model.FeatureFlag) (model.FeatureFlag, bool, error) {
	if f.AllowUserIDs == nil {
		f.AllowUserIDs = []uuid.UUID{}
	}
	var created bool
	row := s.pool.QueryRow(ctx, `INSERT INTO feature_flags (key, enabled, rollout_pct, allow_user_ids, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (key) DO UPDATE SET enabled=EXCLUDED.enabled, rollout_pct=EXCLUDED.rollout_pct,
		  allow_user_ids=EXCLUDED.allow_user_ids, updated_at=now()
		RETURNING `+flagColumns+`, (xmax = 0)`, f.Key, f.Enabled, f.RolloutPct, f.AllowUserIDs)
	var out model.FeatureFlag
	err := row.Scan(&out.Key, &out.Enabled, &out.RolloutPct, &out.AllowUserIDs, &out.UpdatedAt, &created)
	return out, created, err
}

// DeleteFlag removes a flag; ErrNotFound when there is none.
func (s *Store) DeleteFlag(ctx context.Context, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM feature_flags WHERE key=$1`, key)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
