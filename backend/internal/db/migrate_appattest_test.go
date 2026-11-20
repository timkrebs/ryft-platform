package db

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0037: App Attest's two tables. Only the api reads and writes them (a challenge is never updated), the
// CHECKs hold the shapes the store relies on, the rows go with the users row (an image from before 0037
// deletes only that), and a rerun of the file changes nothing. Run as the service roles.
func TestMigrateAppAttest(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateAppAttest(t, supabase) })
	}
}

func testMigrateAppAttest(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 36); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	as := func(role string) *pgx.Conn { return asRole(t, dsn, role, testsupport.RolePassword(role)) }
	api := as("ryft_api")
	user := "11111111-2222-4333-8444-555555555555"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, user); err != nil {
		t.Fatal(err)
	}
	// Values of the right length, made at run time.
	b32 := func(c byte) []byte { return bytes.Repeat([]byte{c}, 32) }
	pub := append([]byte{4}, bytes.Repeat([]byte{7}, 64)...)

	// The api writes both, through RLS on the Supabase shape.
	if _, err := api.Exec(ctx, `INSERT INTO app_attest_keys (user_id, key_id, public_key, environment) VALUES ($1, $2, $3, 'production')`,
		user, b32(1), pub); err != nil {
		t.Fatalf("api stores a key: %v", err)
	}
	if _, err := api.Exec(ctx, `UPDATE app_attest_keys SET counter = 4294967295, last_used_at = now() WHERE user_id = $1`, user); err != nil {
		t.Fatalf("api advances the counter to the largest uint32: %v", err)
	}
	if _, err := api.Exec(ctx, `INSERT INTO app_attest_challenges (user_id, hash, expires_at) VALUES ($1, $2, now() + interval '5 minutes')`,
		user, b32(2)); err != nil {
		t.Fatalf("api opens a challenge: %v", err)
	}
	var n int
	if err := api.QueryRow(ctx, `WITH d AS (DELETE FROM app_attest_challenges WHERE user_id = $1 AND hash = $2 RETURNING 1)
		SELECT count(*) FROM d`, user, b32(2)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api consumes a challenge: %d %v", n, err)
	}
	if _, err := api.Exec(ctx, `UPDATE app_attest_challenges SET expires_at = now()`); err == nil {
		t.Error("a challenge is never updated")
	}
	for _, q := range []string{`TRUNCATE app_attest_keys`, `TRUNCATE app_attest_challenges`} {
		if _, err := api.Exec(ctx, q); err == nil {
			t.Errorf("the api must not run %q", q)
		}
	}

	// The shapes, refused as the owner too.
	for name, c := range map[string]struct {
		q    string
		args []any
	}{
		"key id of 31 bytes":   {`INSERT INTO app_attest_keys (user_id, key_id, public_key, environment) VALUES ($1, $2, $3, 'production')`, []any{user, b32(3)[:31], pub}},
		"public key of 64":     {`INSERT INTO app_attest_keys (user_id, key_id, public_key, environment) VALUES ($1, $2, $3, 'production')`, []any{user, b32(3), pub[:64]}},
		"compressed key":       {`INSERT INTO app_attest_keys (user_id, key_id, public_key, environment) VALUES ($1, $2, $3, 'production')`, []any{user, b32(3), append([]byte{3}, pub[1:]...)}},
		"development":          {`INSERT INTO app_attest_keys (user_id, key_id, public_key, environment) VALUES ($1, $2, $3, 'development')`, []any{user, b32(3), pub}},
		"counter above uint32": {`UPDATE app_attest_keys SET counter = 4294967296 WHERE user_id = $1`, []any{user}},
		"negative counter":     {`UPDATE app_attest_keys SET counter = -1 WHERE user_id = $1`, []any{user}},
		"hash of 33 bytes":     {`INSERT INTO app_attest_challenges (user_id, hash, expires_at) VALUES ($1, $2, now())`, []any{user, append(b32(4), 0)}},
		"no such user":         {`INSERT INTO app_attest_challenges (user_id, hash, expires_at) VALUES (gen_random_uuid(), $1, now())`, []any{b32(4)}},
	} {
		if _, err := pool.Exec(ctx, c.q, c.args...); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}

	// Nobody else sees them.
	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_notify"} {
		c := as(role)
		for _, tbl := range []string{"app_attest_keys", "app_attest_challenges"} {
			if _, err := c.Exec(ctx, `SELECT 1 FROM `+tbl); err == nil {
				t.Errorf("%s must not read %s", role, tbl)
			}
		}
	}
	if supabase {
		for _, tbl := range []string{"app_attest_keys", "app_attest_challenges"} {
			if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, "public."+tbl) {
				t.Errorf("RLS must be on for %s", tbl)
			}
			for _, role := range []string{"anon", "authenticated", "service_role"} {
				if scalar[bool](t, pool, `SELECT has_table_privilege($1, $2, 'SELECT')`, role, "public."+tbl) {
					t.Errorf("%s must not read %s", role, tbl)
				}
			}
		}
	}

	// The purges' indexes.
	for _, idx := range []string{"app_attest_keys_last_used", "app_attest_challenges_expires"} {
		if !scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1)`, idx) {
			t.Errorf("index %s missing", idx)
		}
	}

	// An image from before 0037 deletes the users row and nothing of these: the rows go with it.
	if _, err := api.Exec(ctx, `INSERT INTO app_attest_challenges (user_id, hash, expires_at) VALUES ($1, $2, now() + interval '5 minutes')`,
		user, b32(5)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatalf("api deletes the users row: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT (SELECT count(*) FROM app_attest_keys) + (SELECT count(*) FROM app_attest_challenges)`); n != 0 {
		t.Errorf("the cascade leaves %d rows", n)
	}

	// Idempotent: the file again changes nothing.
	body, err := migrations.ReadFile("migrations/0037_app_attest.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0037 a second time: %v", err)
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename LIKE 'app_attest_%'`); got != 2 {
		t.Errorf("want one policy per table after a rerun, got %d", got)
	}
}
