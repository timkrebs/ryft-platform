package db

import (
	"context"
	"testing"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0033: a code bound to an email, a revoke that keeps the row, and who redeemed it. Run as the service
// roles. What an image from before the migration writes still passes; the backfill reads
// users.beta_invite_code; the cascades keep the old image's DELETE and DeleteUser working.
func TestMigrateInvitesForEmail(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateInvitesForEmail(t, supabase) })
	}
}

func testMigrateInvitesForEmail(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 32); err != nil {
		t.Fatal(err)
	}
	const (
		redeemed = "RYFT7K3MQX9P" // used by anna and ben (ben has no beta_access_at: the backfill uses now())
		gone     = "RYFT2222AAAA" // redeemed by carl, then deleted before 0033
		anna     = "aaaaaaaa-0000-4000-8000-000000000001"
		ben      = "aaaaaaaa-0000-4000-8000-000000000002"
		carl     = "aaaaaaaa-0000-4000-8000-000000000003"
		dora     = "aaaaaaaa-0000-4000-8000-000000000004"
	)
	for _, q := range []string{
		`INSERT INTO beta_invite_codes (code, max_uses, uses) VALUES ('` + redeemed + `', 5, 2)`,
		`INSERT INTO users (id, beta_access_at, beta_invite_code) VALUES ('` + anna + `', '2026-10-01T10:00:00Z', '` + redeemed + `')`,
		`INSERT INTO users (id, beta_access_at, beta_invite_code) VALUES ('` + ben + `', NULL, '` + redeemed + `')`,
		`INSERT INTO users (id, beta_access_at, beta_invite_code) VALUES ('` + carl + `', now(), '` + gone + `')`, // the code is not in the table
		`INSERT INTO users (id, beta_access_at) VALUES ('` + dora + `', now())`,                                   // access without a code
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("a second run is a no-op: %v", err)
	}
	for _, c := range []string{"beta_invite_codes_email_form", "beta_invite_codes_created_by_form", "beta_invite_codes_bound_single_use"} {
		if scalar[bool](t, pool, `SELECT NOT convalidated FROM pg_constraint WHERE conname = $1`, c) {
			t.Errorf("%s must be validated", c)
		}
	}

	// The backfill: one row per account that holds a code that still exists, `at` the account's access time.
	if n := scalar[int](t, pool, `SELECT count(*) FROM beta_invite_uses`); n != 2 {
		t.Fatalf("backfill rows: %d, want 2 (anna, ben)", n)
	}
	if !scalar[bool](t, pool, `SELECT at = '2026-10-01T10:00:00Z' FROM beta_invite_uses WHERE user_id = $1`, anna) {
		t.Error("at is the account's beta_access_at")
	}
	if !scalar[bool](t, pool, `SELECT at > now() - interval '1 hour' FROM beta_invite_uses WHERE user_id = $1`, ben) {
		t.Error("without beta_access_at the backfill uses now()")
	}
	// Running the file again (the OPERATIONS rollback step) adds nothing.
	body, err := migrations.ReadFile("migrations/0033_invites_for_email.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0033 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM beta_invite_uses`); n != 2 {
		t.Errorf("a rerun of the backfill adds nothing: %d", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conname LIKE 'beta_invite_codes_%' AND conname IN
		('beta_invite_codes_email_form', 'beta_invite_codes_created_by_form', 'beta_invite_codes_bound_single_use')`); n != 3 {
		t.Errorf("want one of each CHECK after a rerun, got %d", n)
	}
	// Existing rows read as unbound, not revoked, created by the key.
	if !scalar[bool](t, pool, `SELECT email IS NULL AND revoked_at IS NULL AND created_by = 'key' FROM beta_invite_codes WHERE code = $1`, redeemed) {
		t.Error("a code from before 0033 is unbound, not revoked and created by 'key'")
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	api := asRole(t, dsn, "ryft_api", testsupport.RolePassword("ryft_api"))

	// An insert as the image before 0033 makes it (no new column named) passes, and the new columns take
	// their defaults.
	if _, err := api.Exec(ctx, `INSERT INTO beta_invite_codes (code, note, max_uses, expires_at) VALUES ('RYFT3333BBBB', 'old', 1, NULL)`); err != nil {
		t.Fatalf("an insert as the image before 0033 makes it: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT email IS NULL AND revoked_at IS NULL AND created_by = 'key' FROM beta_invite_codes WHERE code = 'RYFT3333BBBB'`) {
		t.Error("the old insert gets the defaults")
	}
	// The new image's inserts, bound and by an actor.
	okInserts := []string{
		`INSERT INTO beta_invite_codes (code, email, created_by) VALUES ('RYFT4444CCCC', 'anna@example.com', 'key:3fa1c2d9')`,
		`INSERT INTO beta_invite_codes (code, email, created_by) VALUES ('RYFT5555DDDD', 'a.b+c@sub.example.co.uk', 'user:1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081')`,
		`INSERT INTO beta_invite_codes (code, created_by) VALUES ('RYFT6666EEEE', 'key')`,
	}
	for _, q := range okInserts {
		if _, err := api.Exec(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', 'Anna@Example.com')`,           // not lower-cased
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', 'anna@@example.com')`,          // two @
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', 'anna@example')`,               // no dot in the domain
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', 'an na@example.com')`,          // a space
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', 'änna@example.com')`,           // not ASCII
		`INSERT INTO beta_invite_codes (code, email) VALUES ('RYFT7777FFFF', '@example.com')`,               // no name
		`INSERT INTO beta_invite_codes (code, email, max_uses) VALUES ('RYFT7777FFFF', 'a@example.com', 2)`, // a bound code is single use
		`INSERT INTO beta_invite_codes (code, created_by) VALUES ('RYFT7777FFFF', 'user')`,
		`INSERT INTO beta_invite_codes (code, created_by) VALUES ('RYFT7777FFFF', 'key:3FA1C2D9')`,
		`INSERT INTO beta_invite_codes (code, created_by) VALUES ('RYFT7777FFFF', 'someone')`,
	} {
		if _, err := api.Exec(ctx, q); err == nil {
			t.Errorf("must be refused: %s", q)
		}
	}
	// The purge sets email to NULL, which the CHECKs allow.
	if _, err := api.Exec(ctx, `UPDATE beta_invite_codes SET email = NULL WHERE code = 'RYFT4444CCCC'`); err != nil {
		t.Errorf("clearing the email: %v", err)
	}

	// Cascades: the old image hard-deletes a used code, and its DeleteUser knows nothing of the table.
	if _, err := api.Exec(ctx, `DELETE FROM beta_invite_codes WHERE code = $1`, redeemed); err != nil {
		t.Fatalf("an old-image DELETE of a used code: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM beta_invite_uses`); n != 0 {
		t.Errorf("the code's use rows go with it: %d left", n)
	}
	if _, err := api.Exec(ctx, `INSERT INTO beta_invite_uses (code, user_id) VALUES ('RYFT3333BBBB', $1)`, dora); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Exec(ctx, `INSERT INTO beta_invite_uses (code, user_id) VALUES ('RYFT3333BBBB', $1)`, dora); err == nil {
		t.Error("an account redeems a code at most once (the primary key)")
	}
	if _, err := api.Exec(ctx, `DELETE FROM users WHERE id = $1`, dora); err != nil {
		t.Fatalf("the old image's DeleteUser deletes the users row without knowing the table: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM beta_invite_uses`); n != 0 {
		t.Errorf("an account's use rows go with the account: %d left", n)
	}

	// Grants: the api writes the table; no other service sees it.
	for _, q := range []string{`SELECT 1 FROM beta_invite_uses`, `INSERT INTO beta_invite_uses (code, user_id) VALUES ('RYFT3333BBBB', gen_random_uuid())`} {
		for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_notify"} {
			c := asRole(t, dsn, role, testsupport.RolePassword(role))
			if _, err := c.Exec(ctx, q); err == nil {
				t.Errorf("%s must not run %q", role, q)
			}
		}
	}
	if supabase {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.beta_invite_uses'::regclass`) {
			t.Error("RLS must be on")
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			if scalar[bool](t, pool, `SELECT has_table_privilege($1, 'public.beta_invite_uses', 'SELECT')`, role) {
				t.Errorf("%s must not read beta_invite_uses", role)
			}
		}
	}
}
