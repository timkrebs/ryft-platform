package store

import (
	"context"
	"net"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
)

// The pooler leg (D221, design §4). On Cloud Run the services reach Postgres through
// Supavisor's transaction pooler (6543) with default_query_exec_mode=exec: pgx's unnamed
// statements, parameters as text typed by their Go type, and whichever server connection is
// free for each transaction. Two opt-in parts:
//   - RYFT_TEST_POOLER_URL (CI's `pooler` job: PgBouncer, pool_mode transaction, in front of the
//     server of RYFT_TEST_POSTGRES_URL): testStore hands the store a pool through it, so the store's
//     own queries in every test go through the bouncer in exec mode. Set-up, migrations, the tests'
//     own reads and writes and the role pools stay on the direct pool.
//   - RYFT_POOLER_URL: TestPooler, read-only. CI runs it against the bouncer; runbook step 7
//     against Supavisor before anything is deployed (make pooler-check).

// pooledPool is the store's pool through RYFT_TEST_POOLER_URL for database name, or nil when that
// is unset.
func pooledPool(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("RYFT_TEST_POOLER_URL")
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("RYFT_TEST_POOLER_URL does not parse as a URL")
	}
	u.Path = "/" + name
	pool, err := db.Connect(context.Background(), u.String(), 4)
	if err != nil {
		t.Fatalf("the store's pool through RYFT_TEST_POOLER_URL: %v", err)
	}
	t.Cleanup(pool.Close) // before testStore's DROP DATABASE: cleanups run last in, first out
	if m := pool.Config().ConnConfig.DefaultQueryExecMode; m != pgx.QueryExecModeExec {
		t.Fatalf("RYFT_TEST_POOLER_URL must set default_query_exec_mode=exec, as the services' DATABASE_URL does (is %s)", m)
	}
	return pool
}

// poolerCheckLock is TestPooler's advisory-lock class: beside testsupport.RoleLockKey (7419099),
// outside the classes in use (locks.go), so the check never waits on a service's lock.
const poolerCheckLock = 7419098

// poolerTag marks TestPooler's statements, so pg_prepared_statements can be asked for them.
const poolerTag = "/* ryft pooler check */ "

// oneAddress resolves host to a single address, IPv4 first (Supavisor's pooler is IPv4-only,
// like Cloud Run's default egress): pgx would otherwise try every address after a failure that is
// not a refused login, and each try is another login at the pooler.
func oneAddress(ctx context.Context, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) <= 1 {
		return addrs, err
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return []string{a}, nil
		}
	}
	return addrs[:1], nil
}

// TestPooler checks what the services need from a transaction-mode pooler with
// default_query_exec_mode=exec, read-only: one login, one connection, the services' own pool
// config (db.PoolConfig, its AfterConnect), and everything in a READ ONLY transaction that is
// rolled back. The settings the text results depend on; parameters of the store's types (uuid,
// uuid[], text, int, timestamptz, date, jsonb, text[], float8, bool, NULL) back as they went;
// a pgx.Batch; SET LOCAL, set_config(…, true) and pg_advisory_xact_lock seen by the next statement
// of the same transaction (one server connection per transaction); and no named prepared statement
// of ours on that connection.
func TestPooler(t *testing.T) {
	raw := os.Getenv("RYFT_POOLER_URL")
	if raw == "" {
		t.Skip("set RYFT_POOLER_URL: CI's pooler job (PgBouncer) or runbook step 7 (make pooler-check, Supavisor on 6543)")
	}
	pcfg, err := db.PoolConfig(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	cc := pcfg.ConnConfig
	if cc.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("RYFT_POOLER_URL must set default_query_exec_mode=exec, as the services' DATABASE_URL does (is %s)", cc.DefaultQueryExecMode)
	}
	if local := cc.Host == "127.0.0.1" || cc.Host == "localhost" || cc.Host == "::1"; !local && (cc.TLSConfig == nil || cc.TLSConfig.InsecureSkipVerify) {
		t.Fatalf("refusing to log in to %s without verified TLS (sslmode=verify-full and sslrootcert)", cc.Host)
	}
	cc.LookupFunc = oneAddress
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		t.Fatalf("connect (one attempt; do not rerun it in a loop: each failed login feeds Supavisor's circuit breaker "+
			"for the whole role): %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := pcfg.AfterConnect(ctx, conn); err != nil {
		t.Fatal(err)
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }() // a no-op after the checked rollback below

	// exec mode reads results as text: pgx parses ISO dates, postgres-style intervals and hex
	// bytea, and floats are exact only with extra_float_digits >= 1 (the default since PG 12).
	var who, version, dateStyle, intervalStyle, byteaOutput string
	var floatDigits int
	if err := tx.QueryRow(ctx, poolerTag+`SELECT current_user, current_setting('server_version'), current_setting('DateStyle'),
		current_setting('IntervalStyle'), current_setting('bytea_output'), current_setting('extra_float_digits')::int`).
		Scan(&who, &version, &dateStyle, &intervalStyle, &byteaOutput, &floatDigits); err != nil {
		t.Fatal(err)
	}
	if len(dateStyle) < 3 || dateStyle[:3] != "ISO" || intervalStyle != "postgres" || byteaOutput != "hex" || floatDigits < 1 {
		t.Errorf("DateStyle %q, IntervalStyle %q, bytea_output %q, extra_float_digits %d: pgx's text results need ISO, postgres, hex, >= 1",
			dateStyle, intervalStyle, byteaOutput, floatDigits)
	}

	// Transaction-scoped settings and lock (the relay's and the migrations' pattern).
	if _, err := tx.Exec(ctx, poolerTag+`SET LOCAL statement_timeout = '15s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, poolerTag+`SELECT set_config('lock_timeout', $1, true)`, "5s"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, poolerTag+`SELECT pg_advisory_xact_lock($1, hashtext($2::text))`, poolerCheckLock, "pooler-check"); err != nil {
		t.Fatal(err)
	}
	var stmtTimeout, lockTimeout string
	var held int
	if err := tx.QueryRow(ctx, poolerTag+`SELECT current_setting('statement_timeout'), current_setting('lock_timeout'),
		(SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND classid::int8 = $1)`,
		int64(poolerCheckLock)).Scan(&stmtTimeout, &lockTimeout, &held); err != nil {
		t.Fatal(err)
	}
	if stmtTimeout != "15s" || lockTimeout != "5s" || held != 1 {
		t.Errorf("statement_timeout %q, lock_timeout %q, advisory locks held %d: want 15s, 5s, 1 (one server connection for the transaction)",
			stmtTimeout, lockTimeout, held)
	}

	// The store's parameter types, there and back.
	id, other := uuid.New(), uuid.New()
	text := `it's "q" \ ü`
	at := time.Date(2026, 11, 15, 6, 30, 15, 123456000, time.FixedZone("CET", 3600))
	doc := `{"kind": "hr", "n": [1, 2.5, null], "ok": true}`
	texts := []string{"a", "b c", "", "NULL", " x"}
	tenth := 0.1
	f := tenth + 0.2
	var (
		gotID    uuid.UUID
		gotIDs   []uuid.UUID
		gotText  string
		gotInt   int
		gotAt    time.Time
		gotDay   time.Time
		gotDoc   map[string]any
		gotTexts []string
		gotF     float64
		gotB     bool
		gotNull  *string
		matched  int64
	)
	if err := tx.QueryRow(ctx, poolerTag+`SELECT $1::uuid, $2::uuid[], $3::text, $4::int, $5::timestamptz, $6::date, $7::jsonb,
		$8::text[], $9::float8, $10::bool, $11::text, (SELECT count(*) FROM unnest($2::uuid[]) u WHERE u = ANY($12))`,
		id, []uuid.UUID{id, other}, text, 42, at, "2026-11-15", doc, texts, f, true, nil, []uuid.UUID{other}).
		Scan(&gotID, &gotIDs, &gotText, &gotInt, &gotAt, &gotDay, &gotDoc, &gotTexts, &gotF, &gotB, &gotNull, &matched); err != nil {
		t.Fatal(err)
	}
	switch {
	case gotID != id, !slices.Equal(gotIDs, []uuid.UUID{id, other}), matched != 1:
		t.Errorf("uuid %v, uuid[] %v, matches %d", gotID, gotIDs, matched)
	case gotText != text, gotInt != 42, !slices.Equal(gotTexts, texts):
		t.Errorf("text %q, int %d, text[] %q", gotText, gotInt, gotTexts)
	case !gotAt.Equal(at) || gotAt.Location() != time.UTC:
		t.Errorf("timestamptz %v, want %v in UTC", gotAt, at)
	case !gotDay.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)):
		t.Errorf("date %v", gotDay)
	case gotDoc["kind"] != "hr" || gotDoc["ok"] != true:
		t.Errorf("jsonb %v", gotDoc)
	case gotF != f, !gotB, gotNull != nil:
		t.Errorf("float8 %v (want %v exactly), bool %v, NULL %v", gotF, f, gotB, gotNull)
	}

	// A batch: one pipeline, one Sync (the store's runBatch and SendBatch).
	b := &pgx.Batch{}
	b.Queue(poolerTag+`SELECT $1::uuid`, id)
	b.Queue(poolerTag+`SELECT count(*) FROM unnest($1::text[])`, texts)
	b.Queue(poolerTag+`SELECT $1::jsonb ->> 'kind'`, doc)
	br := tx.SendBatch(ctx, b)
	var bID uuid.UUID
	var bN int64
	var bKind string
	for i, dst := range []any{&bID, &bN, &bKind} {
		if err := br.QueryRow().Scan(dst); err != nil {
			t.Fatalf("batch query %d: %v", i+1, err)
		}
	}
	if err := br.Close(); err != nil {
		t.Fatal(err)
	}
	if bID != id || bN != int64(len(texts)) || bKind != "hr" {
		t.Errorf("batch: %v, %d, %q", bID, bN, bKind)
	}

	// Unnamed statements leave nothing in pg_prepared_statements; a named one of ours would be there.
	var named int
	if err := tx.QueryRow(ctx, poolerTag+`SELECT count(*) FROM pg_prepared_statements WHERE statement LIKE '%ryft pooler check%'`).
		Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 0 {
		t.Errorf("%d named prepared statements of this check on the server connection: exec mode sends unnamed ones", named)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	t.Logf("through %s:%d as %s (PostgreSQL %s): exec mode, one read-only transaction, rolled back", cc.Host, cc.Port, who, version)
}
