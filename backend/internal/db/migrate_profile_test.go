package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigrateProfileGoalBody(t *testing.T) {
	pool := freshDB(t, true)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 67); err != nil {
		t.Fatal(err)
	}
	const user = "11111111-2222-4333-8444-555555555555"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, user); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT goal IS NULL AND sex IS NULL AND weight_manual_kg IS NULL AND weight_manual_at IS NULL
		FROM users WHERE id=$1`, user) {
		t.Fatal("profile fields should start null on an existing account")
	}
	for _, tt := range []struct {
		name, set, constraint string
	}{
		{"goal", `goal='other'`, "users_goal_check"},
		{"sex", `sex='other'`, "users_sex_check"},
		{"low weight", `weight_manual_kg=24.9, weight_manual_at=now()`, "users_weight_manual_kg_check"},
		{"high weight", `weight_manual_kg=350.1, weight_manual_at=now()`, "users_weight_manual_kg_check"},
		{"weight without time", `weight_manual_kg=64.5`, "users_weight_manual_pair_check"},
		{"time without weight", `weight_manual_at=now()`, "users_weight_manual_pair_check"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `UPDATE users SET `+tt.set+` WHERE id=$1`, user)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tt.constraint {
				t.Errorf("expected check %s: %v", tt.constraint, err)
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET goal='endurance', sex='unspecified',
		weight_manual_kg=350.0, weight_manual_at=now() WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0068_profile_goal_body.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0068 rerun: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT goal='endurance' AND sex='unspecified' AND weight_manual_kg=350.0
		AND weight_manual_at IS NOT NULL FROM users WHERE id=$1`, user) {
		t.Fatal("rerunning 0068 changed the profile")
	}
	for _, column := range []string{"goal", "sex", "weight_manual_kg", "weight_manual_at"} {
		for _, role := range []string{"ryft_api", "ryft_ingest", "ryft_scoring", "ryft_notify"} {
			got := scalar[bool](t, pool, `SELECT has_column_privilege($1::name, 'public.users', $2::text, 'SELECT')`, role, column)
			if got != (role != "ryft_notify") {
				t.Errorf("unexpected SELECT privilege for %s on users.%s", role, column)
			}
		}
	}
}
