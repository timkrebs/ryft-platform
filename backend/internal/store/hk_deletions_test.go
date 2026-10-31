package store

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
)

// Database-level tests of the deletion handle hk_id (migrations 0029, 0030; design D-a):
// what ryft_ingest may delete, the per-user unique index, and the account merge.

// ryft_ingest may DELETE on the three raw tables and on nothing wider than before (the
// outbox relay prunes its own published rows, 0026), and may TRUNCATE nothing. As that role the deletes work on its own rows and
// are refused (42501) elsewhere.
func TestIngestRoleDeletesOnlyRawData(t *testing.T) {
	_, pool := testStore(t)
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT c.relname,
		has_table_privilege('ryft_ingest', c.oid, 'DELETE'), has_table_privilege('ryft_ingest', c.oid, 'TRUNCATE')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var canDelete []string
	for rows.Next() {
		var name string
		var del, trunc bool
		if err := rows.Scan(&name, &del, &trunc); err != nil {
			t.Fatal(err)
		}
		if del {
			canDelete = append(canDelete, name)
		}
		if trunc {
			t.Errorf("ryft_ingest may TRUNCATE %s", name)
		}
	}
	rows.Close()
	if want := []string{"outbox", "samples", "sleep_segments", "workouts"}; !slices.Equal(canDelete, want) {
		t.Fatalf("ryft_ingest can DELETE on %v, want exactly %v (outbox: the relay, 0026)", canDelete, want)
	}

	st := New(pool)
	user := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	hk := uuid.New()
	for q, args := range map[string][]any{
		`INSERT INTO samples (user_id, type, start_at, end_at, value, source, hk_id) VALUES ($1, 'steps', $2::timestamptz, $2, 1, 'W', $3)`:                            {user, now, hk},
		`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id) VALUES ($1, $2::timestamptz, $2::timestamptz + interval '1 h', 'core', 'W', $3)`: {user, now, hk},
		`INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES ($3, $1, 'running', $2::timestamptz, $2::timestamptz + interval '1 h', 'W')`:    {user, now, hk},
	} {
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE ryft_ingest`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `RESET ROLE`) }()
	for _, q := range []string{
		`DELETE FROM samples WHERE user_id = $1 AND hk_id = $2`,
		`DELETE FROM sleep_segments WHERE user_id = $1 AND hk_id = $2`,
		`DELETE FROM workouts WHERE user_id = $1 AND id = $2`,
	} {
		if tag, err := conn.Exec(ctx, q, user, hk); err != nil || tag.RowsAffected() != 1 {
			t.Errorf("%s: %v, %d rows", q, err, tag.RowsAffected())
		}
	}
	for _, table := range []string{"users", "devices", "daily_scores", "score_queue", "sessions", "deleted_users"} {
		_, err := conn.Exec(ctx, `DELETE FROM `+table)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42501" {
			t.Errorf("DELETE FROM %s as ryft_ingest: %v, want 42501", table, err)
		}
	}
}

// hk_id is a nullable uuid on both tables; it is unique per user (many rows may have none),
// not across users; the natural key stays unique too.
func TestHKIDIsNullableAndUniquePerUser(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"samples", "sleep_segments"} {
		var typ, nullable string
		if err := pool.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'hk_id'`, table).Scan(&typ, &nullable); err != nil ||
			typ != "uuid" || nullable != "YES" {
			t.Errorf("%s.hk_id: %q nullable %q (%v), want a nullable uuid", table, typ, nullable, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	sample := func(user uuid.UUID, at time.Time, source string, hk *uuid.UUID) error {
		_, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, source, hk_id) VALUES ($1, 'steps', $2::timestamptz, $2, 1, $3, $4)`, user, at, source, hk)
		return err
	}
	sleep := func(user uuid.UUID, at time.Time, source string, hk *uuid.UUID) error {
		_, err := pool.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id) VALUES ($1, $2::timestamptz, $2::timestamptz + interval '1 h', 'core', $3, $4)`, user, at, source, hk)
		return err
	}
	dup := func(err error) bool {
		var pe *pgconn.PgError
		return errors.As(err, &pe) && pe.Code == "23505"
	}
	for name, insert := range map[string]func(uuid.UUID, time.Time, string, *uuid.UUID) error{"samples": sample, "sleep_segments": sleep} {
		id := uuid.New()
		if err := insert(a, now, "W1", nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := insert(a, now, "W2", nil); err != nil { // NULLs never clash
			t.Errorf("%s: two rows without an id: %v", name, err)
		}
		if err := insert(a, now, "W3", &id); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := insert(a, now.Add(time.Minute), "W3", &id); !dup(err) {
			t.Errorf("%s: the same id twice for one user: %v, want 23505", name, err)
		}
		if err := insert(b, now, "W3", &id); err != nil {
			t.Errorf("%s: the same id for another user: %v", name, err)
		}
		if err := insert(a, now, "W3", nil); !dup(err) {
			t.Errorf("%s: the natural key stays unique: %v, want 23505", name, err)
		}
	}
}

// Merging accounts that hold the same HealthKit id on different natural keys does not
// violate the per-user unique index: to's row is kept, from's clashing row is dropped, the
// rest moves with its id intact; the dry run reports what the real run does; from can be
// deleted afterwards.
func TestMergeWithHKIDsDoesNotViolateTheIndex(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	from, to := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{from, to} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	clash, fromOnly, sameKey := uuid.New(), uuid.New(), uuid.New()
	// A source renamed in one account: the same id, other natural keys.
	put := func(user uuid.UUID, at time.Time, source string, hk *uuid.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, source, hk_id) VALUES ($1, 'steps', $2::timestamptz, $2, 1, $3, $4)`, user, at, source, hk); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id) VALUES ($1, $2::timestamptz, $2::timestamptz + interval '1 h', 'core', $3, $4)`, user, at, source, hk); err != nil {
			t.Fatal(err)
		}
	}
	put(from, now, "Old name", &clash)
	put(to, now, "New name", &clash)
	put(from, now.Add(time.Hour), "W", &fromOnly)
	put(from, now.Add(2*time.Hour), "W", nil)
	put(from, now.Add(3*time.Hour), "W", &sameKey)
	put(to, now.Add(3*time.Hour), "W", &sameKey) // same natural key and same id: one record
	if _, err := st.UpsertWorkouts(ctx, from, []model.Workout{{ID: uuid.New(), Activity: "running", Start: now, End: now.Add(time.Hour), Source: "W"}}); err != nil {
		t.Fatal(err)
	}

	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	moved := func(r MergeResult) map[string][2]int64 {
		m := map[string][2]int64{}
		for _, x := range r.Tables {
			m[x.Table] = [2]int64{x.Moved, x.Dropped}
		}
		return m
	}
	// from has 4 rows per table: moves 2 (fromOnly, no id), drops 2 (clash, sameKey).
	want := [2]int64{2, 2}
	dry, err := st.MergeUsers(ctx, from, to, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	res, err := st.MergeUsers(ctx, from, to, false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	for _, r := range []MergeResult{dry, res} {
		for _, table := range []string{"samples", "sleep_segments"} {
			if got := moved(r)[table]; got != want {
				t.Errorf("%s moved/dropped %v, want %v", table, got, want)
			}
		}
	}
	for _, table := range []string{"samples", "sleep_segments"} {
		if n := count(`SELECT count(*) FROM `+table+` WHERE user_id = $1`, to); n != 4 {
			t.Errorf("%s: %d rows for to, want 4 (its own 2 + 2 moved)", table, n)
		}
		if n := count(`SELECT count(*) FROM `+table+` WHERE user_id = $1 AND hk_id = $2 AND source = 'New name'`, to, clash); n != 1 {
			t.Errorf("%s: to keeps its row of the clashing id", table)
		}
		if n := count(`SELECT count(*) FROM `+table+` WHERE user_id = $1 AND hk_id = $2`, to, fromOnly); n != 1 {
			t.Errorf("%s: from's own id moves with the row", table)
		}
		if n := count(`SELECT count(*) FROM `+table+` WHERE user_id = $1 AND hk_id IS NULL`, to); n != 1 {
			t.Errorf("%s: a row without an id moves as before", table)
		}
	}
	if err := st.DeleteUser(ctx, from); err != nil {
		t.Fatalf("from is deleted afterwards: %v", err)
	}
}

// The merge runs as ryft_api, which may change the owner column of the raw tables and nothing
// else (0024): plan and real merge must work with those grants, with HealthKit ids on both
// sides, also where the natural key is the same.
func TestMergeAsAPIRoleWithHKIDs(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	from, to := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{from, to} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	h := uuid.New()
	for _, user := range []uuid.UUID{from, to} {
		var hk *uuid.UUID
		if user == from {
			hk = &h
		}
		if _, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, source, hk_id) VALUES ($1, 'steps', $2::timestamptz, $2, 1, 'W', $3)`, user, now, hk); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, hk_id) VALUES ($1, $2::timestamptz, $2::timestamptz + interval '1 h', 'core', 'W', $3)`, user, now, hk); err != nil {
			t.Fatal(err)
		}
	}
	const pw = "pw-ryft_api-0123456789"
	if err := db.SetRolePassword(ctx, pool, "ryft_api", pw); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("ryft_api", pw)
	ap, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ap.Close)
	for _, dry := range []bool{true, false} {
		if _, err := New(ap).MergeUsers(ctx, from, to, dry); err != nil {
			t.Fatalf("merge as ryft_api (dry run %v): %v", dry, err)
		}
	}
}
