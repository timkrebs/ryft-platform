package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
	// The roles first: tests of other packages migrate on this server at the same time.
	if err := EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
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

// 0017: every account from before the beta gate keeps access.
func TestMigrateKeepsExistingAccountsInTheBeta(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 16); err != nil {
		t.Fatal(err)
	}
	old := "cccccccc-0000-0000-0000-000000000017"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, old); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT beta_access_at IS NOT NULL FROM users WHERE id=$1`, old) {
		t.Fatal("an existing account must keep beta access")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ('dddddddd-0000-0000-0000-000000000017')`); err != nil {
		t.Fatal(err)
	}
	if scalar[bool](t, pool, `SELECT beta_access_at IS NOT NULL FROM users WHERE id='dddddddd-0000-0000-0000-000000000017'`) {
		t.Fatal("a new account starts without access")
	}
}

// 0019: a max_hr someone changed from the default was set by hand.
func TestMigrateMarksChangedMaxHRManual(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, max_hr) VALUES
		('cccccccc-0000-0000-0000-000000000019', 176), ('dddddddd-0000-0000-0000-000000000019', 190)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"cccccccc-0000-0000-0000-000000000019": "manual",
		"dddddddd-0000-0000-0000-000000000019": "default"} {
		if got := scalar[string](t, pool, `SELECT max_hr_source FROM users WHERE id=$1`, id); got != want {
			t.Errorf("%s: max_hr_source %s, want %s", id, got, want)
		}
	}
}

// 0020: user_sources starts from what the last 30 days show.
func TestMigrateBackfillsSources(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 19); err != nil {
		t.Fatal(err)
	}
	id := "cccccccc-0000-0000-0000-000000000020"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ($1)`,
		`INSERT INTO samples (user_id, type, start_at, end_at, value, source) VALUES
		   ($1, 'heart_rate', now() - interval '1 hour', now() - interval '1 hour', 60, 'Watch'),
		   ($1, 'vo2max', now() - interval '2 days', now() - interval '2 days', 44, 'Watch'),
		   ($1, 'steps', now() - interval '3 hours', now() - interval '2 hours', 100, 'Phone'),
		   ($1, 'steps', now() - interval '40 days', now() - interval '40 days', 100, 'Old phone'),
		   ($1, 'steps', now(), now(), 1, '')`,
		`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source) VALUES
		   ($1, now() - interval '9 hours', now() - interval '1 hour', 'deep', 'Oura')`,
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES
		   (gen_random_uuid(), $1, 'running', now() - interval '1 day', now() - interval '23 hours', 'Watch')`,
	} {
		if _, err := pool.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	got := scalar[string](t, pool, `SELECT string_agg(name || ':' || kind, ',' ORDER BY name, kind) FROM user_sources WHERE user_id=$1`, id)
	if want := "Oura:sleep,Phone:steps,Watch:hr,Watch:workout"; got != want {
		t.Fatalf("backfilled %s, want %s", got, want)
	}
}

// 0027: rows scored before keep NULL counts and reason (unknown, not 0), and
// the reason is a closed enum.
func TestMigrateCalibratingReason(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 26); err != nil {
		t.Fatal(err)
	}
	id := "cccccccc-0000-0000-0000-000000000027"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ($1)`,
		`INSERT INTO daily_scores (user_id, day, calibrating, baseline_days) VALUES ($1, '2026-10-21', true, 0)`,
	} {
		if _, err := pool.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT baseline_hrv_days IS NULL AND baseline_rhr_days IS NULL AND calibrating_reason IS NULL
		FROM daily_scores WHERE user_id=$1`, id) {
		t.Fatal("a row scored before 0027 must read as unknown (NULL), not as 0 nights")
	}
	if _, err := pool.Exec(ctx, `UPDATE daily_scores SET baseline_hrv_days=0, baseline_rhr_days=24, calibrating_reason='no_hrv'
		WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE daily_scores SET calibrating_reason='no_sleep' WHERE user_id=$1`, id); err == nil {
		t.Fatal("calibrating_reason must refuse a value outside short_history, no_hrv, no_rhr")
	}
}

// 0027 gives up on a held table after its lock_timeout (5 s) instead of waiting
// with every reader queued behind it: the migration fails, nothing of it stays,
// and it applies once the table is free.
func TestMigrateCalibratingReasonGivesUpOnALock(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 26); err != nil {
		t.Fatal(err)
	}
	reader, err := pool.Begin(ctx) // a long read: ACCESS SHARE, which ACCESS EXCLUSIVE waits for
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx) // released below; a no-op then
	if _, err := reader.Exec(ctx, `SELECT count(*) FROM daily_scores`); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	err = Migrate(wait, pool, quiet)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" { // lock_not_available
		t.Fatalf("want a lock timeout, got %v", err)
	}
	if took := time.Since(start); took < 4*time.Second || took > 15*time.Second {
		t.Fatalf("gave up after %s, want about 5 s", took)
	}
	if scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 27)`) ||
		scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		  WHERE table_name = 'daily_scores' AND column_name = 'calibrating_reason')`) {
		t.Fatal("a failed 0027 must leave nothing behind")
	}
	if err := reader.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("once the table is free: %v", err)
	}
	// SET LOCAL ends with the migration's transaction: the session keeps no timeout.
	if got := scalar[string](t, pool, `SHOW lock_timeout`); got != "0" {
		t.Fatalf("lock_timeout after the migration: %s", got)
	}
}

func TestMigrateSupabaseShape(t *testing.T) {
	pool := freshDB(t, true)
	if err := Migrate(context.Background(), pool, quiet); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"users", "devices", "samples", "sleep_segments", "workouts", "daily_scores",
		"deleted_users", "schema_migrations", "apple_tokens", "feature_flags", "push_tokens", "notifications_sent"},
		append(append(trainTables, journalTables...), betaTables...)...)
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
