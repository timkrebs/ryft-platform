package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 0028: sessions already stored read as 'app' without a rewrite, the new columns are closed
// by their CHECKs (also the one left NOT VALID), the flag ships dark, and the file can run twice.
func TestMigrateTrainImport(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 27); err != nil {
		t.Fatal(err)
	}
	user, sess, ex, set := "dddddddd-0000-0000-0000-000000000028", "dddddddd-0000-0000-0000-0000000000a1",
		"dddddddd-0000-0000-0000-0000000000b1", "dddddddd-0000-0000-0000-0000000000c1"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + user + `')`,
		`INSERT INTO sessions (user_id, id, started_at, status) VALUES ('` + user + `', '` + sess + `', now(), 'completed')`,
		`INSERT INTO exercises (id, slug, name_en, name_de, kind, primary_muscles, load_type)
		   VALUES ('` + ex + `', 'x-28', 'X', 'X', 'bodyweight', '{chest}', 'bodyweight')`,
		`INSERT INTO session_exercises (user_id, id, session_id, exercise_id, position) VALUES ('` + user + `', '` + ex + `', '` + sess + `', '` + ex + `', 0)`,
		`INSERT INTO session_sets (user_id, id, session_exercise_id, set_index, updated_at) VALUES ('` + user + `', '` + set + `', '` + ex + `', 0, now())`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT origin = 'app' AND import_source IS NULL FROM sessions WHERE id = $1`, sess) ||
		!scalar[bool](t, pool, `SELECT assist_kg IS NULL FROM session_sets WHERE id = $1`, set) ||
		!scalar[bool](t, pool, `SELECT import_lease_until IS NULL FROM users WHERE id = $1`, user) {
		t.Fatal("rows from before 0028: origin app, no import source, no assist, no lease")
	}
	// The sessions constraints are validated; the session_sets one is left NOT VALID, and enforced.
	for name, want := range map[string]bool{"sessions_origin_check": true, "sessions_import_source_check": true, "session_sets_assist_kg_check": false} {
		if got := scalar[bool](t, pool, `SELECT convalidated FROM pg_constraint WHERE conname = $1`, name); got != want {
			t.Errorf("%s validated = %v, want %v", name, got, want)
		}
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 FROM feature_flags WHERE key = 'train.import'`) {
		t.Error("train.import ships enabled at 0 %")
	}
	for _, c := range []struct{ name, q string }{
		{"origin outside the enum", `UPDATE sessions SET origin = 'sync', import_source = 'x'`},
		{"import without a source", `UPDATE sessions SET origin = 'import'`},
		{"source on an app session", `UPDATE sessions SET import_source = 'alpha_progression'`},
		{"source that is not a slug", `UPDATE sessions SET origin = 'import', import_source = 'Alpha Progression!'`},
		{"assist above 200", `UPDATE session_sets SET assist_kg = 200.5`},
		{"assist below 0", `UPDATE session_sets SET assist_kg = -1`},
	} {
		if _, err := pool.Exec(ctx, c.q); err == nil {
			t.Errorf("%s must be refused", c.name)
		}
	}
	for _, q := range []string{
		`UPDATE sessions SET origin = 'import', import_source = 'alpha_progression'`,
		`UPDATE sessions SET origin = 'app', import_source = NULL`,
		`UPDATE session_sets SET assist_kg = 0`, `UPDATE session_sets SET assist_kg = 200`, `UPDATE session_sets SET assist_kg = NULL`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	// Idempotent: the file applied again (a manual rerun, a restored schema_migrations) changes nothing.
	body, err := migrations.ReadFile("migrations/0028_train_import.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0028 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conname IN
		('sessions_origin_check', 'sessions_import_source_check', 'session_sets_assist_kg_check')`); n != 3 {
		t.Errorf("want 3 constraints after a rerun, got %d", n)
	}
}

// 0028 gives up on a held sessions table after its lock_timeout instead of queueing every reader
// behind its ALTER, and leaves nothing behind.
func TestMigrateTrainImportGivesUpOnALock(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 27); err != nil {
		t.Fatal(err)
	}
	reader, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx)
	if _, err := reader.Exec(ctx, `SELECT count(*) FROM sessions`); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = Migrate(wait, pool, quiet)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("want a lock timeout, got %v", err)
	}
	if scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'sessions' AND column_name = 'origin')`) ||
		scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 28)`) {
		t.Fatal("a failed 0028 must leave nothing behind")
	}
	if err := reader.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("once the table is free: %v", err)
	}
}
