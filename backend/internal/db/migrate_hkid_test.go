package db

import (
	"context"
	"testing"
)

// 0029/0030: hk_id is nullable (rows stored before stay NULL, nothing is rewritten), unique per user
// among the non-NULL ones on both tables, free to repeat as NULL, scoped to the user, and the two
// files can run twice. Column grants: scoring and the api cannot write the new column's value.
func TestMigrateHKIDs(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 28); err != nil {
		t.Fatal(err)
	}
	u1, u2 := "dddddddd-0000-0000-0000-000000000029", "dddddddd-0000-0000-0000-000000000030"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + u1 + `'), ('` + u2 + `')`,
		`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source) VALUES ('` + u1 + `', 'steps', now(), now(), 1, 'count', 'a')`,
		`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source) VALUES ('` + u1 + `', now(), now(), 'core', 'a')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"samples", "sleep_segments"} {
		if n := scalar[int](t, pool, `SELECT count(*) FROM `+tbl+` WHERE hk_id IS NULL`); n != 1 {
			t.Errorf("%s: the row from before 0029 reads hk_id NULL, got %d NULL rows", tbl, n)
		}
	}
	h := "eeeeeeee-0000-0000-0000-000000000001"
	ins := map[string]string{
		"samples": `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, hk_id)
			VALUES ($1, 'hr', now() + ($2 || ' s')::interval, now(), 1, 'bpm', 'a', $3)`,
		"sleep_segments": `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id)
			VALUES ($1, now() + ($2 || ' s')::interval, now(), 'core', 'a', $3)`,
	}
	for tbl, q := range ins {
		if _, err := pool.Exec(ctx, q, u1, "1", h); err != nil {
			t.Fatalf("%s first: %v", tbl, err)
		}
		if _, err := pool.Exec(ctx, q, u1, "2", h); err == nil {
			t.Errorf("%s: the same hk_id twice for one user must be refused", tbl)
		}
		if _, err := pool.Exec(ctx, q, u2, "3", h); err != nil {
			t.Errorf("%s: another user may hold the same hk_id: %v", tbl, err)
		}
		if _, err := pool.Exec(ctx, q, u1, "4", nil); err != nil {
			t.Errorf("%s: NULL hk_id: %v", tbl, err)
		}
		if _, err := pool.Exec(ctx, q, u1, "5", nil); err != nil {
			t.Errorf("%s: NULL repeats freely: %v", tbl, err)
		}
		// ON CONFLICT DO NOTHING without a target absorbs the hk_id clash (the sync's second statement).
		if ct, err := pool.Exec(ctx, q+` ON CONFLICT DO NOTHING`, u1, "6", h); err != nil || ct.RowsAffected() != 0 {
			t.Errorf("%s: target-less ON CONFLICT must absorb the clash: %v %v", tbl, ct, err)
		}
	}
	for _, idx := range []string{"samples_user_hk", "sleep_segments_user_hk"} {
		if !scalar[bool](t, pool, `SELECT indisunique AND indpred IS NOT NULL FROM pg_index WHERE indexrelid = $1::regclass`, idx) {
			t.Errorf("%s: want a partial unique index", idx)
		}
	}
	// Grants: ingest writes hk_id and deletes; scoring never writes the raw tables; the api moves rows
	// by user_id only (0024) and deletes.
	for _, c := range []struct {
		role, table, col, priv string
		want                   bool
	}{
		{"ryft_ingest", "samples", "hk_id", "INSERT", true}, {"ryft_ingest", "samples", "hk_id", "UPDATE", true},
		{"ryft_ingest", "sleep_segments", "hk_id", "UPDATE", true},
		{"ryft_scoring", "samples", "hk_id", "SELECT", true}, {"ryft_scoring", "samples", "hk_id", "UPDATE", false},
		{"ryft_scoring", "sleep_segments", "hk_id", "UPDATE", false},
		{"ryft_api", "samples", "hk_id", "UPDATE", false}, {"ryft_api", "sleep_segments", "hk_id", "UPDATE", false},
		{"ryft_api", "samples", "user_id", "UPDATE", true},
		{"ryft_notify", "samples", "hk_id", "SELECT", false},
	} {
		if got := scalar[bool](t, pool, `SELECT has_column_privilege($1, $2, $3, $4)`, c.role, "public."+c.table, c.col, c.priv); got != c.want {
			t.Errorf("%s %s %s.%s: %v, want %v", c.role, c.priv, c.table, c.col, got, c.want)
		}
	}
	// Idempotent: both files again change nothing.
	for _, f := range []string{"migrations/0029_hk_ids.sql", "migrations/0030_hk_ids_index.sql"} {
		body, err := migrations.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("%s a second time: %v", f, err)
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname IN ('samples_user_hk', 'sleep_segments_user_hk')`); n != 2 {
		t.Errorf("want 2 indexes after a rerun, got %d", n)
	}
}
