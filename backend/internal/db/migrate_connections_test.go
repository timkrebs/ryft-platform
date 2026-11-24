package db

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0042 (D257, design §5, §13): direct connections. Applied over a database at 0041 that holds raw rows, on
// both shapes: the old rows read as HealthKit and stay as they were (no CHECK on an existing table is
// validated, no index is built on samples or sleep_segments); each role can do exactly what the design gives
// it, run as that role through RLS; the CHECKs hold the shapes the store relies on (S1, S3, S6, B1); the
// per-account rows go with the users row (an image from before 0042 deletes only that) while the
// deregistration queue outlives it; the flag ships dark and a rerun of the file changes nothing.
func TestMigrateConnections(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateConnections(t, supabase) })
	}
}

var connectionTables = []string{"connections", "connection_tokens", "connection_pending", "connection_states",
	"connection_deregistrations", "provider_inbox"}

func testMigrateConnections(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 41); err != nil {
		t.Fatal(err)
	}
	user, other := "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + user + `'), ('` + other + `')`,
		`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		 VALUES ('` + user + `', 'hrv_sdnn', now() - interval '8 hours', now() - interval '8 hours', 42, 'ms', 'Watch')`,
		`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source)
		 VALUES ('` + user + `', now() - interval '8 hours', now() - interval '7 hours', 'core', 'Watch')`,
		`INSERT INTO user_sources (user_id, name, kind, last_seen) VALUES ('` + user + `', 'Watch', 'sleep', now())`,
		`INSERT INTO daily_scores (user_id, day, hrv_ms) VALUES ('` + user + `', current_date, 42)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("before 0042: %v", err)
		}
	}
	indexes := func(tbl string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1 ORDER BY 1`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	before := map[string]string{}
	for _, tbl := range []string{"samples", "sleep_segments"} {
		before[tbl] = strings.Join(indexes(tbl), ",")
	}

	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}

	// Existing rows: HealthKit, no vendor, nothing validated, no new index on the two big tables.
	if !scalar[bool](t, pool, `SELECT (SELECT bool_and(origin = 'healthkit' AND vendor IS NULL AND record_id IS NULL) FROM samples)
		AND (SELECT bool_and(origin = 'healthkit' AND vendor IS NULL AND record_state IS NULL) FROM sleep_segments)
		AND (SELECT bool_and(origin = 'healthkit') FROM user_sources)
		AND (SELECT bool_and(hrv_metric IS NULL AND coverage IS NULL AND score_status IS NULL) FROM daily_scores)`) {
		t.Error("the rows from before 0042 must read as HealthKit with the new columns NULL")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint
		WHERE conrelid IN ('public.samples'::regclass, 'public.sleep_segments'::regclass, 'public.user_sources'::regclass, 'public.daily_scores'::regclass)
		  AND conname = ANY ($1) AND NOT convalidated`, []string{
		"samples_origin_check", "samples_vendor_check", "samples_record_id_check",
		"sleep_segments_origin_check", "sleep_segments_vendor_check", "sleep_segments_record_id_check", "sleep_segments_record_state_check",
		"user_sources_origin_check", "daily_scores_hrv_metric_check", "daily_scores_score_status_check",
	}); n != 10 {
		t.Errorf("want the 10 CHECKs on existing tables present and NOT VALID, got %d", n)
	}
	for _, tbl := range []string{"samples", "sleep_segments"} {
		if got := strings.Join(indexes(tbl), ","); got != before[tbl] {
			t.Errorf("0042 must build no index on %s: before %s, after %s", tbl, before[tbl], got)
		}
	}
	body, err := migrations.ReadFile("migrations/0042_connections.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nSET LOCAL lock_timeout = '5s';\n") {
		t.Error("0042 sets its lock_timeout (D116)")
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	as := func(role string) *pgx.Conn { return asRole(t, dsn, role, testsupport.RolePassword(role)) }
	api, ingest, scoring, notify := as("ryft_api"), as("ryft_ingest"), as("ryft_scoring"), as("ryft_notify")
	must := func(c *pgx.Conn, what, q string, args ...any) {
		t.Helper()
		if _, err := c.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	mustNot := func(c *pgx.Conn, what, q string, args ...any) {
		t.Helper()
		_, err := c.Exec(ctx, q, args...)
		wantRefusal(t, what, err)
	}
	// Ciphertexts and digests of the right length, made at run time.
	blob := func(c byte, n int) []byte { return bytes.Repeat([]byte{c}, n) }

	// api: the OAuth side, through RLS on the Supabase shape.
	must(api, "api opens a state", `INSERT INTO connection_states (state_hash, user_id, provider, verifier_enc, policy_version, expires_at)
		VALUES ($1, $2, 'fake', $3, '2026-11-02', now() + interval '10 minutes')`, blob(1, 32), user, blob(2, 40))
	var n int
	if err := api.QueryRow(ctx, `WITH d AS (DELETE FROM connection_states WHERE state_hash = $1 AND provider = 'fake' RETURNING user_id)
		SELECT count(*) FROM d`, blob(1, 32)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api consumes a state: %d %v", n, err)
	}
	mustNot(api, "a state is never updated", `UPDATE connection_states SET expires_at = now()`)
	must(api, "api stores a pending grant", `INSERT INTO connection_pending (user_id, provider, provider_user_id, permissions, access_enc, refresh_enc,
		confirm_hash, confirm_expires_at) VALUES ($1, 'fake', 'p-1', '{HEALTH_EXPORT}', $2, $3, $4, now() + interval '10 minutes')`,
		user, blob(3, 40), blob(4, 40), blob(5, 32))
	must(api, "api confirms: the connection", `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1, 'fake', 'p-1', 'connected', '{HEALTH_EXPORT}', '2026-11-02')`, user)
	must(api, "api confirms: the tokens", `INSERT INTO connection_tokens (user_id, provider, access_enc, refresh_enc, refresh_due_at)
		VALUES ($1, 'fake', $2, $3, now() + interval '1 hour')`, user, blob(6, 40), blob(7, 40))
	must(api, "api confirms: the pending row keeps its hash, drops its tokens",
		`UPDATE connection_pending SET access_enc = NULL, refresh_enc = NULL, confirmed_at = now() WHERE user_id = $1 AND provider = 'fake'`, user)
	must(api, "api queues a deregistration", `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc)
		VALUES ($1, 'fake', 'p-0', 'replaced', $2)`, user, blob(8, 40))
	must(api, "api backs a deregistration off", `UPDATE connection_deregistrations SET attempts = attempts + 1, last_error = 'unreachable',
		next_attempt_at = now() + interval '15 minutes' WHERE user_id = $1`, user)
	for _, tbl := range []string{"connections", "connection_tokens", "connection_pending", "connection_deregistrations", "provider_inbox"} {
		mustNot(api, "api TRUNCATE "+tbl, `TRUNCATE `+tbl)
	}

	// ingest: the inbox, the lookup, the status; nothing of the OAuth side.
	var resolved string
	if err := ingest.QueryRow(ctx, `SELECT user_id::text FROM connections
		WHERE provider = 'fake' AND provider_user_id = ANY ($1) AND status IN ('connected', 'needs_reauth')`, []string{"p-1"}).Scan(&resolved); err != nil || resolved != user {
		t.Fatalf("ingest resolves a provider user: %q %v", resolved, err)
	}
	for _, q := range []string{
		`SELECT policy_version FROM connections`, `SELECT permissions FROM connections`, `SELECT * FROM connections`,
		`SELECT 1 FROM connection_tokens`, `SELECT 1 FROM connection_pending`, `SELECT 1 FROM connection_states`,
		`SELECT 1 FROM connection_deregistrations`,
		`UPDATE connections SET policy_version = 'x'`,
		`INSERT INTO connections (user_id, provider, provider_user_id, status, policy_version) VALUES ('` + other + `', 'fake', 'p-9', 'connected', 'x')`,
		`DELETE FROM connections`,
	} {
		mustNot(ingest, "ingest: "+q, q)
	}
	payload := []byte(`{"night":{"state":"final"}}`)
	var inboxID int64
	if err := ingest.QueryRow(ctx, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, version, payload, payload_sha256)
		VALUES ('fake', 'sleep', 'n-1', $1, 1, $2, $3) RETURNING id`, user, payload, blob(9, 32)).Scan(&inboxID); err != nil {
		t.Fatalf("ingest stores an inbox row: %v", err)
	}
	// S1: a collision of another account on the same record id changes nothing.
	ct, err := ingest.Exec(ctx, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, version, payload, payload_sha256)
		VALUES ('fake', 'sleep', 'n-1', $1, 2, $2, $3)
		ON CONFLICT (provider, kind, record_id) DO UPDATE SET payload = EXCLUDED.payload, payload_sha256 = EXCLUDED.payload_sha256,
		    version = EXCLUDED.version, revision = provider_inbox.revision + 1, state = 'pending', received_at = now()
		WHERE provider_inbox.user_id = EXCLUDED.user_id AND provider_inbox.payload_sha256 <> EXCLUDED.payload_sha256
		  AND EXCLUDED.version >= provider_inbox.version`, other, payload, blob(10, 32))
	if err != nil || ct.RowsAffected() != 0 {
		t.Fatalf("the guarded DO UPDATE must leave another account's row alone: %v %v", ct, err)
	}
	if !scalar[bool](t, pool, `SELECT user_id = $1 AND revision = 1 AND payload_sha256 = $2 FROM provider_inbox WHERE id = $3`, user, blob(9, 32), inboxID) {
		t.Error("the collision changed the row")
	}
	must(ingest, "ingest applies the record", `UPDATE provider_inbox SET state = 'processed', processed_at = now(), payload = NULL,
		attempts = attempts + 1 WHERE id = $1`, inboxID)
	must(ingest, "ingest writes a direct night", `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, origin, vendor, record_id, record_state)
		VALUES ($1, now() - interval '6 hours', now() - interval '5 hours', 'deep', 'ryft-direct-fake', 'direct', 'fake', 'sleep:n-1', 'final')`, user)
	must(ingest, "ingest writes a direct sample", `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, origin, vendor, record_id)
		VALUES ($1, 'hrv_rmssd', now() - interval '6 hours', now() - interval '5 hours', 51, 'ms', 'ryft-direct-fake', 'direct', 'fake', 'hrv:h-1')`, user)
	must(ingest, "ingest keeps the direct source", `INSERT INTO user_sources (user_id, name, kind, last_seen, origin)
		VALUES ($1, 'ryft-direct-fake', 'sleep', now(), 'direct')`, user)
	must(ingest, "ingest stamps the connection", `UPDATE connections SET last_data_at = now() WHERE user_id = $1 AND provider = 'fake'
		AND status IN ('connected', 'needs_reauth')`, user)
	must(ingest, "ingest suspends on a permission change", `UPDATE connections SET status = 'needs_reauth', status_reason = 'permissions',
		status_at = now() WHERE user_id = $1 AND provider = 'fake' AND status = 'connected'`, user)
	must(ingest, "ingest restores only what a permission change suspended", `UPDATE connections SET status = 'connected', status_reason = NULL,
		status_at = now() WHERE user_id = $1 AND provider = 'fake' AND status = 'needs_reauth' AND status_reason = 'permissions'`, user)
	// P2-8: ingest's UPDATE on provider_user_id is for the revocation only; the guard refuses any other value,
	// as a grant would (42501), while an unchanged one passes.
	guarded := func(what, q string, args ...any) {
		t.Helper()
		var pgErr *pgconn.PgError
		if _, err := ingest.Exec(ctx, q, args...); !errors.As(err, &pgErr) || pgErr.Code != "42501" ||
			!strings.Contains(pgErr.Message, "may only clear connections.provider_user_id") {
			t.Errorf("%s: want the provider_user_id guard's refusal, got %v", what, err)
		}
	}
	guarded("ingest relinks to another provider user", `UPDATE connections SET provider_user_id = 'p-other'
		WHERE user_id = $1 AND provider = 'fake'`, user)
	guarded("ingest relinks along with a status", `UPDATE connections SET status = 'needs_reauth', status_reason = 'permissions',
		status_at = now(), provider_user_id = 'p-other' WHERE user_id = $1 AND provider = 'fake'`, user)
	must(ingest, "ingest writes provider_user_id unchanged", `UPDATE connections SET provider_user_id = provider_user_id, status_at = now()
		WHERE user_id = $1 AND provider = 'fake'`, user)
	if got := scalar[string](t, pool, `SELECT provider_user_id FROM connections WHERE user_id = $1 AND provider = 'fake'`, user); got != "p-1" {
		t.Fatalf("the guard let ingest move provider_user_id to %q", got)
	}
	// The deregistration notice: revoked with nothing kept (S6), the direct data deleted (S2).
	mustNot(ingest, "a revoked row keeps no provider user id (S6)", `UPDATE connections SET status = 'revoked', status_reason = 'provider_revoked',
		permissions = '{}' WHERE user_id = $1 AND provider = 'fake'`, user)
	mustNot(ingest, "a revoked row keeps no permissions (S6)", `UPDATE connections SET status = 'revoked', status_reason = 'provider_revoked',
		provider_user_id = NULL WHERE user_id = $1 AND provider = 'fake'`, user)
	must(ingest, "ingest marks a revocation", `UPDATE connections SET status = 'revoked', status_reason = 'provider_revoked', status_at = now(),
		provider_user_id = NULL, permissions = '{}' WHERE user_id = $1 AND provider = 'fake'`, user)
	guarded("ingest sets provider_user_id on a revoked row", `UPDATE connections SET status = 'connected', status_reason = NULL,
		provider_user_id = 'p-1', permissions = '{HEALTH_EXPORT}' WHERE user_id = $1 AND provider = 'fake'`, user)
	for _, tbl := range []string{"samples", "sleep_segments"} {
		if ct, err := ingest.Exec(ctx, `DELETE FROM `+tbl+` WHERE user_id = $1 AND origin = 'direct' AND vendor = 'fake'`, user); err != nil || ct.RowsAffected() != 1 {
			t.Fatalf("ingest deletes the direct %s: %v %v", tbl, ct, err)
		}
	}
	if ct, err := ingest.Exec(ctx, `DELETE FROM user_sources WHERE user_id = $1 AND origin = 'direct' AND name = 'ryft-direct-fake'`, user); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("ingest deletes the direct source (S2): %v %v", ct, err)
	}
	if n := scalar[int](t, pool, `SELECT (SELECT count(*) FROM samples WHERE origin = 'healthkit') + (SELECT count(*) FROM sleep_segments WHERE origin = 'healthkit')
		+ (SELECT count(*) FROM user_sources WHERE origin = 'healthkit')`); n != 3 {
		t.Errorf("the HealthKit rows must survive the direct delete, %d left", n)
	}

	// api: the inbox's bookkeeping, never a payload; the disconnect's and the deletion's deletes.
	if err := api.QueryRow(ctx, `SELECT count(*) FROM provider_inbox WHERE user_id = $1 AND provider = 'fake' AND state = 'processed'`, user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("api counts the inbox: %d %v", n, err)
	}
	mustNot(api, "api must not read a payload", `SELECT payload FROM provider_inbox`)
	mustNot(api, "api must not write the inbox", `UPDATE provider_inbox SET state = 'pending'`)
	mustNot(api, "api must not write raw data", `INSERT INTO samples (user_id, type, start_at, end_at, value, source, origin)
		VALUES ('`+user+`', 'hrv_rmssd', now(), now(), 1, 'ryft-direct-fake', 'direct')`)

	// scoring reads what the new columns say, writes the new score columns, sees nothing of the connection.
	if err := scoring.QueryRow(ctx, `SELECT count(*) FROM sleep_segments WHERE user_id = $1 AND origin = 'healthkit' AND vendor IS NULL
		AND record_state IS NULL`, user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("scoring reads origin, vendor and record_state: %d %v", n, err)
	}
	must(scoring, "scoring writes the coverage columns", `UPDATE daily_scores SET hrv_metric = 'sdnn', coverage = '{"sleep_origin": "healthkit"}',
		score_status = 'provisional', first_recovery_at = now() WHERE user_id = $1`, user)
	for _, tbl := range connectionTables {
		mustNot(scoring, "scoring reads "+tbl, `SELECT 1 FROM `+tbl)
		mustNot(notify, "notify reads "+tbl, `SELECT 1 FROM `+tbl)
	}
	mustNot(notify, "notify reads samples.origin", `SELECT origin FROM samples`)
	mustNot(notify, "notify reads daily_scores.hrv_metric", `SELECT hrv_metric FROM daily_scores`)
	var day int
	if err := notify.QueryRow(ctx, `SELECT count(*) FROM daily_scores WHERE user_id = $1 AND recovery IS NULL`, user).Scan(&day); err != nil || day != 1 {
		t.Fatalf("notify's own columns still read through RLS: %d %v", day, err)
	}

	checkConnectionShapes(t, pool, user, other, blob)

	if supabase {
		for _, tbl := range connectionTables {
			if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, "public."+tbl) {
				t.Errorf("RLS must be on for %s", tbl)
			}
			for _, role := range []string{"anon", "authenticated", "service_role"} {
				if scalar[bool](t, pool, `SELECT has_table_privilege($1, $2, 'SELECT')`, role, "public."+tbl) {
					t.Errorf("%s must not read %s", role, tbl)
				}
			}
		}
		for _, seq := range []string{"connection_deregistrations_id_seq", "provider_inbox_id_seq"} {
			for _, role := range []string{"anon", "authenticated", "service_role"} {
				if scalar[bool](t, pool, `SELECT has_sequence_privilege($1, $2, 'USAGE')`, role, "public."+seq) {
					t.Errorf("%s must not use %s", role, seq)
				}
			}
		}
	}

	// An image from before 0042 deletes the users row and none of these: the account's rows go with it, the
	// deregistration queue stays (it outlives the account; the api's tick drains it).
	must(api, "a new state", `INSERT INTO connection_states (state_hash, user_id, provider, verifier_enc, policy_version, expires_at)
		VALUES ($1, $2, 'fake', $3, '2026-11-02', now() + interval '10 minutes')`, blob(11, 32), user, blob(2, 40))
	// api is outside the P2-8 guard: its confirm sets provider_user_id on a revoked row.
	must(api, "a live connection again", `UPDATE connections SET status = 'connected', status_reason = NULL, provider_user_id = 'p-1',
		permissions = '{HEALTH_EXPORT}' WHERE user_id = $1`, user)
	for _, tbl := range []string{"provider_inbox", "connection_states", "connection_pending", "connection_tokens", "connections"} {
		if scalar[int](t, pool, `SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, user) == 0 {
			t.Fatalf("setup: %s holds no row of the account", tbl)
		}
	}
	for _, q := range []string{`DELETE FROM samples WHERE user_id = $1`, `DELETE FROM sleep_segments WHERE user_id = $1`,
		`DELETE FROM user_sources WHERE user_id = $1`, `DELETE FROM daily_scores WHERE user_id = $1`, `DELETE FROM users WHERE id = $1`} {
		must(api, "an old DeleteUser: "+q, q, user)
	}
	for _, tbl := range []string{"provider_inbox", "connection_states", "connection_pending", "connection_tokens", "connections"} {
		if n := scalar[int](t, pool, `SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, user); n != 0 {
			t.Errorf("the cascade leaves %d rows in %s", n, tbl)
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id = $1`, user); n != 1 {
		t.Errorf("the deregistration queue outlives the account: %d rows", n)
	}

	// The flag ships dark; a rerun of the file keeps the operator's setting and adds no policy.
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0 FROM feature_flags WHERE key = 'connect.garmin'`) {
		t.Error("connect.garmin ships enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET allow_user_ids = ARRAY[$1::uuid] WHERE key = 'connect.garmin'`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0042 a second time: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT allow_user_ids = ARRAY[$1::uuid] FROM feature_flags WHERE key = 'connect.garmin'`, other) {
		t.Error("a rerun of 0042 overwrote the flag")
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = ANY ($1)`, connectionTables); got != len(connectionTables) {
		t.Errorf("want one policy per new table after a rerun, got %d", got)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'public.samples'::regclass AND conname LIKE 'samples_origin_check%'`); n != 1 {
		t.Errorf("a rerun must not add a CHECK twice: %d", n)
	}
}

// wantRefusal wants err to be a refusal by a grant (42501) or by the schema (class 23: a CHECK, NOT NULL, a key),
// so a typo in a statement cannot pass for one.
func wantRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		t.Errorf("%s: must be refused", what)
	case !errors.As(err, &pgErr):
		t.Errorf("%s: %v", what, err)
	case pgErr.Code != "42501" && !strings.HasPrefix(pgErr.Code, "23"):
		t.Errorf("%s: refused for another reason: %v", what, err)
	}
}

// checkConnectionShapes runs as the owner, so a refusal is the schema's, not a grant's.
func checkConnectionShapes(t *testing.T, pool *pgxpool.Pool, user, other string, blob func(byte, int) []byte) {
	t.Helper()
	ctx := context.Background()
	conn := `INSERT INTO connections (user_id, provider, provider_user_id, status, status_reason, permissions, policy_version) VALUES `
	pending := `INSERT INTO connection_pending (user_id, provider, provider_user_id, confirm_hash, confirm_expires_at, access_enc, confirmed_at) VALUES `
	inbox := `INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload, payload_sha256, state, error) VALUES `
	for name, c := range map[string]struct {
		q    string
		args []any
	}{
		"provider with a capital":     {conn + `($1, 'Fake', 'p-2', 'connected', NULL, '{}', 'v')`, []any{other}},
		"provider of one letter":      {conn + `($1, 'f', 'p-2', 'connected', NULL, '{}', 'v')`, []any{other}},
		"unknown status":              {conn + `($1, 'fake', 'p-2', 'pending', NULL, '{}', 'v')`, []any{other}},
		"unknown status reason":       {conn + `($1, 'fake', 'p-2', 'needs_reauth', 'expired', '{}', 'v')`, []any{other}},
		"connected without a puid":    {conn + `($1, 'fake', NULL, 'connected', NULL, '{}', 'v')`, []any{other}},
		"revoked with a puid":         {conn + `($1, 'fake', 'p-2', 'revoked', 'relinked', '{}', 'v')`, []any{other}},
		"puid of 129":                 {conn + `($1, 'fake', repeat('a', 129), 'connected', NULL, '{}', 'v')`, []any{other}},
		"empty puid":                  {conn + `($1, 'fake', '', 'connected', NULL, '{}', 'v')`, []any{other}},
		"33 permissions":              {conn + `($1, 'fake', 'p-2', 'connected', NULL, array_fill('x'::text, ARRAY[33]), 'v')`, []any{other}},
		"empty policy version":        {conn + `($1, 'fake', 'p-2', 'connected', NULL, '{}', '')`, []any{other}},
		"connection for nobody":       {conn + `(gen_random_uuid(), 'fake', 'p-2', 'connected', NULL, '{}', 'v')`, nil},
		"tokens without connection":   {`INSERT INTO connection_tokens (user_id, provider, access_enc) VALUES ($1, 'fake', $2)`, []any{other, blob(1, 40)}},
		"token error as text":         {`UPDATE connection_tokens SET last_error = 'Bad Gateway' WHERE user_id = $1`, []any{user}},
		"confirm hash of 31":          {pending + `($1, 'fake', 'p-2', $2, now(), NULL, NULL)`, []any{other, blob(1, 31)}},
		"confirmed with a token":      {pending + `($1, 'fake', 'p-2', $2, now(), $3, now())`, []any{other, blob(1, 32), blob(2, 40)}},
		"pending without a puid":      {pending + `($1, 'fake', NULL, $2, now(), NULL, NULL)`, []any{other, blob(1, 32)}},
		"state hash of 33":            {`INSERT INTO connection_states (state_hash, user_id, provider, verifier_enc, policy_version, expires_at) VALUES ($1, $2, 'fake', $3, 'v', now())`, []any{blob(1, 33), other, blob(2, 40)}},
		"unknown deregister reason":   {`INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason) VALUES ($1, 'fake', 'p-2', 'expired')`, []any{other}},
		"deregistration without puid": {`INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason) VALUES ($1, 'fake', NULL, 'cap')`, []any{other}},
		"inbox kind with a digit":     {inbox + `('fake', 'sleep2', 'r', $1, '{}', $2, 'pending', NULL)`, []any{other, blob(1, 32)}},
		"record id of 129":            {inbox + `('fake', 'sleep', repeat('r', 129), $1, '{}', $2, 'pending', NULL)`, []any{other, blob(1, 32)}},
		"digest of 31":                {inbox + `('fake', 'sleep', 'r', $1, '{}', $2, 'pending', NULL)`, []any{other, blob(1, 31)}},
		"unknown inbox state":         {inbox + `('fake', 'sleep', 'r', $1, '{}', $2, 'queued', NULL)`, []any{other, blob(1, 32)}},
		"processed with a payload":    {inbox + `('fake', 'sleep', 'r', $1, '{}', $2, 'processed', NULL)`, []any{other, blob(1, 32)}},
		"dropped with a payload":      {inbox + `('fake', 'sleep', 'r', $1, '{}', $2, 'dropped', 'no_consent')`, []any{other, blob(1, 32)}},
		"inbox error as text":         {inbox + `('fake', 'sleep', 'r', $1, NULL, $2, 'failed', 'unexpected EOF')`, []any{other, blob(1, 32)}},
		"inbox row for nobody":        {inbox + `('fake', 'sleep', 'r', gen_random_uuid(), '{}', $1, 'pending', NULL)`, []any{blob(1, 32)}},
		"sample origin unknown":       {`INSERT INTO samples (user_id, type, start_at, end_at, value, source, origin) VALUES ($1, 'steps', now(), now(), 1, 'x', 'manual')`, []any{other}},
		"sample vendor capitalised":   {`INSERT INTO samples (user_id, type, start_at, end_at, value, source, vendor) VALUES ($1, 'steps', now(), now(), 1, 'x', 'Garmin')`, []any{other}},
		"sample record id of 162":     {`INSERT INTO samples (user_id, type, start_at, end_at, value, source, record_id) VALUES ($1, 'steps', now(), now(), 1, 'x', repeat('r', 162))`, []any{other}},
		"night state unknown":         {`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, record_state) VALUES ($1, now(), now(), 'core', 'x', 'draft')`, []any{other}},
		"source origin unknown":       {`INSERT INTO user_sources (user_id, name, kind, last_seen, origin) VALUES ($1, 'x', 'sleep', now(), 'cloud')`, []any{other}},
		"hrv metric unknown":          {`INSERT INTO daily_scores (user_id, day, hrv_metric) VALUES ($1, current_date, 'rr')`, []any{other}},
		"score status unknown":        {`INSERT INTO daily_scores (user_id, day, score_status) VALUES ($1, current_date, 'done')`, []any{other}},
	} {
		_, err := pool.Exec(ctx, c.q, c.args...)
		wantRefusal(t, name, err)
	}

	// B1: one live link per provider account; a revoked row and a pending grant are outside it.
	for _, q := range []string{
		conn + `('` + other + `', 'fake', 'p-shared', 'connected', NULL, '{}', 'v')`,
		conn + `('` + user + `', 'other', 'p-shared', 'connected', NULL, '{}', 'v')`, // another provider, the same id
		conn + `('` + user + `', 'third', NULL, 'revoked', 'relinked', '{}', 'v')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := pool.Exec(ctx, pending+`($1, 'fake', 'p-shared', $2, now(), $3, NULL)`, other, blob(3, 32), blob(4, 40)); err != nil {
		t.Errorf("a pending grant of a live provider account is allowed (an unconfirmed reconnect, B1): %v", err)
	}
	// The account's revoked fake row coexists with the other account's live link; made live again, it conflicts.
	if _, err := pool.Exec(ctx, `UPDATE connections SET status = 'needs_reauth', status_reason = 'refresh_failed', provider_user_id = 'p-shared'
		WHERE user_id = $1 AND provider = 'fake' AND status = 'revoked'`, user); err == nil {
		t.Error("a second live link of one provider account must be refused")
	} else if !strings.Contains(err.Error(), "connections_provider_user") {
		t.Errorf("refused for another reason: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM connections WHERE provider_user_id = 'p-shared' OR provider = 'third'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM connection_pending WHERE provider_user_id = 'p-shared'`); err != nil {
		t.Fatal(err)
	}
	// A failed row keeps its payload for the 7 days; a re-push brings any row back to pending.
	if _, err := pool.Exec(ctx, inbox+`('fake', 'hrv', 'h-9', $1, '{}', $2, 'failed', 'parse')`, other, blob(5, 32)); err != nil {
		t.Errorf("a failed row keeps its payload: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM provider_inbox WHERE record_id = 'h-9'`); err != nil {
		t.Fatal(err)
	}
}
