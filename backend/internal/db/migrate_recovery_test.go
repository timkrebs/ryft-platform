package db

import (
	"context"
	"testing"
)

// 0062–0064 (D291–D295): the check-in and rating tables keep their ranges, cascade with their user and workout,
// every file reruns, and recovery.checkin ships dark and keeps what the operator set since.
func TestMigrateRecovery(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0
		FROM feature_flags WHERE key = 'recovery.checkin'`) {
		t.Fatal("recovery.checkin ships enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false, rollout_pct = 100 WHERE key = 'recovery.checkin'`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"0062_recovery_checkins.sql", "0063_workout_ratings.sql", "0064_recovery_checkin_flag.sql"} {
		body, err := migrations.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("%s a second time: %v", f, err)
		}
	}
	if !scalar[bool](t, pool, `SELECT NOT enabled AND rollout_pct = 100 FROM feature_flags WHERE key = 'recovery.checkin'`) {
		t.Error("a rerun of 0064 overwrote the operator's setting")
	}

	user, other := "aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"
	workout := "bbbbbbbb-0000-0000-0000-000000000001"
	for _, id := range []string{user, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at) VALUES ($1, $2, 'run', now(), now())`, workout, user); err != nil {
		t.Fatal(err)
	}

	const checkin = `INSERT INTO recovery_checkins (user_id, wake_date, bedtime, wake_time, tz_offset_sec,
		perceived_sleep_quality, perceived_freshness, soreness, sleep_duration_min, recovery_score, components,
		acute_drop, drop_reason, confounders, model_version)
		VALUES ($1, $2::date, '2026-11-25T22:10:00Z', $3::timestamptz, $4, $5, $6, false, 450, 77, '{}', $7, $8, $9, '2026.11.1')`
	good := []any{user, "2026-11-26", "2026-11-26T05:40:00Z", 3600, 7, 6, false, nil, []string{}}
	if _, err := pool.Exec(ctx, checkin, good...); err != nil {
		t.Fatalf("a valid check-in: %v", err)
	}
	with := func(i int, v any) []any {
		a := append([]any(nil), good...)
		a[i] = v
		return a
	}
	for name, args := range map[string][]any{
		"the same wake day twice":                good,
		"wake_time not after bedtime":            with(2, "2026-11-25T22:10:00Z"),
		"an offset that is not a multiple of 60": with(3, 3601),
		"an offset beyond 14 hours":              with(3, 50460),
		"quality 0":                              with(4, 0),
		"quality 11":                             with(4, 11),
		"freshness 0":                            with(5, 0),
		"a drop reason without a drop":           with(7, "checkin_below_usual"),
		"an unknown drop reason":                 append(with(6, true)[:7:7], "mystery", []string{}),
		"confounders without a drop":             with(8, []string{"journal_sick"}),
		"an unknown user":                        with(0, "aaaaaaaa-0000-0000-0000-0000000000ff"),
	} {
		if args[0] == user && name != "the same wake day twice" && args[1] == good[1] {
			args = append([]any(nil), args...)
			args[1] = "2026-11-24" // a free wake day, so only the rule under test can fail
		}
		if _, err := pool.Exec(ctx, checkin, args...); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := pool.Exec(ctx, checkin, user, "2026-11-27", "2026-11-26T06:00:00Z", 3600, 3, 4, true, "checkin_low_quality_and_freshness", []string{"journal_sick"}); err != nil {
		t.Errorf("a drop with its reason and a confounder: %v", err)
	}

	const rating = `INSERT INTO workout_ratings (user_id, workout_id, session_rpe) VALUES ($1, $2, $3)`
	for _, rpe := range []string{"0.5", "0", "10.5", "7.25", "11"} {
		if _, err := pool.Exec(ctx, rating, user, workout, rpe); err == nil {
			t.Errorf("session_rpe %s must be refused", rpe)
		}
	}
	if _, err := pool.Exec(ctx, rating, user, "bbbbbbbb-0000-0000-0000-0000000000ff", "5"); err == nil {
		t.Error("a rating of a workout that does not exist must be refused")
	}
	for _, rpe := range []string{"1", "7.5", "10"} {
		if _, err := pool.Exec(ctx, `DELETE FROM workout_ratings`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, rating, user, workout, rpe); err != nil {
			t.Errorf("session_rpe %s: %v", rpe, err)
		}
	}
	if _, err := pool.Exec(ctx, rating, user, workout, "8"); err == nil {
		t.Error("a second rating of the same workout must be refused")
	}

	// A workout's deletion (a HealthKit deletion, the fold) takes its rating; an account's takes both tables' rows.
	if _, err := pool.Exec(ctx, `DELETE FROM workouts WHERE id = $1`, workout); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM workout_ratings`); n != 0 {
		t.Errorf("%d ratings survived their workout", n)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM recovery_checkins WHERE user_id = $1`, user); n != 0 {
		t.Errorf("%d check-ins survived their user", n)
	}
}
