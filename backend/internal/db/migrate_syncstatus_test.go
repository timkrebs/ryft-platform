package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0067 (D297, D301): the sync status. Applied over a database at 0061 that holds queue rows and an inbox row, on
// both shapes: the old queue rows read as before (closed_reason NULL); closed_reason takes NULL or 'no_consent'
// and nothing else, also when scoring writes it through RLS; the api reads an inbox row's received_at and still
// not its payload; the api's probe "the earliest pending delivery of an account and provider" is answered by
// provider_inbox_user_pending; a rerun of the file changes nothing.
func TestMigrateSyncStatus(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateSyncStatus(t, supabase) })
	}
}

func testMigrateSyncStatus(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 61); err != nil {
		t.Fatal(err)
	}
	const user, other = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + user + `'), ('` + other + `')`,
		`INSERT INTO score_queue (user_id, day, queued_at, scored_at) VALUES
		   ('` + user + `', '2026-11-26', now(), NULL), ('` + user + `', '2026-11-25', now(), now())`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("before 0067: %v", err)
		}
	}

	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}

	// The rows from before read as they did: not closed for any reason.
	if n := scalar[int](t, pool, `SELECT count(*) FROM score_queue WHERE closed_reason IS NULL`); n != 2 {
		t.Errorf("the queue rows from before 0067 must have closed_reason NULL, %d of 2 do", n)
	}
	body, err := migrations.ReadFile("migrations/0067_sync_status.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nSET LOCAL lock_timeout = '5s';\n") {
		t.Error("0067 sets its lock_timeout (D116)")
	}

	// The CHECK: NULL and 'no_consent' only, as the owner and as scoring.
	if _, err := pool.Exec(ctx, `UPDATE score_queue SET closed_reason = 'other' WHERE user_id = $1 AND day = '2026-11-26'`, user); err == nil {
		t.Error("closed_reason 'other' must be refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE score_queue SET closed_reason = '' WHERE user_id = $1 AND day = '2026-11-26'`, user); err == nil {
		t.Error("an empty closed_reason must be refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE score_queue SET closed_reason = 'no_consent' WHERE user_id = $1 AND day = '2026-11-25'`, user); err != nil {
		t.Errorf("closed_reason 'no_consent' must be accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE score_queue SET closed_reason = NULL WHERE user_id = $1 AND day = '2026-11-25'`, user); err != nil {
		t.Errorf("closed_reason NULL must be accepted: %v", err)
	}

	// The inbox: one pending delivery of the account, one of another account, many that are done.
	for _, q := range []string{
		`INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload, payload_sha256, state, received_at)
		 VALUES ('garmin', 'sleep', 'pending-1', '` + user + `', '{"secret": 1}', decode(repeat('ab', 32), 'hex'), 'pending', now() - interval '3 minutes'),
		        ('garmin', 'sleep', 'pending-2', '` + user + `', '{"secret": 2}', decode(repeat('ab', 32), 'hex'), 'pending', now() - interval '1 minute'),
		        ('garmin', 'sleep', 'pending-3', '` + other + `', '{"secret": 3}', decode(repeat('ab', 32), 'hex'), 'pending', now() - interval '9 minutes')`,
		// The spread for the plan below: the account's older deliveries processed, and a backlog of other
		// accounts' pending ones, all older than the account's. A walk of provider_inbox_state_received (state,
		// received_at) would filter that whole backlog before it reached the account's first row; the partial
		// index descends straight to (user_id, provider).
		`INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload, payload_sha256, state, received_at, processed_at)
		 SELECT 'garmin', 'sleep', 'done-' || g, '` + user + `', NULL,
		        decode(repeat('cd', 32), 'hex'), 'processed', now() - g * interval '1 minute', now() - g * interval '1 minute'
		 FROM generate_series(10, 2000) g`,
		`INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload, payload_sha256, state, received_at)
		 SELECT 'garmin', 'sleep', 'backlog-' || g, '` + other + `', '{"secret": 4}',
		        decode(repeat('ef', 32), 'hex'), 'pending', now() - g * interval '1 minute'
		 FROM generate_series(10, 5000) g`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("inbox rows: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `ANALYZE provider_inbox`); err != nil {
		t.Fatal(err)
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	as := func(role string) *pgx.Conn { return asRole(t, dsn, role, testsupport.RolePassword(role)) }

	// api: when the earliest pending delivery arrived (the sync status' probe), through RLS on the Supabase shape;
	// never the payload or the record id, and no write.
	api := as("ryft_api")
	var minOK bool
	if err := api.QueryRow(ctx, `SELECT min(received_at) BETWEEN now() - interval '4 minutes' AND now() - interval '2 minutes'
		FROM provider_inbox WHERE user_id = $1 AND provider = $2 AND state = 'pending'`, user, "garmin").Scan(&minOK); err != nil || !minOK {
		t.Errorf("api reads the earliest pending received_at of the account: %v %v", minOK, err)
	}
	for _, q := range []string{
		`SELECT payload FROM provider_inbox`, `SELECT record_id FROM provider_inbox`, `SELECT * FROM provider_inbox`,
		`UPDATE provider_inbox SET received_at = now()`, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload_sha256) VALUES ('garmin', 'sleep', 'x', '` + user + `', decode(repeat('ab', 32), 'hex'))`,
	} {
		if _, err := api.Exec(ctx, q); err == nil {
			t.Errorf("api must not run %q", q)
		}
	}

	// scoring closes an account's open rows with a reason, through RLS: that is the whole of its write.
	scoring := as("ryft_scoring")
	if ct, err := scoring.Exec(ctx, `UPDATE score_queue SET scored_at = now(), closed_reason = 'no_consent' WHERE user_id = $1 AND scored_at IS NULL`, user); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring closes the open rows of an account without consent: %v %v", ct, err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE score_queue SET closed_reason = 'other' WHERE user_id = $1`, user); err == nil {
		t.Error("scoring must not write another reason")
	}
	// ... and a day that is really scored clears it (MarkScored).
	if ct, err := scoring.Exec(ctx, `UPDATE score_queue SET scored_at = now(), closed_reason = NULL WHERE user_id = $1 AND day = '2026-11-26'`, user); err != nil || ct.RowsAffected() != 1 {
		t.Errorf("scoring clears the reason of a day it scored: %v %v", ct, err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE score_queue SET queued_at = now() WHERE user_id = $1`, user); err == nil {
		t.Error("scoring must still not move queued_at")
	}
	// The api reads the reason (the status's "is a day still pending" asks scored_at IS NULL first).
	if _, err := api.Exec(ctx, `SELECT closed_reason FROM score_queue WHERE user_id = $1 AND scored_at IS NULL`, user); err != nil {
		t.Errorf("api reads score_queue.closed_reason: %v", err)
	}
	if _, err := api.Exec(ctx, `UPDATE score_queue SET closed_reason = 'no_consent' WHERE user_id = $1`, user); err == nil {
		t.Error("api must not write closed_reason")
	}

	// The plan. The probe is "the earliest pending delivery of an account and provider": the partial index
	// answers it with one descent. A table of 7000 rows is small enough that the planner may still prefer a
	// sequential scan, so enable_seqscan is off for this session. With only a handful of pending rows the planner
	// rightly takes provider_inbox_state_received (it reaches them as cheaply), which is why the fixture holds a
	// backlog of another account's pending deliveries. Literals, not parameters: a generic plan could not use the
	// partial index.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `EXPLAIN (COSTS OFF) SELECT min(received_at) FROM provider_inbox
		WHERE user_id = '`+user+`' AND provider = 'garmin' AND state = 'pending'`)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if plan := strings.Join(lines, "\n"); !strings.Contains(plan, "provider_inbox_user_pending") {
		t.Errorf("the probe must use provider_inbox_user_pending, plan:\n%s", plan)
	}

	if supabase {
		for _, tbl := range []string{"provider_inbox", "score_queue"} {
			if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = ('public.' || $1)::regclass`, tbl) {
				t.Errorf("RLS must stay on for %s", tbl)
			}
			for _, role := range []string{"anon", "authenticated", "service_role"} {
				if scalar[bool](t, pool, `SELECT has_table_privilege($1, $2, 'SELECT')`, role, "public."+tbl) {
					t.Errorf("%s must not read %s", role, tbl)
				}
			}
		}
	}

	// Idempotent: the file again changes nothing (one index, one constraint, the same grants).
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0067 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'provider_inbox_user_pending'`); n != 1 {
		t.Errorf("want one provider_inbox_user_pending after a rerun, got %d", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'public.score_queue'::regclass AND conname = 'score_queue_closed_reason_check'`); n != 1 {
		t.Errorf("want one score_queue_closed_reason_check after a rerun, got %d", n)
	}
}
