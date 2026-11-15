package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// D228: on Cloud Run the services read results as text (exec mode, D221), and a float8 is exact as
// text only with extra_float_digits >= 1. Supabase's configuration file sets 0; migration 0035 sets 1
// on each service role. Here the test database's own setting of 0 stands in for Supabase's file: a
// database setting ranks above the configuration file and below a role's, so a role setting that wins
// here wins there too. ALTER SYSTEM would change the shared server under the other packages' tests.
// CI's pooler job runs TestPooler against a configuration file of 0 itself.
func TestServiceRolesReadExactFloats(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	name := scalar[string](t, pool, `SELECT current_database()`)
	if _, err := pool.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{name}.Sanitize()+` SET extra_float_digits = 0`); err != nil {
		t.Fatal(err)
	}
	dsn := pool.Config().ConnString()
	tenth := 0.1
	f := tenth + 0.2 // at run time 0.30000000000000004, which 15 significant digits print as 0.3

	// read opens a new session (password "" keeps the owner's) in exec mode, as the services do, and
	// returns its extra_float_digits, where that came from, and f there and back.
	read := func(t *testing.T, role, password string) (digits int, source string, got float64) {
		t.Helper()
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if role != "" {
			cfg.User, cfg.Password = role, password
		}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect as %q: %v", role, err)
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if err := conn.QueryRow(ctx, `SELECT setting::int, source, $1::float8 FROM pg_settings WHERE name = 'extra_float_digits'`, f).
			Scan(&digits, &source, &got); err != nil {
			t.Fatal(err)
		}
		return digits, source, got
	}

	// The owner has no role setting, so the database's 0 holds and f comes back rounded: the stand-in
	// reproduces what TestPooler met on Supabase.
	if digits, source, got := read(t, "", ""); digits != 0 || source != "database" || got == f {
		t.Fatalf("owner: extra_float_digits %d (%s), float8 %v: the database's 0 does not hold, so this test proves nothing", digits, source, got)
	}

	for _, role := range ServiceRoles {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
		digits, source, got := read(t, role, testsupport.RolePassword(role))
		if digits < 1 || source != "user" || got != f {
			t.Errorf("%s: extra_float_digits %d (%s), float8 %v, want >= 1 from the role (0035) and %v exactly", role, digits, source, got, f)
		}
	}

	// 0035 again (another database on this server, or a rerun by hand) skips the roles that have it:
	// their rows in pg_db_role_setting keep the transaction that wrote them (xmin).
	settingRows := func() string {
		return scalar[string](t, pool, `SELECT string_agg(r.rolname || ':' || s.xmin::text, ',' ORDER BY r.rolname)
			FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole
			WHERE s.setdatabase = 0 AND r.rolname::text = ANY($1::text[])`, roleNames())
	}
	before := settingRows()
	body, err := migrations.ReadFile("migrations/0035_float_digits.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0035 a second time: %v", err)
	}
	if after := settingRows(); after != before {
		t.Errorf("0035 a second time rewrote role settings: %s, then %s", before, after)
	}
}
