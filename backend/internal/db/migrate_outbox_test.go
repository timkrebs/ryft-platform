package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0034: the outbox's not-before time and notify as its fourth publisher (docs/work/2026-11-14-gcp/plan-a1.md
// → Outbox). Applied to a fresh database, and on top of 0033 with rows the image before wrote, on both
// shapes, and twice; then run as the service roles with RLS on. 0026 turns RLS on for Supabase only; the plain
// run first shows the policy is inert without it, then turns it on itself, so the policy is tested there too.
func TestMigrateOutboxAvailableAt(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		pool := freshDB(t, false)
		if err := Migrate(context.Background(), pool, quiet); err != nil {
			t.Fatal(err)
		}
		checkOutbox0034(t, pool)
	})
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateOutboxAvailableAt(t, supabase) })
	}
}

func testMigrateOutboxAvailableAt(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 33); err != nil {
		t.Fatal(err)
	}
	// The name 0034 replaces: the one Postgres gave 0026's inline CHECK.
	if got := scalar[string](t, pool, `SELECT coalesce(string_agg(conname::text, ',' ORDER BY conname), '') FROM pg_constraint
		WHERE conrelid = 'public.outbox'::regclass AND contype = 'c'`); got != "outbox_publisher_check" {
		t.Fatalf("CHECKs on outbox at 0033: %q, want outbox_publisher_check", got)
	}
	const (
		anna = "aaaaaaaa-0000-4000-8000-000000000034"
		ben  = "bbbbbbbb-0000-4000-8000-000000000034"
	)
	// What the image before 0034 wrote: a row waiting for the broker and a published one.
	for _, q := range []string{
		`INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', '` + anna + `', 'ingested', '{}')`,
		`INSERT INTO outbox (publisher, user_id, routing_key, payload, published_at) VALUES ('scoring', '` + anna + `', 'scored', '{}', now())`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload) VALUES ('notify', 'scored', '{}')`); err == nil {
		t.Fatal("before 0034 the CHECK refuses notify")
	}

	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("a second run is a no-op: %v", err)
	}
	checkOutbox0034(t, pool)
	// The rows from before are due at once: they read the migration's time, after they were written.
	if n := scalar[int](t, pool, `SELECT count(*) FROM outbox WHERE available_at <= now() AND available_at >= created_at`); n != 2 {
		t.Errorf("rows from before 0034 due at once: %d of 2", n)
	}
	// Idempotent: the file again (a manual rerun, a restored schema_migrations) changes nothing.
	body, err := migrations.ReadFile("migrations/0034_outbox_available_at.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0034 a second time: %v", err)
	}
	checkOutbox0034(t, pool)

	for _, role := range []string{"ryft_ingest", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	as := func(role string) *pgx.Conn { return asRole(t, dsn, role, testsupport.RolePassword(role)) }

	if supabase {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.outbox'::regclass`) {
			t.Fatal("RLS on outbox is on on Supabase (0026)")
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			if scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.outbox', 'available_at', 'SELECT')`, role) {
				t.Errorf("%s must not read outbox.available_at", role)
			}
		}
	} else {
		if scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.outbox'::regclass`) {
			t.Fatal("plain Postgres keeps RLS off on outbox (0026 turns it on for Supabase only)")
		}
		// Harmless where RLS is off: with the policy in place, notify's own work goes through.
		notifyRoundTrip(t, pool, as("ryft_notify"), anna)
		if _, err := pool.Exec(ctx, `ALTER TABLE outbox ENABLE ROW LEVEL SECURITY`); err != nil {
			t.Fatal(err)
		}
	}
	// Connected after RLS is on, so no plan from before it is reused.
	notify, ingest, api := as("ryft_notify"), as("ryft_ingest"), as("ryft_api")
	notifyRoundTrip(t, pool, notify, anna)

	// Under RLS notify reaches its own rows only: it neither sees nor changes another publisher's, and
	// cannot write a row as one.
	if n := scalar[int](t, pool, `SELECT count(*) FROM outbox WHERE publisher <> 'notify'`); n < 2 {
		t.Fatalf("the owner sees %d rows of the other publishers, want the 2 from before 0034", n)
	}
	var n int
	if err := notify.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE publisher <> 'notify'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("notify sees %d rows of other publishers (err %v), want 0", n, err)
	}
	for _, q := range []string{
		`SELECT id FROM outbox WHERE publisher = 'ingest' AND published_at IS NULL ORDER BY id FOR UPDATE SKIP LOCKED`,
		`UPDATE outbox SET attempts = attempts + 1 WHERE publisher <> 'notify'`,
		`UPDATE outbox SET published_at = now() WHERE publisher = 'ingest'`,
		`DELETE FROM outbox WHERE publisher <> 'notify'`,
	} {
		if ct, err := notify.Exec(ctx, q); err != nil || ct.RowsAffected() != 0 {
			t.Errorf("%s as notify: %d rows, err %v; want 0 rows", q, ct.RowsAffected(), err)
		}
	}
	var pgErr *pgconn.PgError
	_, err = notify.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload) VALUES ('ingest', 'ingested', '{}')`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("notify must not write a row as ingest (the policy's WITH CHECK): %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM outbox WHERE publisher <> 'notify'`); n != 2 {
		t.Errorf("the other publishers' rows after notify's attempts: %d, want 2", n)
	}

	// The others keep their policy: the image before 0034 inserts without available_at, through RLS, and its
	// row is due at once; the CHECK refuses a publisher it does not know.
	var dueNow bool
	if err := ingest.QueryRow(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', $1, 'ingested', '{}')
		RETURNING available_at <= now()`, anna).Scan(&dueNow); err != nil || !dueNow {
		t.Errorf("an insert as the image before 0034 makes it, due at once: %v %v", dueNow, err)
	}
	_, err = ingest.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload) VALUES ('coach', 'scored', '{}')`)
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "outbox_publisher_check" {
		t.Errorf("the CHECK refuses an unknown publisher: %v", err)
	}

	// Account deletion (store.DeleteUser's statement, as the api) removes notify's rows of the account with
	// the rest; notify sees only its own of them.
	if _, err := notify.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, payload, available_at)
		VALUES ('notify', $1, 'session.scored', '{}', now() + interval '1 hour')`, ben); err != nil {
		t.Fatalf("notify writes a wait: %v", err)
	}
	if _, err := ingest.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', $1, 'ingested', '{}')`, ben); err != nil {
		t.Fatal(err)
	}
	if err := notify.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE user_id = $1`, ben).Scan(&n); err != nil || n != 1 {
		t.Errorf("notify sees %d of the account's 2 rows (err %v), want its own 1", n, err)
	}
	if ct, err := api.Exec(ctx, `DELETE FROM outbox WHERE user_id=$1`, ben); err != nil || ct.RowsAffected() != 2 {
		t.Fatalf("the api deletes the account's outbox rows, notify's included: %v %v", ct, err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM outbox WHERE user_id = $1`, ben); n != 0 {
		t.Errorf("%d outbox rows of the deleted account left", n)
	}
}

// checkOutbox0034 pins what 0034 leaves: the column, one validated CHECK that admits the four publishers and
// nothing else, both indexes of unpublished rows, notify's sequence grant, and the two policies.
func checkOutbox0034(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if !scalar[bool](t, pool, `SELECT data_type = 'timestamp with time zone' AND is_nullable = 'NO' AND column_default = 'now()'
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'outbox' AND column_name = 'available_at'`) {
		t.Error("outbox.available_at: want timestamptz NOT NULL DEFAULT now()")
	}
	if got := scalar[string](t, pool, `SELECT coalesce(string_agg(conname::text || ':' || convalidated::text, ',' ORDER BY conname), '')
		FROM pg_constraint WHERE conrelid = 'public.outbox'::regclass AND contype = 'c'`); got != "outbox_publisher_check:true" {
		t.Errorf("CHECKs on outbox: %q, want the one, validated (outbox_publisher_check:true)", got)
	}
	func() { // as the owner, rolled back
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, p := range []string{"ingest", "api", "scoring", "notify"} {
			if _, err := tx.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload) VALUES ($1, 'scored', '{}')`, p); err != nil {
				t.Fatalf("publisher %s: %v", p, err)
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload) VALUES ('coach', 'scored', '{}')`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "outbox_publisher_check" {
			t.Errorf("an unknown publisher must fail outbox_publisher_check: %v", err)
		}
	}()
	for name, want := range map[string][]string{
		"outbox_due":     {"(publisher, available_at)", "WHERE (published_at IS NULL)"},
		"outbox_pending": {"(publisher, id)", "WHERE (published_at IS NULL)"},
	} {
		def := scalar[string](t, pool, `SELECT coalesce(pg_get_indexdef(to_regclass('public.' || $1::text)), '')`, name)
		for _, w := range want {
			if !strings.Contains(def, w) {
				t.Errorf("index %s: %q, want it to contain %q", name, def, w)
			}
		}
	}
	for priv, want := range map[string]bool{"USAGE": true, "SELECT": false, "UPDATE": false} {
		if got := scalar[bool](t, pool, `SELECT has_sequence_privilege('ryft_notify', 'public.outbox_id_seq', $1)`, priv); got != want {
			t.Errorf("ryft_notify %s on outbox_id_seq: %v, want %v", priv, got, want)
		}
	}
	if got := scalar[string](t, pool, `SELECT coalesce(string_agg(policyname::text || ' ' || permissive || ' ' || cmd || ' ' ||
		array_to_string(roles, ','), '; ' ORDER BY policyname), '') FROM pg_policies WHERE schemaname = 'public' AND tablename = 'outbox'`); got !=
		"outbox_notify PERMISSIVE ALL ryft_notify; outbox_services PERMISSIVE ALL ryft_api,ryft_ingest,ryft_scoring" {
		t.Errorf("outbox policies: %q", got)
	}
	for _, col := range []string{"qual", "with_check"} {
		got := scalar[string](t, pool, `SELECT coalesce(`+col+`, '') FROM pg_policies
			WHERE schemaname = 'public' AND tablename = 'outbox' AND policyname = 'outbox_notify'`)
		if !strings.Contains(got, "publisher = 'notify'") {
			t.Errorf("outbox_notify %s: %q, want publisher = 'notify'", col, got)
		}
	}
}

// notifyRoundTrip is notify's outbox as SendAt and the tick's Sweep use it, as ryft_notify: write a wait, find
// it only once it is due (the drain's statement), record a failed send, mark it sent, clean it up. The relay
// changes no other column. pool (the owner) stands in for the clock.
func notifyRoundTrip(t *testing.T, pool *pgxpool.Pool, notify *pgx.Conn, user string) {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := notify.QueryRow(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, message_id, reason, payload, available_at)
		VALUES ('notify', $1, 'scored', 'wait-1', 'quiet_hours', '{}', now() + interval '1 hour') RETURNING id`, user).Scan(&id); err != nil {
		t.Fatalf("notify writes a wait: %v", err)
	}
	due := func() []int64 {
		t.Helper()
		rows, err := notify.Query(ctx, `SELECT id FROM outbox WHERE publisher = 'notify' AND published_at IS NULL AND attempts < 50
			AND available_at <= now() ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			t.Fatalf("notify's drain: %v", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			t.Fatalf("notify's drain: %v", err)
		}
		return ids
	}
	if ids := due(); len(ids) != 0 {
		t.Fatalf("a wait is not due before its time: %v", ids)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET available_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if ids := due(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("the due wait: %v, want [%d]", ids, id)
	}
	for _, q := range []string{
		`UPDATE outbox SET attempts = attempts + 1, last_error = 'publish: 503' WHERE id = $1`,
		`UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = $1`,
	} {
		if ct, err := notify.Exec(ctx, q, id); err != nil || ct.RowsAffected() != 1 {
			t.Fatalf("%s: %v %v", q, ct, err)
		}
	}
	for _, q := range []string{
		`UPDATE outbox SET available_at = now() + interval '1 hour' WHERE id = $1`,
		`UPDATE outbox SET payload = '{"x": 1}' WHERE id = $1`,
		`UPDATE outbox SET publisher = 'notify' WHERE id = $1`,
	} {
		if _, err := notify.Exec(ctx, q, id); err == nil {
			t.Errorf("notify must not run %s", q)
		}
	}
	if ct, err := notify.Exec(ctx, `DELETE FROM outbox WHERE publisher = 'notify' AND published_at IS NOT NULL AND id = $1`, id); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("notify cleans its published row: %v %v", ct, err)
	}
}

// 0034 gives up on an outbox a relay holds (a drain between its SELECT ... FOR UPDATE and its commit) after
// its lock_timeout, instead of queueing every sync's insert behind its ALTER; it leaves nothing behind and
// applies once the table is free.
func TestMigrateOutboxAvailableAtGivesUpOnALock(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 33); err != nil {
		t.Fatal(err)
	}
	relay, err := pool.Begin(ctx) // ROW SHARE, which ACCESS EXCLUSIVE waits for
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Rollback(ctx) // released below; a no-op then
	if _, err := relay.Exec(ctx, `SELECT id FROM outbox WHERE publisher = 'ingest' AND published_at IS NULL ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`); err != nil {
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
	if scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 34)`) ||
		scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'outbox' AND column_name = 'available_at')`) ||
		scalar[bool](t, pool, `SELECT has_table_privilege('ryft_notify', 'public.outbox', 'SELECT')`) {
		t.Fatal("a failed 0034 must leave nothing behind")
	}
	if err := relay.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatalf("once the table is free: %v", err)
	}
}
