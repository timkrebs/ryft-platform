package db

import (
	"context"
	"testing"
)

// 0038: a tombstoned id leaves every allow-list; a live account's id and an id that is in neither users nor
// deleted_users (allow-listed before the user's first call) stay, in their order; a list without a tombstoned
// id is not touched (updated_at included); and a rerun of the file changes nothing.
func TestMigrateFlagAllowListOrphans(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 37); err != nil {
		t.Fatal(err)
	}
	const (
		tomb    = "eeeeeeee-0000-0000-0000-000000000038"
		live    = "ffffffff-0000-0000-0000-000000000038"
		neither = "abababab-0000-0000-0000-000000000038"
	)
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + live + `')`,
		`INSERT INTO deleted_users (id) VALUES ('` + tomb + `')`,
		`UPDATE feature_flags SET updated_at = '2026-01-01T00:00:00Z'`,
		`UPDATE feature_flags SET allow_user_ids = '{` + tomb + `,` + live + `,` + neither + `,` + tomb + `}' WHERE key = 'today.redesign'`,
		`UPDATE feature_flags SET allow_user_ids = '{` + tomb + `}' WHERE key = 'coach.enabled'`,
		`UPDATE feature_flags SET allow_user_ids = '{` + neither + `,` + live + `}' WHERE key = 'push.enabled'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	list := func(key string) string {
		t.Helper()
		return scalar[string](t, pool, `SELECT allow_user_ids::text FROM feature_flags WHERE key = $1`, key)
	}
	for key, want := range map[string]string{
		"today.redesign": "{" + live + "," + neither + "}",
		"coach.enabled":  "{}",
		"push.enabled":   "{" + neither + "," + live + "}",
	} {
		if got := list(key); got != want {
			t.Errorf("%s: %s, want %s", key, got, want)
		}
	}
	touched := func(key string) bool {
		t.Helper()
		return scalar[bool](t, pool, `SELECT updated_at > '2026-01-01T00:00:00Z' FROM feature_flags WHERE key = $1`, key)
	}
	if !touched("today.redesign") || !touched("coach.enabled") {
		t.Error("a cleaned list moves updated_at")
	}
	if touched("push.enabled") || touched("train.enabled") {
		t.Error("a list without a tombstoned id must not be updated")
	}

	// Idempotent: the file applied again (a manual rerun, a restored schema_migrations) changes nothing.
	snapshot := func() string {
		t.Helper()
		return scalar[string](t, pool, `SELECT string_agg(key || '=' || allow_user_ids::text || '@' || updated_at::text, ';' ORDER BY key)
			FROM feature_flags`)
	}
	first := snapshot()
	body, err := migrations.ReadFile("migrations/0038_flag_allowlist_orphans.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0038 a second time: %v", err)
	}
	if again := snapshot(); again != first {
		t.Errorf("a rerun of 0038 changed the flags:\n%s\nwas\n%s", again, first)
	}
}
