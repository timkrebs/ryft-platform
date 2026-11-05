package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0032: the api reads admin_accounts and never writes it; the audit gains an actor next to the
// fingerprint, and every write an image from before the migration makes still passes. Run as the
// service roles, not as the superuser.
func TestMigrateAdminAccounts(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateAdminAccounts(t, supabase) })
	}
}

func testMigrateAdminAccounts(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 31); err != nil {
		t.Fatal(err)
	}
	// A row written before 0032 (a key call: fingerprint, no actor).
	if _, err := pool.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, action, status) VALUES ('3fa1c2d9', 'GET /v1/admin/flags', 200)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("a second run is a no-op: %v", err)
	}
	if scalar[bool](t, pool, `SELECT NOT convalidated FROM pg_constraint WHERE conname = 'admin_audit_one_actor'`) {
		t.Error("the CHECK must be validated")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM admin_audit WHERE actor_user IS NULL AND key_fingerprint = '3fa1c2d9'`); n != 1 {
		t.Errorf("the row from before 0032 reads as a key call, got %d", n)
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	as := func(role string) *pgx.Conn { return asRole(t, dsn, role, testsupport.RolePassword(role)) }
	api := as("ryft_api")
	admin := "11111111-2222-4333-8444-555555555555"

	// The owner writes the allow-list (the SQL editor), as the design's step does.
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (user_id, note) VALUES ($1, 'operator') ON CONFLICT DO NOTHING`, admin); err != nil {
		t.Fatalf("owner insert: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (user_id, note) VALUES ($1, 'operator') ON CONFLICT DO NOTHING`, admin); err != nil {
		t.Fatalf("the owner's statement is repeatable: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (user_id, note) VALUES (gen_random_uuid(), $1)`, string(make([]byte, 201))); err == nil {
		t.Error("a note over 200 characters is refused")
	}

	// The api reads it, and does not write it.
	var n int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM admin_accounts WHERE user_id = $1`, admin).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api reads the row: %d %v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO admin_accounts (user_id) VALUES (gen_random_uuid())`,
		`UPDATE admin_accounts SET note = 'x'`,
		`DELETE FROM admin_accounts`,
		`TRUNCATE admin_accounts`,
	} {
		if _, err := api.Exec(ctx, q); err == nil {
			t.Errorf("the api must not run %q", q)
		}
	}
	// Nobody else sees it.
	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_notify"} {
		c := as(role)
		if _, err := c.Exec(ctx, `SELECT 1 FROM admin_accounts`); err == nil {
			t.Errorf("%s must not read admin_accounts", role)
		}
	}
	if supabase {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.admin_accounts'::regclass`) {
			t.Error("RLS must be on")
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			if scalar[bool](t, pool, `SELECT has_table_privilege($1, 'public.admin_accounts', 'SELECT')`, role) {
				t.Errorf("%s must not read admin_accounts", role)
			}
		}
	}

	// The audit: the old image's insert (fingerprint only) and the new one's (actor only) pass,
	// both or neither is refused. Through the api role, which writes it.
	if _, err := api.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, action, status) VALUES ('3fa1c2d9', 'GET /v1/admin/audit', 200)`); err != nil {
		t.Errorf("an insert as the image before 0032 makes it: %v", err)
	}
	if _, err := api.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, actor_user, action, status) VALUES ('', $1, 'GET /v1/admin/invites', 200)`, admin); err != nil {
		t.Errorf("an admin-token call: %v", err)
	}
	if _, err := api.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, actor_user, action, status) VALUES ('3fa1c2d9', $1, 'x', 200)`, admin); err == nil {
		t.Error("a fingerprint and a user together are refused")
	}
	if _, err := api.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, action, status) VALUES ('', 'x', 200)`); err == nil {
		t.Error("neither a fingerprint nor a user is refused")
	}
	// The image before 0032 reads every row into a string, user rows included.
	var fp string
	if err := api.QueryRow(ctx, `SELECT key_fingerprint FROM admin_audit WHERE actor_user = $1`, admin).Scan(&fp); err != nil || fp != "" {
		t.Errorf("a user row has an empty fingerprint, never NULL: %q %v", fp, err)
	}
	if _, err := api.Exec(ctx, `UPDATE admin_audit SET status = 500`); err == nil {
		t.Error("the audit stays append-only")
	}

	// Idempotent: the file again changes nothing.
	body, err := migrations.ReadFile("migrations/0032_admin_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0032 a second time: %v", err)
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conname = 'admin_audit_one_actor'`); got != 1 {
		t.Errorf("want one CHECK after a rerun, got %d", got)
	}
}
