package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// RecordHealthKitCursor advances the authorized device’s saved anchor.
func (s *Store) RecordHealthKitCursor(ctx context.Context, user, device uuid.UUID, c model.HealthKitCursor) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT public.record_healthkit_cursor($1,$2,$3,$4,$5)`, user, device, c.Stream, c.Sequence, c.Value)
		return err
	})
}
