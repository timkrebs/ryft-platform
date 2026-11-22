package db

import (
	"context"
	"strings"
	"testing"
)

// 0039: users.push_devices exists, NOT NULL, '{}' on the rows from before and on new ones; the claim's two
// lookups have their indexes (GIN on the list, b-tree on push_tokens.device_id) and use them; notify can't read
// the column; a rerun of the file keeps the lists and adds no second index (D251).
func TestMigratePushDevices(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 38); err != nil {
		t.Fatal(err)
	}
	old, other := "eeeeeeee-0000-0000-0000-000000000039", "ffffffff-0000-0000-0000-000000000039"
	device := "eeeeeeee-0000-0000-0000-0000000000d1"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, old); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT is_nullable = 'NO' AND udt_name = '_uuid' FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'push_devices'`) {
		t.Fatal("users.push_devices: want uuid[] NOT NULL")
	}
	if !scalar[bool](t, pool, `SELECT push_devices = '{}'::uuid[] FROM users WHERE id = $1`, old) {
		t.Error("a row from before 0039 reads an empty list")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, other); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT push_devices = '{}'::uuid[] FROM users WHERE id = $1`, other) {
		t.Error("a new row defaults to an empty list")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET push_devices = NULL WHERE id = $1`, other); err == nil {
		t.Error("push_devices must refuse NULL")
	}
	for name, want := range map[string]string{
		"public.users_push_devices":    "users gin push_devices",
		"public.push_tokens_device_id": "push_tokens btree device_id",
	} {
		got := scalar[string](t, pool, `SELECT COALESCE((
			SELECT t.relname || ' ' || am.amname || ' ' || pg_get_indexdef(x.indexrelid, 1, true)
			FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid JOIN pg_class t ON t.oid = x.indrelid
			  JOIN pg_am am ON am.oid = i.relam
			WHERE x.indexrelid = to_regclass($1) AND x.indnatts = 1 AND NOT x.indisunique AND x.indpred IS NULL), 'missing')`, name)
		if got != want {
			t.Errorf("%s: %s, want %s (one column, not unique, not partial)", name, got, want)
		}
	}
	// The claim's statements as the api runs them (docs/work/2026-11-22-notifications-nt/plan.md, T1): with
	// sequential and plain index scans off, only bitmap scans on an index whose operator matches the WHERE are
	// left (no unqualified walk of a primary key), and the new index is the cheapest of them, so the plan names
	// it unless its operators don't fit the statement.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) // read-only, rolled back
	for _, q := range []string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_indexscan = off`, `SET LOCAL enable_indexonlyscan = off`} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for index, q := range map[string]string{
		"users_push_devices": `EXPLAIN UPDATE users SET push_devices = array_remove(push_devices, '` + device + `'::uuid)
			WHERE push_devices @> ARRAY['` + device + `']::uuid[] AND id <> '` + other + `'`,
		"push_tokens_device_id": `EXPLAIN DELETE FROM push_tokens WHERE device_id = '` + device + `' AND user_id <> '` + other + `'`,
	} {
		rows, err := tx.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if p := strings.Join(plan, "\n"); !strings.Contains(p, index) {
			t.Errorf("the claim must be able to use %s:\n%s", index, p)
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Who sees the column: the api reads and writes it, ingest and scoring read it through userColumns
	// (table-level SELECT, 0009), scoring writes nothing of it, notify's column grant (0015) leaves it out.
	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"ryft_api", "SELECT", true}, {"ryft_api", "UPDATE", true},
		{"ryft_ingest", "SELECT", true}, {"ryft_scoring", "SELECT", true}, {"ryft_scoring", "UPDATE", false},
		{"ryft_notify", "SELECT", false}, {"ryft_notify", "UPDATE", false},
	} {
		if got := scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.users', 'push_devices', $2)`, c.role, c.priv); got != c.want {
			t.Errorf("%s %s users.push_devices: %v, want %v", c.role, c.priv, got, c.want)
		}
	}
	// Idempotent: the file applied again keeps the lists and adds nothing.
	if _, err := pool.Exec(ctx, `UPDATE users SET push_devices = ARRAY['`+device+`']::uuid[] WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0039_push_devices.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0039 a second time: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT push_devices = ARRAY['`+device+`']::uuid[] FROM users WHERE id = $1`, old) {
		t.Error("a rerun of 0039 changed a stored list")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public'
		AND ((tablename = 'users' AND indexdef LIKE '%(push_devices)') OR (tablename = 'push_tokens' AND indexdef LIKE '%(device_id)'))`); n != 2 {
		t.Errorf("want 2 indexes on push_devices and push_tokens.device_id after a rerun, got %d", n)
	}
}

// 0040: notifications.reminders ships dark (enabled, 0 %, no allow-list), and a rerun of the file keeps
// what the operator set since (D252).
func TestMigrateRemindersFlag(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0
		FROM feature_flags WHERE key = 'notifications.reminders'`) {
		t.Fatal("notifications.reminders ships enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false, rollout_pct = 100 WHERE key = 'notifications.reminders'`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0040_reminders_flag.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0040 a second time: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT NOT enabled AND rollout_pct = 100 FROM feature_flags WHERE key = 'notifications.reminders'`) {
		t.Error("a rerun of 0040 overwrote the operator's setting")
	}
}
