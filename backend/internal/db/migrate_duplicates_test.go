package db

import (
	"context"
	"testing"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0031: duplicate_of is nullable (old rows stay NULL), refuses a self-reference, has no foreign key
// (merge moves workouts by user_id), and only scoring may write it. Run as the service roles, not as the
// superuser: a statement that works for the owner can still be refused for a role.
func TestMigrateWorkoutDuplicates(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateWorkoutDuplicates(t, supabase) })
	}
}

func testMigrateWorkoutDuplicates(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 28); err != nil {
		t.Fatal(err)
	}
	u := "dddddddd-0000-0000-0000-000000000031"
	w1, w2, w3 := "aaaaaaaa-0000-0000-0000-000000000031", "aaaaaaaa-0000-0000-0000-000000000032", "aaaaaaaa-0000-0000-0000-000000000033"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + u + `')`,
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES
			('` + w1 + `', '` + u + `', 'run', now(), now(), 'Watch'),
			('` + w2 + `', '` + u + `', 'run', now(), now(), 'Phone')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM workouts WHERE duplicate_of IS NULL`); n != 2 {
		t.Errorf("rows from before 0031 read duplicate_of NULL, got %d NULL rows of 2", n)
	}
	if scalar[bool](t, pool, `SELECT convalidated = false FROM pg_constraint WHERE conname = 'workouts_duplicate_of_not_self'`) {
		t.Error("the CHECK must be validated")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'workouts'::regclass AND contype = 'f' AND confrelid = 'workouts'::regclass`); n != 0 {
		t.Errorf("duplicate_of must have no self foreign key, got %d", n)
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	scoring := asRole(t, dsn, "ryft_scoring", testsupport.RolePassword("ryft_scoring"))
	api := asRole(t, dsn, "ryft_api", testsupport.RolePassword("ryft_api"))
	ingest := asRole(t, dsn, "ryft_ingest", testsupport.RolePassword("ryft_ingest"))

	// Scoring sets and clears it, pointing at a workout that need not exist (no FK).
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, u, w2, w1); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring sets duplicate_of: %v %v", ct, err)
	}
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, u, w2, w3); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring points at a workout that is gone (no FK): %v %v", ct, err)
	}
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1 AND id = $2`, u, w2); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring clears duplicate_of: %v %v", ct, err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = id WHERE user_id = $1 AND id = $2`, u, w2); err == nil {
		t.Error("duplicate_of = id must be refused by the CHECK")
	}
	// Scoring's other columns stay as they were (0009, 0011).
	for _, q := range []string{
		`UPDATE workouts SET source = 'x' WHERE user_id = $1`,
		`UPDATE workouts SET start_at = now() WHERE user_id = $1`,
		`UPDATE workouts SET activity = 'swim' WHERE user_id = $1`,
		`UPDATE workouts SET user_id = user_id WHERE user_id = $1`,
	} {
		if _, err := scoring.Exec(ctx, q, u); err == nil {
			t.Errorf("scoring must not run %q", q)
		}
	}
	// The api reads it and cannot write it; ingest reads it too (and see below for its UPDATE).
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $2 WHERE user_id = $1 AND id = $3`, u, w1, w2); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM workouts WHERE duplicate_of = $1`, w1); got != 1 {
		t.Fatalf("setup: %d", got)
	}
	var seen int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, u).Scan(&seen); err != nil || seen != 1 {
		t.Errorf("api reads duplicate_of: %d %v", seen, err)
	}
	if _, err := api.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1`, u); err == nil {
		t.Error("api must not write duplicate_of")
	}
	if _, err := api.Exec(ctx, `UPDATE workouts SET user_id = user_id WHERE user_id = $1`, u); err != nil {
		t.Errorf("api keeps UPDATE (user_id) for the merge: %v", err)
	}
	// Ingest holds table-level UPDATE on workouts since 0009, which covers the new column; Postgres cannot
	// narrow that without revoking and re-granting columns (not additive). Its code never names the
	// column; this pins the fact so a later narrowing is a conscious change.
	if _, err := ingest.Exec(ctx, `UPDATE workouts SET duplicate_of = duplicate_of WHERE user_id = $1`, u); err != nil {
		t.Errorf("ingest's table-level UPDATE covers duplicate_of: %v", err)
	}
	if err := ingest.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, u).Scan(&seen); err != nil || seen != 1 {
		t.Errorf("ingest reads duplicate_of: %d %v", seen, err)
	}
	// Ingest's insert and upsert of a workout (no duplicate_of in the column list) are unaffected.
	if _, err := ingest.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		VALUES ($1, $2, 'run', now(), now(), 'Phone') ON CONFLICT (user_id, id) DO UPDATE SET end_at = EXCLUDED.end_at`, w3, u); err != nil {
		t.Errorf("ingest workout upsert: %v", err)
	}
	// Ingest's fold deletes a duplicate row.
	if ct, err := ingest.Exec(ctx, `DELETE FROM workouts WHERE user_id = $1 AND id = $2`, u, w3); err != nil || ct.RowsAffected() != 1 {
		t.Errorf("ingest fold delete: %v %v", ct, err)
	}

	// Catalog pin of the grants, for every role.
	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"ryft_scoring", "UPDATE", true}, {"ryft_scoring", "SELECT", true}, {"ryft_scoring", "INSERT", false},
		{"ryft_api", "SELECT", true}, {"ryft_api", "UPDATE", false}, {"ryft_api", "INSERT", false},
		{"ryft_ingest", "SELECT", true}, {"ryft_ingest", "UPDATE", true}, {"ryft_ingest", "INSERT", true},
		{"ryft_notify", "SELECT", false}, {"ryft_notify", "UPDATE", false},
	} {
		got := scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.workouts', 'duplicate_of', $2)`, c.role, c.priv)
		if got != c.want {
			t.Errorf("%s %s workouts.duplicate_of: %v, want %v", c.role, c.priv, got, c.want)
		}
	}

	// Idempotent: the file again changes nothing.
	body, err := migrations.ReadFile("migrations/0031_workout_duplicates.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0031 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conname = 'workouts_duplicate_of_not_self'`); n != 1 {
		t.Errorf("want one CHECK after a rerun, got %d", n)
	}
}
