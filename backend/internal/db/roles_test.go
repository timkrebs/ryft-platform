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
	betaTables    = []string{"consents", "beta_invite_codes", "user_sources", "feedback", "admin_audit"}
)

// trainTables are migration 0011's.
var trainTables = []string{"exercises", "plans", "plan_days", "plan_exercises", "plan_enrollments",
	"sessions", "session_exercises", "session_sets", "personal_records"}

var grants = map[string]map[string][]string{
	"ryft_ingest": {
		"users": {"SELECT", "INSERT", "UPDATE"}, "devices": {"SELECT", "INSERT", "UPDATE"},
		"samples": {"SELECT", "INSERT", "UPDATE"}, "sleep_segments": {"SELECT", "INSERT", "UPDATE"},
		"workouts": {"SELECT", "INSERT", "UPDATE"}, "schema_migrations": {"SELECT"}, "consents": {"SELECT"},
		"user_sources": {"SELECT", "INSERT", "UPDATE"},
		// deleted_users: only the id column (below), never the stored Apple token
	},
	"ryft_scoring": {
		"users": {"SELECT"}, "devices": {"SELECT"}, "samples": {"SELECT"}, "sleep_segments": {"SELECT"},
		"workouts": {"SELECT", "DELETE"}, "daily_scores": {"SELECT", "INSERT", "UPDATE"}, "schema_migrations": {"SELECT"},
		"exercises": {"SELECT"}, "sessions": {"SELECT"}, "session_exercises": {"SELECT"}, "session_sets": {"SELECT"},
		"consents": {"SELECT"},
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
		"admin_audit": {"SELECT", "INSERT"},
		"exercises":   crud, "plans": crud, "plan_days": crud, "plan_exercises": crud, "plan_enrollments": crud,
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
	if _, err := asRoleErr(dsn, "ryft_api", "wrong-password"); err == nil {
		t.Fatal("a wrong password must fail")
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
