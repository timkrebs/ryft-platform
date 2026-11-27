package db

import (
	"context"
	"testing"
)

func TestMigrateDailyRestingKcal(t *testing.T) {
	pool := freshDB(t, true)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 69); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `WITH account AS (
		INSERT INTO users (id, name) VALUES (gen_random_uuid(), 'migration fixture') RETURNING id
	)
	INSERT INTO daily_scores (user_id, day, active_kcal) SELECT id, DATE '2026-11-26', 200 FROM account`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT resting_kcal IS NULL AND active_kcal = 200 FROM daily_scores WHERE day = '2026-11-26'`) {
		t.Fatal("the migration changed an existing day's totals")
	}
	if !scalar[bool](t, pool, `SELECT data_type = 'integer' AND is_nullable = 'YES' AND column_default IS NULL
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'daily_scores' AND column_name = 'resting_kcal'`) {
		t.Fatal("resting_kcal must be a nullable integer with no default")
	}
	for _, role := range []string{"ryft_scoring", "ryft_api", "ryft_ingest", "ryft_notify", "anon", "authenticated"} {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE"} {
			if !scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.daily_scores', 'resting_kcal', $2)
				= has_column_privilege($1, 'public.daily_scores', 'active_kcal', $2)`, role, privilege) {
				t.Errorf("%s privilege %s differs from active_kcal", role, privilege)
			}
		}
	}
}
