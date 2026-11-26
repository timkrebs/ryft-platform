package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

// smokeEmail is the address both scripts expect the smoke account to hold in auth.users (each script carries it as
// v_email, because each file is pasted alone; the two tests below pin both to this one value).
const smokeEmail = "smoke@ryftapp.de"

// retiredSmokeEmail is the address #250 first named (a lookalike account created on 2026-11-26 and never used). An
// account that holds it is not the smoke account.
const retiredSmokeEmail = "smoke@ryftlab.de"

// smokeScriptDB is a migrated database on the Supabase shape with stand-ins for auth.users (with the email
// column, and, like Supabase, no unique constraint that would keep a second row from holding the same
// address: single sign-on users may) and auth.identities.
func smokeScriptDB(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	pool := freshDB(t, true)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE auth.users (id uuid PRIMARY KEY, email text)`,
		`CREATE TABLE auth.identities (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, provider text NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, pool
}

// scripts/sql/bind_smoke_account.sql, the operator's one write: it binds only the one account that holds the
// smoke email in auth.users and exists in both users tables, signs in with email alone and is not tombstoned;
// a binding is replaced only on request. Run as the owner on the Supabase shape, with stand-ins for
// auth.users and auth.identities.
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
	if !strings.Contains(script, "v_email  constant text := '"+smokeEmail+"'") {
		t.Fatalf("the script's smoke email is not %s", smokeEmail)
	}
	ctx, pool := smokeScriptDB(t)
	const (
		good    = "00000000-0000-4000-8000-000000000001" // email only, in both tables
		google  = "00000000-0000-4000-8000-000000000002" // also a google identity
		apple   = "00000000-0000-4000-8000-000000000003" // apple only
		noPub   = "00000000-0000-4000-8000-000000000004" // in auth.users, not in public.users
		noAuth  = "00000000-0000-4000-8000-000000000005" // in public.users, not in auth.users
		deleted = "00000000-0000-4000-8000-000000000006" // tombstoned (the rows a restore brings back)
		second  = "00000000-0000-4000-8000-000000000007" // a second good account
		noIdent = "00000000-0000-4000-8000-000000000008" // no identity row at all
		sso     = "00000000-0000-4000-8000-000000000009" // a single sign-on user that may hold the same address
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

	// holder gives the smoke email, spelled as given, to exactly the one account id ("" for nobody): every
	// other row gets an address of its own. This is what the operator does when the smoke account changes.
	holder := func(id, spelled string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE auth.users SET email = id::text || '@example.test'`); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			return
		}
		if _, err := pool.Exec(ctx, `UPDATE auth.users SET email = $2 WHERE id = $1`, id, spelled); err != nil {
			t.Fatal(err)
		}
	}
	// run executes the file for id as the owner does: one simple-protocol query, one transaction. It returns the
	// id bound afterwards ("" for none), the command tag of the file's last statement and the error.
	run := func(id string, rebind bool) (string, string, error) {
		s := strings.Replace(script, idLit, "'"+id+"'", 1)
		if rebind {
			s = strings.Replace(s, rebindLit, "v_rebind boolean := true", 1)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		tag, runErr := conn.Exec(ctx, s)
		return scalar[string](t, pool, `SELECT coalesce((SELECT user_id::text FROM smoke_account), '')`), tag.String(), runErr
	}
	refused := func(name, id string, want string) {
		t.Helper()
		bound, _, err := run(id, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want it to contain %q", name, err, want)
		}
		if bound != "" {
			t.Errorf("%s: bound %q", name, bound)
		}
	}

	// Each account-specific check is tested with the smoke email on that very account, so that the email check
	// passes and the check named is the one that refuses.
	holder(good, smokeEmail)
	refused("the placeholder left in", "not-a-uuid", "invalid input syntax for type uuid")
	refused("the nil UUID", "00000000-0000-0000-0000-000000000000", "nil UUID")
	holder(noAuth, smokeEmail)
	refused("not in auth.users", noAuth, "not in auth.users")
	holder(noPub, smokeEmail)
	refused("not in public.users", noPub, "not in public.users")
	holder(google, smokeEmail)
	refused("a google identity", google, "other than email (google)")
	holder(apple, smokeEmail)
	refused("an apple identity", apple, "other than email (apple)")
	holder(deleted, smokeEmail)
	refused("a tombstone", deleted, "deleted_users")

	// The email check: the pasted UUID must be the one account that holds the smoke email.
	holder(second, smokeEmail)
	refused("a valid email-only user that is not the smoke account", good, "is not the account with the email "+smokeEmail+" (that is "+second+")")
	holder("", "")
	refused("no row with the smoke email", good, "no auth.users row has the email "+smokeEmail)
	// An otherwise valid account whose email is not the configured smoke email is refused, even when that email is
	// the retired lookalike (the 2026-11-26 failure, the other way round) or the right one with a stray character.
	for _, other := range []string{retiredSmokeEmail, "smoke@ryftapp.de.", "xsmoke@ryftapp.de", " smoke@ryftapp.de"} {
		holder(good, other)
		refused("an account holding "+other, good, "no auth.users row has the email "+smokeEmail)
	}
	holder(second, retiredSmokeEmail)
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET email = $2 WHERE id = $1`, good, smokeEmail); err != nil {
		t.Fatal(err)
	}
	refused("the retired lookalike while the real account holds the email", second, "is not the account with the email "+smokeEmail+" (that is "+good+")")
	holder(good, smokeEmail)
	if _, err := pool.Exec(ctx, `INSERT INTO auth.users (id, email) VALUES ($1, $2)`, sso, "SMOKE@RyftApp.de"); err != nil {
		t.Fatal(err)
	}
	refused("the real smoke account while a second row holds the address", good, "2 auth.users rows have the email "+smokeEmail)
	refused("the second row of the address", sso, "2 auth.users rows have the email "+smokeEmail)
	if _, err := pool.Exec(ctx, `DELETE FROM auth.users WHERE id = $1`, sso); err != nil {
		t.Fatal(err)
	}

	// The match ignores case: the address as the user typed it at sign-up binds.
	holder(good, "Smoke@RyftApp.DE")
	if bound, _, err := run(good, false); err != nil || bound != good {
		t.Fatalf("binding with the email in another case: %v, bound %q", err, bound)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM smoke_account`); err != nil {
		t.Fatal(err)
	}

	// The account with the smoke email and email alone binds, and the file's last statement shows exactly that
	// row: one row, the bound user_id, a bound_at that is now.
	holder(good, smokeEmail)
	bound, tag, err := run(good, false)
	if err != nil || bound != good {
		t.Fatalf("binding the good account: %v, bound %q", err, bound)
	}
	if tag != "SELECT 1" {
		t.Errorf("the last statement must return one row, its tag is %q", tag)
	}
	var (
		gotID     string
		boundAt   time.Time
		boundNow  bool
		rowsBound int
	)
	if err := pool.QueryRow(ctx, `SELECT user_id::text, bound_at, bound_at <= now() AND now() - bound_at < interval '1 minute',
		(SELECT count(*) FROM smoke_account) FROM smoke_account`).Scan(&gotID, &boundAt, &boundNow, &rowsBound); err != nil {
		t.Fatalf("reading the bound row: %v", err)
	}
	if rowsBound != 1 || gotID != good {
		t.Errorf("one bound row for %s expected, got %d rows, user_id %q", good, rowsBound, gotID)
	}
	if boundAt.IsZero() || !boundNow {
		t.Errorf("bound_at %v is not close to now()", boundAt)
	}
	if bound, _, err := run(good, false); err == nil || bound != good {
		t.Errorf("binding again without rebind must be refused and keep the binding: %v, bound %q", err, bound)
	}
	// Another account without rebind: it holds the smoke email now, so only "already bound" stops it.
	holder(second, smokeEmail)
	if bound, _, err := run(second, false); err == nil || !strings.Contains(err.Error(), "already bound") || bound != good {
		t.Errorf("another account without rebind: %v, bound %q", err, bound)
	}
	// A refused rebind keeps the old binding (the checks run before the DELETE, and both are one transaction).
	holder(google, smokeEmail)
	if bound, _, err := run(google, true); err == nil || !strings.Contains(err.Error(), "other than email (google)") || bound != good {
		t.Errorf("a refused rebind must keep the old binding: %v, bound %q", err, bound)
	}
	// An account without the smoke email is refused as a rebind too, and the old binding stays.
	holder(good, smokeEmail)
	if bound, _, err := run(second, true); err == nil || !strings.Contains(err.Error(), "is not the account with the email") || bound != good {
		t.Errorf("a rebind to an account without the smoke email: %v, bound %q", err, bound)
	}
	// The operator moves the email to the new account, then rebinds.
	holder(second, smokeEmail)
	if bound, _, err := run(second, true); err != nil || bound != second {
		t.Errorf("rebind: %v, bound %q", err, bound)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 1 {
		t.Errorf("%d rows after a rebind", n)
	}
	// Deleting the bound account removes the binding; an account without any identity row has no other
	// sign-in either, so it binds once it holds the smoke email.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM smoke_account`); n != 0 {
		t.Errorf("the binding outlived its account: %d rows", n)
	}
	holder(noIdent, smokeEmail)
	if bound, _, err := run(noIdent, false); err != nil || bound != noIdent {
		t.Errorf("binding an account without any identity row: %v, bound %q", err, bound)
	}
}

// scripts/sql/consent_smoke_account.sql, the operator's record of the smoke account's consent for a policy
// version: only for the account in smoke_account, only while that account still holds the smoke email, once
// per version.
func TestConsentSmokeAccountScript(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/sql/consent_smoke_account.sql")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	const versionLit = "'<PRIVACY_POLICY_VERSION>'"
	if strings.Count(script, versionLit) != 1 {
		t.Fatalf("the script's marked literal changed (%q)", versionLit)
	}
	if !strings.Contains(script, "v_email   constant text := '"+smokeEmail+"'") {
		t.Fatalf("the script's smoke email is not %s", smokeEmail)
	}
	ctx, pool := smokeScriptDB(t)
	const smoke, other = "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	for _, id := range []string{smoke, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth.users (id, email) VALUES ($1, $2)`, id, id+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	setEmail := func(id, email string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE auth.users SET email = $2 WHERE id = $1`, id, email); err != nil {
			t.Fatal(err)
		}
	}
	// A consent of the other account must never be touched.
	if _, err := pool.Exec(ctx, `INSERT INTO consents (user_id, kind, policy_version) VALUES ($1, 'health_processing', '2026-01-01')`, other); err != nil {
		t.Fatal(err)
	}
	counts := func() (mine, others int) {
		t.Helper()
		return scalar[int](t, pool, `SELECT count(*) FROM consents WHERE user_id = $1`, smoke),
			scalar[int](t, pool, `SELECT count(*) FROM consents WHERE user_id <> $1`, smoke)
	}
	run := func(version string) (string, error) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		tag, err := conn.Exec(ctx, strings.Replace(script, versionLit, "'"+version+"'", 1))
		return tag.String(), err
	}
	refused := func(name, version, want string) {
		t.Helper()
		before, beforeOthers := counts()
		if _, err := run(version); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want it to contain %q", name, err, want)
		}
		if after, afterOthers := counts(); after != before || afterOthers != beforeOthers {
			t.Errorf("%s: consents changed (%d to %d, others %d to %d)", name, before, after, beforeOthers, afterOthers)
		}
	}

	// Unbound: refused, nothing written.
	setEmail(smoke, smokeEmail)
	refused("an unbound smoke", "2026-11-26", "public.smoke_account is empty, no smoke account is bound")
	if _, err := pool.Exec(ctx, `INSERT INTO smoke_account (user_id) VALUES ($1)`, smoke); err != nil {
		t.Fatal(err)
	}
	// The version: the placeholder, a wrong shape, a date that does not exist.
	refused("the placeholder left in", "<PRIVACY_POLICY_VERSION>", "is not YYYY-MM-DD")
	refused("a version of the wrong shape", "26-11-26", "is not YYYY-MM-DD")
	refused("a version with a suffix", "2026-11-26x", "is not YYYY-MM-DD")
	refused("a month 13", "2026-13-01", "is not a real date")
	refused("a day that does not exist", "2026-02-30", "is not a real date")
	// The email re-check: another address, none at all, no auth.users row.
	setEmail(smoke, "someone.else@example.test")
	refused("another email", "2026-11-26", "does not have the email "+smokeEmail)
	setEmail(smoke, retiredSmokeEmail)
	refused("the retired lookalike email", "2026-11-26", "does not have the email "+smokeEmail)
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET email = NULL WHERE id = $1`, smoke); err != nil {
		t.Fatal(err)
	}
	refused("no email", "2026-11-26", "does not have the email "+smokeEmail)
	if mine, _ := counts(); mine != 0 {
		t.Fatalf("a refused run left %d consents", mine)
	}

	// The bound account with the smoke email (any case) gets one consent, and the last statement shows it.
	setEmail(smoke, "Smoke@RyftApp.de")
	tag, err := run("2026-11-26")
	if err != nil {
		t.Fatalf("recording the consent: %v", err)
	}
	if tag != "SELECT 1" {
		t.Errorf("the last statement must return one row, its tag is %q", tag)
	}
	type consent struct {
		policy, kind string
		app          *string
		revoked      *time.Time
	}
	read := func() consent {
		t.Helper()
		var c consent
		if err := pool.QueryRow(ctx, `SELECT policy_version, kind, app_version, revoked_at FROM consents
			WHERE user_id = $1`, smoke).Scan(&c.policy, &c.kind, &c.app, &c.revoked); err != nil {
			t.Fatalf("one consent row expected: %v", err)
		}
		return c
	}
	c := read()
	if c.policy != "2026-11-26" || c.kind != "health_processing" || c.app == nil || *c.app != "operator-sql" || c.revoked != nil {
		t.Errorf("the consent row: %+v", c)
	}
	var grantedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT granted_at FROM consents WHERE user_id = $1`, smoke).Scan(&grantedAt); err != nil {
		t.Fatal(err)
	}
	if _, others := counts(); others != 1 {
		t.Errorf("another account's consents changed: %d rows", others)
	}

	// A rerun inserts nothing, and keeps granted_at.
	if _, err := run("2026-11-26"); err != nil {
		t.Fatalf("a rerun: %v", err)
	}
	if mine, _ := counts(); mine != 1 {
		t.Errorf("a rerun must insert nothing, the account has %d consents", mine)
	}
	var again time.Time
	if err := pool.QueryRow(ctx, `SELECT granted_at FROM consents WHERE user_id = $1`, smoke).Scan(&again); err != nil || !again.Equal(grantedAt) {
		t.Errorf("a rerun moved granted_at: %v -> %v (%v)", grantedAt, again, err)
	}

	// A revoked consent of the same version does not count as current: the run records a new one.
	if _, err := pool.Exec(ctx, `UPDATE consents SET revoked_at = now() WHERE user_id = $1`, smoke); err != nil {
		t.Fatal(err)
	}
	if _, err := run("2026-11-26"); err != nil {
		t.Fatalf("after a revoke: %v", err)
	}
	if mine, _ := counts(); mine != 2 {
		t.Errorf("after a revoke want a new row (2 in all), got %d", mine)
	}

	// The next version adds its own row; the last statement shows the new one as the current consent.
	tag, err = run("2027-03-01")
	if err != nil || tag != "SELECT 1" {
		t.Fatalf("a new version: %v (tag %q)", err, tag)
	}
	if got := scalar[string](t, pool, `SELECT policy_version FROM consents WHERE user_id = $1 AND revoked_at IS NULL ORDER BY granted_at DESC, id DESC LIMIT 1`, smoke); got != "2027-03-01" {
		t.Errorf("the current consent is %q", got)
	}
	if mine, others := counts(); mine != 3 || others != 1 {
		t.Errorf("consents after the new version: %d of the account, %d of others", mine, others)
	}
}
