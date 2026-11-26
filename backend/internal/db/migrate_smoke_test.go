package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0060 (D286): smoke_account, the database's own statement of which one account may be the production smoke.
// The api and ingest read it and write nothing; no other role sees it; at most one row, one account; the row
// goes with the users row; a rerun of the file changes nothing. Run as the service roles, through RLS on the
// Supabase shape.
func TestMigrateSmokeAccount(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateSmokeAccount(t, supabase) })
	}
}

func testMigrateSmokeAccount(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
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
	const smoke, other = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	for _, id := range []string{smoke, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}

	// Empty after the migration: every smoke request fails closed until the operator binds an account.
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 0 {
		t.Fatalf("a fresh table holds %d rows", n)
	}
	// The operator (the owner) binds.
	if _, err := pool.Exec(ctx, `INSERT INTO smoke_account (user_id) VALUES ($1)`, smoke); err != nil {
		t.Fatalf("the owner binds: %v", err)
	}

	// api and ingest read the binding (through RLS on the Supabase shape) and write nothing.
	for _, role := range []string{"ryft_api", "ryft_ingest"} {
		c := as(role)
		var got string
		if err := c.QueryRow(ctx, `SELECT user_id::text FROM smoke_account`).Scan(&got); err != nil || got != smoke {
			t.Errorf("%s reads the binding: %q %v", role, got, err)
		}
		for _, q := range []string{
			`INSERT INTO smoke_account (user_id) VALUES ('` + other + `')`,
			`UPDATE smoke_account SET user_id = '` + other + `'`,
			`DELETE FROM smoke_account`,
			`TRUNCATE smoke_account`,
		} {
			if _, err := c.Exec(ctx, q); err == nil {
				t.Errorf("%s must not run %q", role, q)
			}
		}
	}
	// Nobody else sees it.
	for _, role := range []string{"ryft_scoring", "ryft_notify"} {
		if _, err := as(role).Exec(ctx, `SELECT 1 FROM smoke_account`); err == nil {
			t.Errorf("%s must not read smoke_account", role)
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 1 {
		t.Errorf("a service changed the binding: %d rows", n)
	}

	// The shape, refused as the owner too: one row, one account, a real account.
	for name, q := range map[string]string{
		"a second account":    `INSERT INTO smoke_account (singleton, user_id) VALUES (true, '` + other + `')`,
		"the singleton false": `INSERT INTO smoke_account (singleton, user_id) VALUES (false, '` + other + `')`,
		"no such user":        `UPDATE smoke_account SET user_id = gen_random_uuid()`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}

	if supabase {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.smoke_account'::regclass`) {
			t.Error("RLS must be on for smoke_account")
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			if scalar[bool](t, pool, `SELECT has_table_privilege($1, 'public.smoke_account', 'SELECT')`, role) {
				t.Errorf("%s must not read smoke_account", role)
			}
		}
	}
	// One SELECT-only policy, for the two readers (inert on plain Postgres, where RLS is off).
	if got := scalar[string](t, pool, `SELECT coalesce(string_agg(cmd || ':' || array_to_string(roles, ','), ';'), '') FROM pg_policies
		WHERE schemaname = 'public' AND tablename = 'smoke_account'`); got != "SELECT:ryft_api,ryft_ingest" {
		t.Errorf("policies on smoke_account: %q", got)
	}

	// Account deletion (DELETE /v1/me, MergeUsers) deletes the users row as ryft_api, and the binding goes with it
	// although no service may delete it.
	if _, err := as("ryft_api").Exec(ctx, `DELETE FROM users WHERE id = $1`, smoke); err != nil {
		t.Fatalf("api deletes the users row: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 0 {
		t.Errorf("the cascade leaves %d rows", n)
	}

	// Idempotent: the file again changes nothing.
	body, err := migrations.ReadFile("migrations/0060_smoke_account.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0060 a second time: %v", err)
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'smoke_account'`); got != 1 {
		t.Errorf("want one policy after a rerun, got %d", got)
	}
}

// scripts/sql/bind_smoke_account.sql, the operator's one write: it binds only an account that exists in both
// users tables, signs in with email alone and is not tombstoned; a binding is replaced only on request. Run
// as the owner on the Supabase shape, with stand-ins for auth.users and auth.identities.
func TestBindSmokeAccountScript(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/sql/bind_smoke_account.sql")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	const idLit, rebindLit = "'<SMOKE_USER_ID>'", "v_rebind boolean := false"
	if strings.Count(script, idLit) != 1 || strings.Count(script, rebindLit) != 1 {
		t.Fatalf("the script's marked literals changed (%q, %q)", idLit, rebindLit)
	}
	pool := freshDB(t, true)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE auth.users (id uuid PRIMARY KEY)`,
		`CREATE TABLE auth.identities (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, provider text NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	const (
		good    = "00000000-0000-4000-8000-000000000001" // email only, in both tables
		google  = "00000000-0000-4000-8000-000000000002" // also a google identity
		apple   = "00000000-0000-4000-8000-000000000003" // apple only
		noPub   = "00000000-0000-4000-8000-000000000004" // in auth.users, not in public.users
		noAuth  = "00000000-0000-4000-8000-000000000005" // in public.users, not in auth.users
		deleted = "00000000-0000-4000-8000-000000000006" // tombstoned (the rows a restore brings back)
		second  = "00000000-0000-4000-8000-000000000007" // a second good account
		noIdent = "00000000-0000-4000-8000-000000000008" // no identity row at all
	)
	for _, id := range []string{good, google, apple, noPub, deleted, second, noIdent} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth.users (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{good, google, apple, noAuth, deleted, second, noIdent} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	for id, providers := range map[string][]string{good: {"email"}, google: {"email", "google"}, apple: {"apple"}, deleted: {"email"}, second: {"email"}} {
		for _, p := range providers {
			if _, err := pool.Exec(ctx, `INSERT INTO auth.identities (user_id, provider) VALUES ($1, $2)`, id, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deleted_users (id) VALUES ($1)`, deleted); err != nil {
		t.Fatal(err)
	}

	// run executes the file for id as the owner does: one simple-protocol query, one transaction. It returns the
	// id bound afterwards ("" for none) and the error.
	run := func(id string, rebind bool) (string, error) {
		s := strings.Replace(script, idLit, "'"+id+"'", 1)
		if rebind {
			s = strings.Replace(s, rebindLit, "v_rebind boolean := true", 1)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		_, runErr := conn.Exec(ctx, s)
		return scalar[string](t, pool, `SELECT coalesce((SELECT user_id::text FROM smoke_account), '')`), runErr
	}
	refused := func(name, id string, want string) {
		t.Helper()
		bound, err := run(id, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want it to contain %q", name, err, want)
		}
		if bound != "" {
			t.Errorf("%s: bound %q", name, bound)
		}
	}
	refused("the placeholder left in", "not-a-uuid", "invalid input syntax for type uuid")
	refused("the nil UUID", "00000000-0000-0000-0000-000000000000", "nil UUID")
	refused("not in auth.users", noAuth, "not in auth.users")
	refused("not in public.users", noPub, "not in public.users")
	refused("a google identity", google, "other than email (google)")
	refused("an apple identity", apple, "other than email (apple)")
	refused("a tombstone", deleted, "deleted_users")

	// An account with email alone binds, and the file's last statement shows exactly that row.
	if bound, err := run(good, false); err != nil || bound != good {
		t.Fatalf("binding the good account: %v, bound %q", err, bound)
	}
	rows, err := pool.Query(ctx, `SELECT user_id::text, bound_at FROM smoke_account`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || len(got) != 1 {
		t.Fatalf("one bound row expected: %v %v", got, err)
	}
	if bound, err := run(good, false); err == nil || bound != good {
		t.Errorf("binding again without rebind must be refused and keep the binding: %v, bound %q", err, bound)
	}
	if bound, err := run(second, false); err == nil || !strings.Contains(err.Error(), "already bound") || bound != good {
		t.Errorf("another account without rebind: %v, bound %q", err, bound)
	}
	// A refused rebind keeps the old binding (the checks run before the DELETE, and both are one transaction).
	if bound, err := run(google, true); err == nil || bound != good {
		t.Errorf("a refused rebind must keep the old binding: %v, bound %q", err, bound)
	}
	if bound, err := run(second, true); err != nil || bound != second {
		t.Errorf("rebind: %v, bound %q", err, bound)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 1 {
		t.Errorf("%d rows after a rebind", n)
	}
	// Deleting the bound account removes the binding; an account without any identity row has no other
	// sign-in either, so it binds.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 0 {
		t.Errorf("the binding outlived its account: %d rows", n)
	}
	if bound, err := run(noIdent, false); err != nil || bound != noIdent {
		t.Errorf("binding an account without any identity row: %v, bound %q", err, bound)
	}
}
