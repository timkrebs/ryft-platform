package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigrateDailyActiveMin(t *testing.T) {
	body, err := migrations.ReadFile("migrations/0071_daily_active_min.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nSET LOCAL lock_timeout = '5s';\n") {
		t.Fatal("0071 must set its lock timeout")
	}
	for _, tc := range []struct {
		name     string
		supabase bool
	}{{"plain", false}, {"supabase", true}} {
		t.Run(tc.name, func(t *testing.T) {
			pool := freshDB(t, tc.supabase)
			ctx := context.Background()
			if err := migrateTo(ctx, pool, quiet, 69); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, seed); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day) VALUES ($1, '2026-11-26')`, seed); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(ctx, pool, quiet); err != nil {
				t.Fatal(err)
			}
			if !scalar[bool](t, pool, `SELECT active_min IS NULL FROM daily_scores WHERE user_id=$1`, seed) {
				t.Error("rows scored before migration must keep active_min NULL")
			}
			if !scalar[bool](t, pool, `SELECT column_default IS NULL FROM information_schema.columns
				WHERE table_schema='public' AND table_name='daily_scores' AND column_name='active_min'`) {
				t.Error("active_min must have no default")
			}
			for _, tc := range []struct {
				role, privilege string
				want            bool
			}{{"ryft_scoring", "INSERT", true}, {"ryft_scoring", "UPDATE", true}, {"ryft_api", "SELECT", true}, {"ryft_notify", "SELECT", false}} {
				if got := scalar[bool](t, pool, `SELECT has_column_privilege($1, 'daily_scores', 'active_min', $2)`, tc.role, tc.privilege); got != tc.want {
					t.Errorf("%s %s on active_min = %v", tc.role, tc.privilege, got)
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE daily_scores SET active_min=0 WHERE user_id=$1`, seed); err != nil {
				t.Fatal(err)
			}
			_, err := pool.Exec(ctx, `UPDATE daily_scores SET active_min=-1 WHERE user_id=$1`, seed)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Errorf("active_min = -1 must fail the CHECK, got %v", err)
			}
		})
	}
}
