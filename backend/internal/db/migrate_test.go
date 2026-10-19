package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration test, opt-in: RYFT_TEST_POSTGRES_URL is a superuser connection
// to a throwaway server (it creates and drops its own databases), e.g.
//
//	podman run -d --rm --name pgtest -e POSTGRES_PASSWORD=pw -p 55432:5432 postgres:17-alpine
//	RYFT_TEST_POSTGRES_URL=postgres://postgres:pw@localhost:55432/postgres go test ./internal/db/

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const seed = "00000000-0000-0000-0000-000000000001"

// freshDB creates an empty database and returns a pool on it; supabase also
// fakes what Supabase provides: the auth schema with auth.uid() and the API roles.
func freshDB(t *testing.T, supabase bool) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run migration tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	name := fmt.Sprintf("ryft_migtest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	if supabase {
		for _, q := range []string{
			`DO $$ BEGIN
			   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='anon') THEN CREATE ROLE anon NOLOGIN; END IF;
			   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN CREATE ROLE authenticated NOLOGIN; END IF;
			   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN CREATE ROLE service_role NOLOGIN; END IF;
			 END $$`,
			`CREATE SCHEMA auth`,
			`CREATE FUNCTION auth.uid() RETURNS uuid LANGUAGE sql STABLE AS
			   $$ SELECT nullif(current_setting('request.jwt.claim.sub', true), '')::uuid $$`,
			// Supabase grants the API roles everything in public by default.
			`GRANT USAGE ON SCHEMA public TO anon, authenticated, service_role`,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO anon, authenticated, service_role`,
		} {
			if _, err := pool.Exec(ctx, q); err != nil {
				t.Fatalf("fake Supabase: %v", err)
			}
		}
	}
	return pool
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, q string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestMigrateVanillaPostgres(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE policyname LIKE '%own_row%'`); n != 0 {
		t.Fatalf("no own-row policies on plain Postgres, got %d", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM users WHERE id=$1`, seed); n != 0 {
		t.Fatal("the unused seed user must be removed")
	}
}

func TestMigrateKeepsSeedUserWithData(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value) VALUES ($1,'heart_rate',now(),now(),60)`, seed); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM users WHERE id=$1`, seed); n != 1 {
		t.Fatal("the seed user with samples must be kept")
	}
}

// 0013: whoever already changed the default sleep baseline set it by hand.
func TestMigrateMarksChangedSleepBaseManual(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 12); err != nil {
		t.Fatal(err)
	}
	changed, untouched := "aaaaaaaa-0000-0000-0000-000000000013", "bbbbbbbb-0000-0000-0000-000000000013"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, sleep_base_min) VALUES ($1, 480), ($2, 460)`, changed, untouched); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{changed: "manual", untouched: "default"} {
		if got := scalar[string](t, pool, `SELECT sleep_base_source FROM users WHERE id=$1`, id); got != want {
			t.Errorf("%s: source %s, want %s", id, got, want)
		}
	}
}

func TestMigrateSupabaseShape(t *testing.T) {
	pool := freshDB(t, true)
	if err := Migrate(context.Background(), pool, quiet); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"users", "devices", "samples", "sleep_segments", "workouts", "daily_scores",
		"deleted_users", "schema_migrations", "apple_tokens"}, trainTables...)
	for _, tbl := range tables {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, "public."+tbl) {
			t.Errorf("RLS must be enabled on %s", tbl)
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			if scalar[bool](t, pool, `SELECT has_table_privilege($1, $2, 'SELECT')`, role, "public."+tbl) {
				t.Errorf("%s must not be able to read %s (the services are the only data API)", role, tbl)
			}
		}
	}
	// 0007: five tables + users; 0011: read and write on the four with built-ins, one on the other five.
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname='public' AND policyname LIKE '%own_row%'`); n != 6+4*2+5 {
		t.Errorf("want %d own-row policies, got %d", 6+4*2+5, n)
	}
	// Built-ins (user_id NULL) are readable by every signed-in user, writable by none.
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname='public' AND tablename='exercises'
		AND cmd='SELECT' AND qual LIKE '%user_id IS NULL%'`); n != 1 {
		t.Error("exercises: want a read policy that admits built-ins")
	}
	// Every table admits the service roles (migration 0009), or they would see nothing.
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname='public' AND policyname LIKE '%_services'`); n != len(tables) {
		t.Errorf("want a service policy on each of the %d tables, got %d", len(tables), n)
	}
	if scalar[bool](t, pool, `SELECT has_sequence_privilege('anon', 'public.samples_id_seq', 'USAGE')`) {
		t.Error("anon must not use the sequences")
	}
	// Tables created by later migrations must not be granted to the API roles either.
	if _, err := pool.Exec(context.Background(), `CREATE TABLE later_table (id int)`); err != nil {
		t.Fatal(err)
	}
	if scalar[bool](t, pool, `SELECT has_table_privilege('authenticated', 'public.later_table', 'SELECT')`) {
		t.Error("default privileges must keep new tables away from the API roles")
	}
}
