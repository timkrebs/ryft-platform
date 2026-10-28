package db

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pinned against an independent computation (Python hashlib/hmac) of RFC 7677.
func TestScramVerifier(t *testing.T) {
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	got, err := scramVerifier("pencil", salt, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := "SCRAM-SHA-256$4096:AAECAwQFBgcICQoLDA0ODw==$zHCdol2044/ZyWzPLi7oxApCkamKw9Z+E4U/QApd/5Y=:dd5peBOitVnLNFu7VmwP+HiDaaw4OUCv396eVCWhYiE="
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	for _, bad := range []string{"with space", "tab\tx", "ümlaut", ""} {
		if bad == "" {
			continue // SetRolePassword rejects empty passwords before this
		}
		if _, err := scramVerifier(bad, salt, 4096); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	a, _ := scramVerifier("pencil", nil, 4096)
	b, _ := scramVerifier("pencil", nil, 4096)
	if a == b {
		t.Fatal("a nil salt must be random")
	}
}

// grants is what each role may do (migrations 0009–0015); everything else is denied.
// Column grants (scoring's on workouts, notify's reads) are checked separately.
var crud = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}

// journalTables are migration 0016's, betaTables 0017's onwards.
var (
	journalTables = []string{"behaviors", "user_behaviors", "journal_days", "journal_entries"}
	betaTables    = []string{"consents", "beta_invite_codes", "user_sources", "feedback", "admin_audit", "score_queue", "outbox"}
)

// trainTables are migration 0011's.
var trainTables = []string{"exercises", "plans", "plan_days", "plan_exercises", "plan_enrollments",
	"sessions", "session_exercises", "session_sets", "personal_records"}

var grants = map[string]map[string][]string{
	"ryft_ingest": {
		"users": {"SELECT", "INSERT", "UPDATE"}, "devices": {"SELECT", "INSERT", "UPDATE"},
		"samples": {"SELECT", "INSERT", "UPDATE"}, "sleep_segments": {"SELECT", "INSERT", "UPDATE"},
		"workouts": {"SELECT", "INSERT", "UPDATE"}, "schema_migrations": {"SELECT"}, "consents": {"SELECT"},
		"user_sources": {"SELECT", "INSERT", "UPDATE"}, "score_queue": {"SELECT", "INSERT", "UPDATE"},
		"outbox": {"SELECT", "INSERT", "DELETE"}, // and UPDATE of the relay's columns only (below)
		// deleted_users: only the id column (below), never the stored Apple token
	},
	"ryft_scoring": {
		"users": {"SELECT"}, "devices": {"SELECT"}, "samples": {"SELECT"}, "sleep_segments": {"SELECT"},
		"workouts": {"SELECT", "DELETE"}, "daily_scores": {"SELECT", "INSERT", "UPDATE"}, "schema_migrations": {"SELECT"},
		"exercises": {"SELECT"}, "sessions": {"SELECT"}, "session_exercises": {"SELECT"}, "session_sets": {"SELECT"},
		"consents": {"SELECT"}, "score_queue": {"SELECT", "DELETE"}, "outbox": {"SELECT", "INSERT", "DELETE"},
	},
	"ryft_notify": {
		"push_tokens": {"SELECT", "DELETE"}, "notifications_sent": crud, "personal_records": {"SELECT"},
		"schema_migrations": {"SELECT"},
	},
	"ryft_api": {
		"users": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "devices": {"SELECT", "DELETE"},
		"samples": {"SELECT", "DELETE"}, "sleep_segments": {"SELECT", "DELETE"}, "workouts": {"SELECT", "DELETE"},
		"daily_scores": {"SELECT", "DELETE"}, "deleted_users": {"SELECT", "INSERT", "UPDATE"},
		"schema_migrations": {"SELECT"}, "apple_tokens": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"feature_flags": crud, "push_tokens": crud, "notifications_sent": {"SELECT", "DELETE"},
		"behaviors": crud, "user_behaviors": crud, "journal_days": crud, "journal_entries": crud,
		"consents": crud, "beta_invite_codes": crud, "user_sources": crud, "feedback": crud,
		"admin_audit": {"SELECT", "INSERT"}, "score_queue": {"SELECT", "DELETE"}, "outbox": {"SELECT", "INSERT", "DELETE"},
		"exercises": crud, "plans": crud, "plan_days": crud, "plan_exercises": crud, "plan_enrollments": crud,
		"sessions": crud, "session_exercises": crud, "session_sets": crud, "personal_records": crud,
	},
}

func TestServiceRoleGrants(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"users", "devices", "samples", "sleep_segments", "workouts", "daily_scores",
		"deleted_users", "schema_migrations", "apple_tokens", "feature_flags", "push_tokens", "notifications_sent"},
		append(append(trainTables, journalTables...), betaTables...)...)
	for role, allowed := range grants {
		for _, tbl := range tables {
			for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
				var has bool
				if err := pool.QueryRow(ctx, `SELECT has_table_privilege($1, $2, $3)`, role, "public."+tbl, priv).Scan(&has); err != nil {
					t.Fatal(err)
				}
				want := false
				for _, p := range allowed[tbl] {
					want = want || p == priv
				}
				if has != want {
					t.Errorf("%s %s on %s: %v, want %v", role, priv, tbl, has, want)
				}
			}
		}
		var create bool
		if err := pool.QueryRow(ctx, `SELECT has_schema_privilege($1, 'public', 'CREATE')`, role).Scan(&create); err != nil || create {
			t.Errorf("%s must not create objects in public (err %v)", role, err)
		}
	}
	// ingest sees whether an ID is tombstoned, not what the tombstone holds.
	for col, want := range map[string]bool{"id": true, "apple_token_enc": false, "deleted_at": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_ingest', 'public.deleted_users', $1, 'SELECT')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_ingest SELECT deleted_users.%s: %v, want %v", col, has, want)
		}
	}
	// scoring may write only the load columns of a workout, and of a session.
	for col, want := range map[string]bool{"strain": true, "trimp": true, "zone_minutes": true, "muscular_load": true,
		"session_id": true, "model_version": true, "start_at": false, "avg_hr": false, "activity": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_scoring', 'public.workouts', $1, 'UPDATE')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_scoring UPDATE workouts.%s: %v, want %v", col, has, want)
		}
	}
	// api may move a merged account's raw data: the owner column only (0024, D90).
	for _, table := range []string{"samples", "sleep_segments", "workouts", "devices"} {
		for col, want := range map[string]bool{"user_id": true, "start_at": false, "value": false, "last_sync_at": false} {
			var exists, has bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
				table, col).Scan(&exists); err != nil || !exists {
				continue
			}
			if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_api', 'public.'||$1, $2, 'UPDATE')`, table, col).Scan(&has); err != nil {
				t.Fatal(err)
			}
			if has != want {
				t.Errorf("ryft_api UPDATE %s.%s: %v, want %v", table, col, has, want)
			}
		}
	}
	// scoring may only mark a queued day done (0025).
	// D101: each publisher's relay may mark its rows, and change nothing else of them.
	for _, role := range []string{"ryft_ingest", "ryft_api", "ryft_scoring", "ryft_notify"} {
		for col, want := range map[string]bool{"published_at": true, "attempts": true, "last_error": true, "payload": false, "routing_key": false} {
			if role == "ryft_notify" {
				want = false
			}
			var has bool
			if err := pool.QueryRow(ctx, `SELECT has_column_privilege($1, 'public.outbox', $2, 'UPDATE')`, role, col).Scan(&has); err != nil {
				t.Fatal(err)
			}
			if has != want {
				t.Errorf("%s UPDATE outbox.%s: %v, want %v", role, col, has, want)
			}
		}
	}
	for col, want := range map[string]bool{"scored_at": true, "queued_at": false, "day": false, "user_id": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_scoring', 'public.score_queue', $1, 'UPDATE')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_scoring UPDATE score_queue.%s: %v, want %v", col, has, want)
		}
	}
	// notify may only stamp a delivered push on its token (0023).
	for col, want := range map[string]bool{"last_success_at": true, "token": false, "env": false, "user_id": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_notify', 'public.push_tokens', $1, 'UPDATE')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_notify UPDATE push_tokens.%s: %v, want %v", col, has, want)
		}
	}
	// notify reads a user's time zone and notification settings, nothing else (0015).
	for col, want := range map[string]bool{"id": true, "tz": true, "notify_categories": true, "quiet_from": true,
		"quiet_to": true, "email": false, "name": false, "sleep_base_min": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_notify', 'public.users', $1, 'SELECT')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_notify SELECT users.%s: %v, want %v", col, has, want)
		}
	}
	// 0027: scoring writes why a day calibrates, the api reads it, notify and
	// ingest do not see it (they have no business with it).
	for _, col := range []string{"baseline_hrv_days", "baseline_rhr_days", "calibrating_reason"} {
		for _, c := range []struct {
			role, priv string
			want       bool
		}{
			{"ryft_scoring", "SELECT", true}, {"ryft_scoring", "INSERT", true}, {"ryft_scoring", "UPDATE", true},
			{"ryft_api", "SELECT", true}, {"ryft_api", "UPDATE", false}, {"ryft_api", "INSERT", false},
			{"ryft_notify", "SELECT", false}, {"ryft_ingest", "SELECT", false},
		} {
			var has bool
			if err := pool.QueryRow(ctx, `SELECT has_column_privilege($1, 'public.daily_scores', $2, $3)`, c.role, col, c.priv).Scan(&has); err != nil {
				t.Fatal(err)
			}
			if has != c.want {
				t.Errorf("%s %s daily_scores.%s: %v, want %v", c.role, c.priv, col, has, c.want)
			}
		}
	}
	// 0028: the api queues the days of an import, in full; notify reads the origin of a session
	// and nothing the import adds; only the api writes the new columns.
	for _, c := range []struct {
		role, table, col, priv string
		want                   bool
	}{
		{"ryft_api", "score_queue", "scored_at", "UPDATE", true}, {"ryft_api", "score_queue", "queued_at", "UPDATE", true},
		{"ryft_api", "score_queue", "user_id", "UPDATE", false}, {"ryft_api", "score_queue", "day", "UPDATE", false},
		{"ryft_api", "score_queue", "user_id", "INSERT", true}, {"ryft_api", "score_queue", "day", "INSERT", true},
		{"ryft_api", "sessions", "imported_at", "UPDATE", true}, {"ryft_api", "plans", "imported_at", "UPDATE", true},
		{"ryft_scoring", "sessions", "imported_at", "UPDATE", false}, {"ryft_notify", "sessions", "imported_at", "SELECT", false},
		{"ryft_notify", "sessions", "origin", "SELECT", true}, {"ryft_notify", "sessions", "import_source", "SELECT", false},
		{"ryft_notify", "sessions", "origin", "UPDATE", false}, {"ryft_notify", "session_sets", "assist_kg", "SELECT", false},
		{"ryft_scoring", "sessions", "origin", "SELECT", true}, {"ryft_scoring", "sessions", "origin", "UPDATE", false},
		{"ryft_scoring", "sessions", "import_source", "UPDATE", false}, {"ryft_scoring", "session_sets", "assist_kg", "SELECT", true},
		{"ryft_scoring", "session_sets", "assist_kg", "UPDATE", false},
		{"ryft_scoring", "users", "import_lease_until", "UPDATE", false}, {"ryft_scoring", "users", "import_lease_until", "SELECT", true},
		{"ryft_notify", "users", "import_lease_until", "SELECT", false}, {"ryft_ingest", "users", "import_lease_until", "UPDATE", true},
		{"ryft_api", "sessions", "origin", "INSERT", true}, {"ryft_api", "session_sets", "assist_kg", "UPDATE", true},
		{"ryft_api", "users", "import_lease_until", "UPDATE", true},
	} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege($1, $2, $3, $4)`, c.role, "public."+c.table, c.col, c.priv).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != c.want {
			t.Errorf("%s %s %s.%s: %v, want %v", c.role, c.priv, c.table, c.col, has, c.want)
		}
	}
	// scoring writes the learned sleep baseline (0013), nothing else of a user.
	for col, want := range map[string]bool{"sleep_base_min": true, "sleep_base_source": true, "sleep_base_learned_min": true,
		"sleep_base_learned_at": true, "sleep_base_manual": false, "tz": false, "max_hr": false, "email": false} {
		var has bool
		if err := pool.QueryRow(ctx, `SELECT has_column_privilege('ryft_scoring', 'public.users', $1, 'UPDATE')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_scoring UPDATE users.%s: %v, want %v", col, has, want)
		}
	}
}

// asRole connects to the pool's database as role with password.
func asRole(t *testing.T, poolURL, role, password string) *pgx.Conn {
	t.Helper()
	u, err := url.Parse(poolURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, password)
	c, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// On the Supabase shape RLS is on; the roles must still see and write rows
// through their policy, and the password set as a SCRAM verifier must work.
func TestServiceRolesLogInAndPassRLS(t *testing.T) {
	pool := freshDB(t, true)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := SetRolePassword(ctx, pool, role, "pw-"+role+"-0123456789"); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetRolePassword(ctx, pool, "postgres", "x"); err == nil {
		t.Fatal("only service roles may be changed")
	}
	dsn := pool.Config().ConnString()
	id := "11111111-2222-3333-4444-555555555555"

	ingest := asRole(t, dsn, "ryft_ingest", "pw-ryft_ingest-0123456789")
	if _, err := ingest.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, id); err != nil {
		t.Fatalf("ingest insert through RLS: %v", err)
	}
	if _, err := ingest.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		VALUES ($1, 'steps', now(), now(), 1, 'count', 't')`, id); err != nil {
		t.Fatalf("ingest sample: %v", err)
	}
	if _, err := ingest.Exec(ctx, `DELETE FROM samples`); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("ingest must not delete: %v", err)
	}
	if _, err := ingest.Exec(ctx, `CREATE TABLE evil (x int)`); err == nil {
		t.Fatal("ingest must not create tables")
	}

	scoring := asRole(t, dsn, "ryft_scoring", "pw-ryft_scoring-0123456789")
	var n int
	if err := scoring.QueryRow(ctx, `SELECT count(*) FROM samples WHERE user_id=$1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("scoring must read the sample through RLS: n=%d err=%v", n, err)
	}
	if _, err := scoring.Exec(ctx, `INSERT INTO daily_scores (user_id, day) VALUES ($1, current_date)`, id); err != nil {
		t.Fatalf("scoring writes scores: %v", err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE users SET tz='UTC'`); err == nil {
		t.Fatal("scoring must not change users")
	}
	if _, err := scoring.Exec(ctx, `UPDATE users SET sleep_base_learned_min=480, sleep_base_learned_at=now() WHERE id=$1`, id); err != nil {
		t.Fatalf("scoring stores the learned sleep baseline: %v", err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE users SET sleep_base_manual=false WHERE id=$1`, id); err == nil {
		t.Fatal("only the user turns the manual baseline off")
	}

	notify := asRole(t, dsn, "ryft_notify", "pw-ryft_notify-0123456789")
	var last *time.Time
	if err := notify.QueryRow(ctx, `SELECT max(start_at) FROM samples WHERE user_id=$1 AND type='steps'`, id).Scan(&last); err != nil || last == nil {
		t.Fatalf("notify reads when the last sample arrived, through RLS: %v %v", last, err)
	}
	var tz string
	if err := notify.QueryRow(ctx, `SELECT tz FROM users WHERE id=$1`, id).Scan(&tz); err != nil {
		t.Fatalf("notify reads the user's time zone: %v", err)
	}
	if _, err := notify.Exec(ctx, `INSERT INTO notifications_sent (user_id, day, category) VALUES ($1, current_date, 'MORNING_SCORE')`, id); err != nil {
		t.Fatalf("notify claims a send: %v", err)
	}
	for _, q := range []string{`SELECT email FROM users`, `SELECT value FROM samples`, `UPDATE users SET tz='UTC'`,
		`INSERT INTO push_tokens (user_id, device_id, token, env) VALUES ('` + id + `', gen_random_uuid(), repeat('a', 64), 'sandbox')`} {
		if _, err := notify.Exec(ctx, q); err == nil {
			t.Fatalf("notify must not: %s", q)
		}
	}

	api := asRole(t, dsn, "ryft_api", "pw-ryft_api-0123456789")
	if _, err := api.Exec(ctx, `DELETE FROM samples WHERE user_id=$1`, id); err != nil {
		t.Fatalf("api deletes an account's rows: %v", err)
	}
	if _, err := api.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		VALUES ($1, 'steps', now(), now(), 1, 'count', 't')`, id); err == nil {
		t.Fatal("api must not write raw data")
	}
	// 0028: the api queues the days of an import and holds the user's import lease, through RLS;
	// scoring marks them scored but cannot queue them; notify reads a session's origin only.
	if _, err := api.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at)
		VALUES ($1, '2026-10-18', now(), NULL) ON CONFLICT (user_id, day) DO UPDATE SET queued_at = now(), scored_at = NULL`, id); err != nil {
		t.Fatalf("api queues days: %v", err)
	}
	if _, err := api.Exec(ctx, `UPDATE users SET import_lease_until = now() + interval '2 minutes' WHERE id=$1 AND import_lease_until IS NULL`, id); err != nil {
		t.Fatalf("api takes the import lease: %v", err)
	}
	if _, err := scoring.Exec(ctx, `INSERT INTO score_queue (user_id, day) VALUES ($1, '2026-10-19')`, id); err == nil {
		t.Fatal("scoring must not queue days")
	}
	// The api re-queues a day; it cannot move a queue row to another user or day.
	if _, err := api.Exec(ctx, `UPDATE score_queue SET user_id = user_id WHERE user_id = $1`, id); err == nil {
		t.Fatal("api must not update score_queue.user_id")
	}
	if _, err := api.Exec(ctx, `UPDATE score_queue SET day = day + 1 WHERE user_id = $1`, id); err == nil {
		t.Fatal("api must not update score_queue.day")
	}
	if _, err := api.Exec(ctx, `INSERT INTO sessions (user_id, id, started_at, status, origin, import_source)
		VALUES ($1, gen_random_uuid(), now(), 'completed', 'import', 'alpha_progression')`, id); err != nil {
		t.Fatalf("api stores an imported session: %v", err)
	}
	if err := notify.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND origin = 'import'`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("notify reads a session's origin through RLS: n=%d err=%v", n, err)
	}
	if _, err := notify.Exec(ctx, `SELECT import_source FROM sessions`); err == nil {
		t.Fatal("notify must not read import_source")
	}
	if _, err := asRoleErr(dsn, "ryft_api", "wrong-password"); err == nil {
		t.Fatal("a wrong password must fail")
	}
}

// D93: migrate rewrites a role's verifier only when its password changed; the
// same password again leaves it (and its salt, which the pooler caches) alone.
func TestEnsureRolePassword(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	// ryft_api: no other package's tests log in as it (roles are cluster-wide).
	const role, pw, rotated = "ryft_api", "pw-ryft_api-0123456789", "pw-ryft_api-rotated-0123"
	dsn := pool.Config().ConnString()
	verifier := func() string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname = $1`, role).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	ensure := func(step, password string, want bool) {
		t.Helper()
		changed, err := EnsureRolePassword(ctx, pool, dsn, role, password)
		if err != nil || changed != want {
			t.Fatalf("%s: changed %v, err %v; want changed %v", step, changed, err, want)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER ROLE ryft_api NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	ensure("NOLOGIN, as migration 0009 creates it", pw, true)
	set := verifier()
	ensure("the same password", pw, false)
	if verifier() != set {
		t.Fatal("the same password must leave the verifier (and its salt) as it is")
	}
	ensure("a new password", rotated, true)
	if _, err := asRoleErr(dsn, role, pw); err == nil {
		t.Fatal("the old password must no longer log in")
	}
	_ = asRole(t, dsn, role, rotated)
	ensure("back", pw, true)
	if _, err := EnsureRolePassword(ctx, pool, dsn, "postgres", "x"); err == nil {
		t.Fatal("only service roles may be changed")
	}
	if _, err := EnsureRolePassword(ctx, pool, dsn, role, ""); err == nil {
		t.Fatal("an empty password is refused")
	}
}

func TestPoolerUser(t *testing.T) {
	for owner, want := range map[string]string{"postgres.ydgbemufggwnegbnjhkm": "ryft_api.ydgbemufggwnegbnjhkm", "postgres": "ryft_api"} {
		if got := poolerUser(owner, "ryft_api"); got != want {
			t.Errorf("%s: %s, want %s", owner, got, want)
		}
	}
}

func asRoleErr(poolURL, role, password string) (*pgx.Conn, error) {
	u, _ := url.Parse(poolURL)
	u.User = url.UserPassword(role, password)
	return pgx.Connect(context.Background(), u.String())
}

func TestScoringWritesOnlySessionLoad(t *testing.T) {
	pool := freshDB(t, false)
	if err := Migrate(context.Background(), pool, quiet); err != nil {
		t.Fatal(err)
	}
	for col, want := range map[string]bool{"muscular_trimp": true, "strain": true, "volume_kg": true, "workout_id": true,
		"status": false, "started_at": false, "title": false} {
		var has bool
		if err := pool.QueryRow(context.Background(), `SELECT has_column_privilege('ryft_scoring', 'public.sessions', $1, 'UPDATE')`, col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("ryft_scoring UPDATE sessions.%s: %v, want %v", col, has, want)
		}
	}
}
