package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// 0041: the two workout columns (nullable, no default, no CHECK; a row from before reads NULL), the
// workout_alerts ledger (its columns, the primary key, user_id the only foreign key, the nine outcomes,
// no other index), its policies and the notify read policy on workouts, which no RESTRICTIVE policy
// narrows, and a rerun of the file that changes nothing. The roles' grants and what they can do through
// RLS are in roles_test.go.
func TestMigrateActivityAlerts(t *testing.T) {
	for _, supabase := range []bool{false, true} {
		name := "plain"
		if supabase {
			name = "supabase"
		}
		t.Run(name, func(t *testing.T) { testMigrateActivityAlerts(t, supabase) })
	}
}

func testMigrateActivityAlerts(t *testing.T, supabase bool) {
	pool := freshDB(t, supabase)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 38); err != nil {
		t.Fatal(err)
	}
	const (
		user    = "aaaaaaaa-0000-4000-8000-000000000041"
		other   = "bbbbbbbb-0000-4000-8000-000000000041"
		device  = "cccccccc-0000-4000-8000-000000000041"
		workout = "dddddddd-0000-4000-8000-000000000041"
		alert   = "eeeeeeee-0000-4000-8000-000000000041"
	)
	// A workout stored before 0041.
	for _, q := range []string{
		`INSERT INTO users (id) VALUES ($1)`,
		`INSERT INTO devices (id, user_id) VALUES ('` + device + `', $1)`,
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		   VALUES ('` + workout + `', $1, 'running', now() - interval '1 hour', now() - interval '20 minutes', 'Watch')`,
	} {
		if _, err := pool.Exec(ctx, q, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}

	// The columns: nullable, no default, and no CHECK names them (it would scan under ACCESS EXCLUSIVE).
	for col, typ := range map[string]string{"device_id": "uuid", "source_bundle_id": "text"} {
		got := scalar[string](t, pool, `SELECT coalesce((SELECT data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '-')
			FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'workouts' AND column_name = $1), 'missing')`, col)
		if want := typ + " YES -"; got != want {
			t.Errorf("workouts.%s: %s, want %s", col, got, want)
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'workouts'::regclass AND contype = 'c'
		AND (pg_get_constraintdef(oid) LIKE '%device_id%' OR pg_get_constraintdef(oid) LIKE '%source_bundle_id%')`); n != 0 {
		t.Errorf("want no CHECK on the new workouts columns, got %d", n)
	}
	if !scalar[bool](t, pool, `SELECT device_id IS NULL AND source_bundle_id IS NULL FROM workouts WHERE user_id = $1 AND id = $2`, user, workout) {
		t.Error("a workout stored before 0041 reads NULL device_id and source_bundle_id (rule 5 passes it)")
	}

	// The ledger's columns, in order.
	cols := scalar[string](t, pool, `SELECT coalesce(string_agg(column_name || ':' || data_type || ':' || is_nullable, ',' ORDER BY ordinal_position), '')
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'workout_alerts'`)
	if want := "user_id:uuid:NO,workout_id:uuid:NO,source:text:NO,activity:text:NO," +
		"start_at:timestamp with time zone:NO,end_at:timestamp with time zone:NO,day:date:NO,outcome:text:NO," +
		"devices:integer:NO,decided_at:timestamp with time zone:NO"; cols != want {
		t.Fatalf("workout_alerts columns:\n%s\nwant\n%s", cols, want)
	}
	if got := scalar[string](t, pool, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'workout_alerts'::regclass AND contype = 'p'`); got != "PRIMARY KEY (user_id, workout_id)" {
		t.Errorf("primary key: %s", got)
	}
	// One foreign key: user_id to users, cascading (an image from before 0041 deletes the users row without
	// naming the ledger). None on workout_id: the mark outlives a folded or deleted copy.
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'workout_alerts'::regclass AND contype = 'f'`); n != 1 {
		t.Errorf("want exactly one foreign key on workout_alerts, got %d", n)
	}
	if !scalar[bool](t, pool, `SELECT coalesce(bool_and(confrelid = 'users'::regclass AND confdeltype = 'c'
		AND conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'workout_alerts'::regclass AND attname = 'user_id')]), false)
		FROM pg_constraint WHERE conrelid = 'workout_alerts'::regclass AND contype = 'f'`) {
		t.Error("the foreign key is (user_id) REFERENCES users ON DELETE CASCADE")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'workout_alerts'`); n != 1 {
		t.Errorf("want the primary key's index only, got %d indexes", n)
	}

	// The outcomes: the nine pass (devices and decided_at default), anything else is refused.
	insert := `INSERT INTO workout_alerts (user_id, workout_id, source, activity, start_at, end_at, day, outcome)
		VALUES ($1, $2, 'Watch', 'running', now() - interval '1 hour', now() - interval '20 minutes', current_date, $3)`
	code := func(err error) string {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return pe.Code
		}
		return ""
	}
	for i, o := range []string{"sending", "sent", "failed", "capped", "muted", "quiet", "no_token", "backfill", "inherited"} {
		// A workout_id no workout has: no foreign key refuses it.
		if _, err := pool.Exec(ctx, insert, user, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), o); err != nil {
			t.Errorf("outcome %s: %v", o, err)
		}
	}
	if !scalar[bool](t, pool, `SELECT bool_and(devices = 0 AND decided_at IS NOT NULL) FROM workout_alerts WHERE user_id = $1`, user) {
		t.Error("devices defaults to 0 and decided_at to now()")
	}
	for _, o := range []string{"pending", "duplicate", "SENT", ""} {
		if _, err := pool.Exec(ctx, insert, user, alert, o); code(err) != "23514" { // check_violation
			t.Errorf("outcome %q: want a check violation, got %v", o, err)
		}
	}
	if _, err := pool.Exec(ctx, insert, user, alert, "sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, user, alert, "capped"); code(err) != "23505" { // unique_violation
		t.Errorf("a second row for the same workout: want the primary key to refuse it, got %v", err)
	}
	if _, err := pool.Exec(ctx, insert, other, alert, "sent"); code(err) != "23503" { // foreign_key_violation
		t.Errorf("a row of no user: want a foreign-key violation, got %v", err)
	}

	// Policies (both shapes: they are inert on plain Postgres). The ledger admits api and notify; notify reads
	// workouts through a permissive SELECT policy, and no RESTRICTIVE policy on workouts is for SELECT, for
	// every role or for notify (0011's and 0029's are scoring's and ingest's INSERT and DELETE).
	for _, c := range []struct{ table, policy, want string }{
		{"workout_alerts", "workout_alerts_services", "PERMISSIVE ALL ryft_api,ryft_notify"},
		{"workouts", "workouts_notify_read", "PERMISSIVE SELECT ryft_notify"},
	} {
		got := scalar[string](t, pool, `SELECT coalesce((SELECT permissive || ' ' || cmd || ' ' || array_to_string(roles, ',')
			FROM pg_policies WHERE schemaname = 'public' AND tablename = $1 AND policyname = $2), 'missing')`, c.table, c.policy)
		if got != c.want {
			t.Errorf("policy %s: %s, want %s", c.policy, got, c.want)
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'workouts'
		AND permissive = 'RESTRICTIVE' AND (cmd IN ('SELECT', 'ALL') OR 'ryft_notify' = ANY (roles) OR 'public' = ANY (roles))`); n != 0 {
		t.Errorf("%d restrictive policies on workouts would narrow notify's read", n)
	}
	if supabase {
		if !scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.workout_alerts'::regclass`) {
			t.Error("RLS must be on for workout_alerts")
		}
		for _, role := range []string{"anon", "authenticated", "service_role"} {
			for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
				if scalar[bool](t, pool, `SELECT has_table_privilege($1, 'public.workout_alerts', $2)`, role, priv) {
					t.Errorf("%s must not %s workout_alerts", role, priv)
				}
			}
			for _, col := range []string{"device_id", "source_bundle_id"} {
				if scalar[bool](t, pool, `SELECT has_column_privilege($1, 'public.workouts', $2, 'SELECT')`, role, col) {
					t.Errorf("%s must not read workouts.%s", role, col)
				}
			}
		}
	} else if scalar[bool](t, pool, `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.workout_alerts'::regclass`) {
		t.Error("RLS on workout_alerts is Supabase-only (as in 0015 and 0037)")
	}

	// The ledger goes with the users row.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, other, alert, "sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, other); err != nil {
		t.Fatalf("delete a users row with ledger rows: %v", err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM workout_alerts WHERE user_id = $1`, other); n != 0 {
		t.Errorf("the cascade leaves %d rows", n)
	}

	// Idempotent: the file again changes no row, grant, policy or constraint.
	state := func() string {
		t.Helper()
		return scalar[string](t, pool, `SELECT concat_ws(' | ',
			(SELECT string_agg(outcome || '/' || devices, ',' ORDER BY workout_id) FROM workout_alerts),
			(SELECT relacl::text FROM pg_class WHERE oid = 'public.workout_alerts'::regclass),
			(SELECT string_agg(attname || '=' || coalesce(attacl::text, ''), ',' ORDER BY attnum) FROM pg_attribute
			   WHERE attrelid IN ('public.workout_alerts'::regclass, 'public.workouts'::regclass, 'public.devices'::regclass)
			   AND attnum > 0 AND NOT attisdropped),
			(SELECT string_agg(tablename || '.' || policyname || ':' || permissive || ':' || cmd || ':' || array_to_string(roles, ','),
			   ',' ORDER BY tablename, policyname) FROM pg_policies WHERE tablename IN ('workout_alerts', 'workouts', 'devices')),
			(SELECT string_agg(conname || ':' || pg_get_constraintdef(oid), ',' ORDER BY conname) FROM pg_constraint
			   WHERE conrelid IN ('public.workout_alerts'::regclass, 'public.workouts'::regclass)),
			(SELECT string_agg(column_name, ',' ORDER BY column_name) FROM information_schema.columns
			   WHERE table_schema = 'public' AND table_name = 'workouts'))`)
	}
	before := state()
	body, err := migrations.ReadFile("migrations/0041_activity_alerts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0041 a second time: %v", err)
	}
	if after := state(); after != before {
		t.Errorf("a rerun of 0041 changed the schema or the rows:\n%s\nwas\n%s", after, before)
	}
}
