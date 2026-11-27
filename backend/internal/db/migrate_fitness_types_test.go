package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestMigrateFitnessTypesFlag(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0
		FROM feature_flags WHERE key = 'sync.fitness_types'`) {
		t.Fatal("sync.fitness_types must ship enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false, rollout_pct = 100 WHERE key = 'sync.fitness_types'`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0069_sync_fitness_types_flag.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("rerunning 0069: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT NOT enabled AND rollout_pct = 100 FROM feature_flags WHERE key = 'sync.fitness_types'`) {
		t.Error("rerunning 0069 overwrote the operator's setting")
	}
	// Ingest sees the calling account's membership through the function, never the table (as 0052).
	member, other := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = true, rollout_pct = 0, allow_user_ids = ARRAY[$1::uuid]
		WHERE key = 'sync.fitness_types'`, member); err != nil {
		t.Fatal(err)
	}
	asIngest := func(fn func(pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE ryft_ingest`); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	for _, tc := range []struct {
		user uuid.UUID
		want bool
	}{{member, true}, {other, false}} {
		var allowlisted bool
		var pct int
		if err := asIngest(func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT allowlisted, rollout_pct FROM public.sync_fitness_types_rollout($1)`, tc.user).Scan(&allowlisted, &pct)
		}); err != nil {
			t.Fatal(err)
		}
		if allowlisted != tc.want || pct != 0 {
			t.Errorf("sync_fitness_types_rollout(%v) = %v, %d; want %v, 0", tc.user, allowlisted, pct, tc.want)
		}
	}
	if err := asIngest(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT 1 FROM feature_flags LIMIT 1`)
		return err
	}); err == nil {
		t.Error("ingest can read feature_flags; it must see the flag only through sync_fitness_types_rollout")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false WHERE key = 'sync.fitness_types'`); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := asIngest(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM public.sync_fitness_types_rollout($1)`, member).Scan(&rows)
	}); err != nil || rows != 0 {
		t.Errorf("a disabled flag returned %d rows (err %v), want none", rows, err)
	}
}
