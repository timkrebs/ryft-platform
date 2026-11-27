package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/testsupport"
)

// 0031: duplicate_of is nullable (old rows stay NULL), refuses a self-reference, has no foreign key
// (merge moves workouts by user_id), and only scoring may write it. Run as the service roles, not as the
// superuser: a statement that works for the owner can still be refused for a role.
func TestMigrateWorkoutDuplicates(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateWorkoutDuplicates(t, supabase) })
	}
}

func testMigrateWorkoutDuplicates(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 28); err != nil {
		t.Fatal(err)
	}
	u := "dddddddd-0000-0000-0000-000000000031"
	w1, w2, w3 := "aaaaaaaa-0000-0000-0000-000000000031", "aaaaaaaa-0000-0000-0000-000000000032", "aaaaaaaa-0000-0000-0000-000000000033"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + u + `')`,
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES
			('` + w1 + `', '` + u + `', 'run', now(), now(), 'Watch'),
			('` + w2 + `', '` + u + `', 'run', now(), now(), 'Phone')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM workouts WHERE duplicate_of IS NULL`); n != 2 {
		t.Errorf("rows from before 0031 read duplicate_of NULL, got %d NULL rows of 2", n)
	}
	if scalar[bool](t, pool, `SELECT convalidated = false FROM pg_constraint WHERE conname = 'workouts_duplicate_of_not_self'`) {
		t.Error("the CHECK must be validated")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'workouts'::regclass AND contype = 'f' AND confrelid = 'workouts'::regclass`); n != 0 {
		t.Errorf("duplicate_of must have no self foreign key, got %d", n)
	}

	for _, role := range []string{"ryft_ingest", "ryft_scoring", "ryft_api", "ryft_notify"} {
		if err := testsupport.WithRoleLock(ctx, func() error { return SetRolePassword(ctx, pool, role, testsupport.RolePassword(role)) }); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pool.Config().ConnString()
	scoring := asRole(t, dsn, "ryft_scoring", testsupport.RolePassword("ryft_scoring"))
	api := asRole(t, dsn, "ryft_api", testsupport.RolePassword("ryft_api"))
	ingest := asRole(t, dsn, "ryft_ingest", testsupport.RolePassword("ryft_ingest"))

	// Scoring sets and clears it, pointing at a workout that need not exist (no FK).
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, u, w2, w1); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring sets duplicate_of: %v %v", ct, err)
	}
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, u, w2, w3); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring points at a workout that is gone (no FK): %v %v", ct, err)
	}
	if ct, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1 AND id = $2`, u, w2); err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("scoring clears duplicate_of: %v %v", ct, err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = id WHERE user_id = $1 AND id = $2`, u, w2); err == nil {
		t.Error("duplicate_of = id must be refused by the CHECK")
	}
	// Scoring's other columns stay as they were (0009, 0011).
	for _, q := range []string{
		`UPDATE workouts SET source = 'x' WHERE user_id = $1`,
		`UPDATE workouts SET avg_hr = 1 WHERE user_id = $1`, // start_at and end_at: since 0065, for a session's own workout
		`UPDATE workouts SET activity = 'swim' WHERE user_id = $1`,
		`UPDATE workouts SET user_id = user_id WHERE user_id = $1`,
	} {
		if _, err := scoring.Exec(ctx, q, u); err == nil {
			t.Errorf("scoring must not run %q", q)
		}
	}
	// 0065: scoring has no grant on a workout's times, not even a session's own workout's; it moves the times of
	// a session's own workout (so its rating stays) through a function that takes none and reads them from the
	// owner's completed session.
	own, other, notDone, foreign := "aaaaaaaa-0000-0000-0000-000000000034", "aaaaaaaa-0000-0000-0000-000000000035",
		"aaaaaaaa-0000-0000-0000-000000000036", "aaaaaaaa-0000-0000-0000-000000000037"
	u2 := "dddddddd-0000-0000-0000-000000000032"
	s1, s2, s3 := "bbbbbbbb-0000-0000-0000-000000000031", "bbbbbbbb-0000-0000-0000-000000000032", "bbbbbbbb-0000-0000-0000-000000000033"
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ('` + u2 + `')`,
		`INSERT INTO sessions (user_id, id, started_at, ended_at, status) VALUES
			('` + u + `', '` + s1 + `', '2026-11-25 17:00+00', '2026-11-25 18:00+00', 'completed'),
			('` + u + `', '` + s2 + `', '2026-11-25 19:00+00', NULL, 'in_progress'),
			('` + u2 + `', '` + s3 + `', '2026-11-25 20:00+00', '2026-11-25 21:00+00', 'completed')`,
		// own: u's session workout of a completed session; notDone: of an unfinished one; other: u2's, of u2's session;
		// foreign: u's workout that names u2's session (the pair is no session of u's).
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, session_id) VALUES
			('` + own + `', '` + u + `', 'strength', '2026-11-25 10:00+00', '2026-11-25 10:30+00', 'ryft-session', '` + s1 + `'),
			('` + notDone + `', '` + u + `', 'strength', '2026-11-25 10:00+00', '2026-11-25 10:30+00', 'ryft-session', '` + s2 + `'),
			('` + other + `', '` + u2 + `', 'strength', '2026-11-25 10:00+00', '2026-11-25 10:30+00', 'ryft-session', '` + s3 + `'),
			('` + foreign + `', '` + u + `', 'strength', '2026-11-25 10:00+00', '2026-11-25 10:30+00', 'ryft-session', '` + s3 + `')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range []string{
		`UPDATE workouts SET start_at = now() WHERE user_id = $1 AND id = $2`, `UPDATE workouts SET end_at = now() WHERE user_id = $1 AND id = $2`,
		`UPDATE workouts SET start_at = start_at, end_at = end_at WHERE user_id = $1 AND id = $2`,
	} {
		for _, id := range []string{own, w1} { // a session's own workout and a HealthKit workout alike
			if _, err := scoring.Exec(ctx, q, u, id); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("scoring has no direct grant on the times (%q on %s): %v", q, id, err)
			}
		}
	}
	for _, role := range []string{"ryft_scoring", "ryft_ingest"} {
		if got := scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.workouts', 'start_at', 'UPDATE') AND has_column_privilege($1, 'public.workouts', 'end_at', 'UPDATE')`, role); got != (role == "ryft_ingest") {
			t.Errorf("%s UPDATE workouts.start_at and end_at: %v", role, got)
		}
	}
	follow := func(who interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}, user, workout string) (bool, error) {
		var moved bool
		err := who.QueryRow(ctx, `SELECT public.scoring_follow_session_times($1, $2)`, user, workout).Scan(&moved)
		return moved, err
	}
	times := func(user, workout string) string {
		return scalar[string](t, pool, `SELECT to_char(start_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') || ' ' || to_char(end_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')
			FROM workouts WHERE user_id = $1 AND id = $2`, user, workout)
	}
	if moved, err := follow(scoring, u, own); err != nil || !moved {
		t.Fatalf("scoring moves the own workout of a completed session: %v %v", moved, err)
	}
	if got, want := times(u, own), "2026-11-25 17:00:00 2026-11-25 18:00:00"; got != want {
		t.Errorf("the workout follows its session's times: %q, want %q", got, want)
	}
	// What it must not touch: a HealthKit workout, a workout of an unfinished session, another user's workout and a
	// workout that names a session of another user (nothing moves, and nothing says why).
	for _, c := range []struct{ name, user, workout, owner string }{
		{"a HealthKit workout", u, w1, u}, {"a workout of an unfinished session", u, notDone, u},
		{"another user's workout under this user's id", u, other, u2},
		{"another user's workout under that user's id and a session that is not theirs", u2, foreign, u},
		{"a workout that names another user's session", u, foreign, u},
		{"a workout that does not exist", u, "aaaaaaaa-0000-0000-0000-0000000000ff", ""},
	} {
		before := ""
		if c.owner != "" {
			before = times(c.owner, c.workout)
		}
		if moved, err := follow(scoring, c.user, c.workout); err != nil || moved {
			t.Errorf("%s: moved %v, %v", c.name, moved, err)
		}
		if c.owner != "" {
			if got := times(c.owner, c.workout); got != before {
				t.Errorf("%s: the times changed, %q, want %q", c.name, got, before)
			}
		}
	}
	// Execute is scoring's alone: not PUBLIC, not the other service roles.
	for _, c := range []struct {
		role string
		want bool
	}{{"ryft_scoring", true}, {"ryft_ingest", false}, {"ryft_api", false}, {"ryft_notify", false}} {
		if got := scalar[bool](t, pool, `SELECT has_function_privilege($1, 'public.scoring_follow_session_times(uuid, uuid)', 'EXECUTE')`, c.role); got != c.want {
			t.Errorf("%s EXECUTE scoring_follow_session_times: %v, want %v", c.role, got, c.want)
		}
	}
	if _, err := follow(api, u, own); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the api must not run the function: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'scoring_follow_session_times' AND prosecdef AND proconfig = ARRAY['search_path=pg_catalog']`); n != 1 {
		t.Errorf("the function is SECURITY DEFINER with a fixed search_path: %d", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname = 'workouts_scoring_moves_own_only'`); n != 0 {
		t.Errorf("the trigger of the earlier draft is gone: %d", n)
	}
	if _, err := ingest.Exec(ctx, `UPDATE workouts SET start_at = start_at WHERE user_id = $1 AND id = $2`, u, w1); err != nil {
		t.Errorf("ingest's upsert moves HealthKit workouts: %v", err)
	}
	// The api reads it and cannot write it; ingest reads it too (and see below for its UPDATE).
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $2 WHERE user_id = $1 AND id = $3`, u, w1, w2); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, pool, `SELECT count(*) FROM workouts WHERE duplicate_of = $1`, w1); got != 1 {
		t.Fatalf("setup: %d", got)
	}
	var seen int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, u).Scan(&seen); err != nil || seen != 1 {
		t.Errorf("api reads duplicate_of: %d %v", seen, err)
	}
	if _, err := api.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1`, u); err == nil {
		t.Error("api must not write duplicate_of")
	}
	if _, err := api.Exec(ctx, `UPDATE workouts SET user_id = user_id WHERE user_id = $1`, u); err != nil {
		t.Errorf("api keeps UPDATE (user_id) for the merge: %v", err)
	}
	// Ingest holds table-level UPDATE on workouts since 0009, which covers the new column; Postgres cannot
	// narrow that without revoking and re-granting columns (not additive). Its code never names the
	// column; this pins the fact so a later narrowing is a conscious change.
	if _, err := ingest.Exec(ctx, `UPDATE workouts SET duplicate_of = duplicate_of WHERE user_id = $1`, u); err != nil {
		t.Errorf("ingest's table-level UPDATE covers duplicate_of: %v", err)
	}
	if err := ingest.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, u).Scan(&seen); err != nil || seen != 1 {
		t.Errorf("ingest reads duplicate_of: %d %v", seen, err)
	}
	// Ingest's insert and upsert of a workout (no duplicate_of in the column list) are unaffected.
	if _, err := ingest.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		VALUES ($1, $2, 'run', now(), now(), 'Phone') ON CONFLICT (user_id, id) DO UPDATE SET end_at = EXCLUDED.end_at`, w3, u); err != nil {
		t.Errorf("ingest workout upsert: %v", err)
	}
	// Ingest's fold deletes a duplicate row.
	if ct, err := ingest.Exec(ctx, `DELETE FROM workouts WHERE user_id = $1 AND id = $2`, u, w3); err != nil || ct.RowsAffected() != 1 {
		t.Errorf("ingest fold delete: %v %v", ct, err)
	}

	// Catalog pin of the grants, for every role.
	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"ryft_scoring", "UPDATE", true}, {"ryft_scoring", "SELECT", true}, {"ryft_scoring", "INSERT", false},
		{"ryft_api", "SELECT", true}, {"ryft_api", "UPDATE", false}, {"ryft_api", "INSERT", false},
		{"ryft_ingest", "SELECT", true}, {"ryft_ingest", "UPDATE", true}, {"ryft_ingest", "INSERT", true},
		{"ryft_notify", "SELECT", false}, {"ryft_notify", "UPDATE", false},
	} {
		got := scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.workouts', 'duplicate_of', $2)`, c.role, c.priv)
		if got != c.want {
			t.Errorf("%s %s workouts.duplicate_of: %v, want %v", c.role, c.priv, got, c.want)
		}
	}

	// Idempotent: the file again changes nothing.
	body, err := migrations.ReadFile("migrations/0031_workout_duplicates.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0031 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conname = 'workouts_duplicate_of_not_self'`); n != 1 {
		t.Errorf("want one CHECK after a rerun, got %d", n)
	}
	// 0065 again (CI reruns the migrations): clean, and the grants stay as they were.
	body, err = migrations.ReadFile("migrations/0065_workout_ratings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0065 a second time: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'scoring_follow_session_times'`); n != 1 {
		t.Errorf("want one function after a rerun, got %d", n)
	}
	if scalar[bool](t, pool, `SELECT has_column_privilege('ryft_scoring', 'public.workouts', 'start_at', 'UPDATE')`) ||
		!scalar[bool](t, pool, `SELECT has_function_privilege('ryft_scoring', 'public.scoring_follow_session_times(uuid, uuid)', 'EXECUTE')`) {
		t.Error("the rerun changed scoring's grants")
	}
}
