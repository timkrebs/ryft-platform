package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

// RFT-88, D302: the merge takes the apply lock of both accounts and from's rows FOR UPDATE, and erases from in the
// same transaction. A write in flight on from finishes first and its rows move; one that comes later fails.

func mergePair(t *testing.T) (*Store, *pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	st, pool := testStore(t)
	from, to := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{from, to} {
		if _, err := st.EnsureUser(context.Background(), Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	return st, pool, from, to
}

// mergeAsync starts MergeUsers and fails the test if it returns within 300 ms (it must be waiting).
func mergeAsync(t *testing.T, st *Store, from, to uuid.UUID) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := st.MergeUsers(context.Background(), from, to, false)
		done <- err
	}()
	mustWait(t, "MergeUsers", done)
	return done
}

func mustWait(t *testing.T, what string, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s must wait, returned %v", what, err)
	case <-time.After(300 * time.Millisecond):
	}
}

func begin(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) }) // a failed assertion must not leave the locks held
	return tx
}

// wantForeignKeyViolation: a write on the merged account ends in SQLSTATE 23503 (foreign_key_violation), the users
// row it references is gone, and in nothing else (the handlers answer it with a 500; the retry gets the 401).
func wantForeignKeyViolation(t *testing.T, what string, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23503" {
		t.Errorf("%s: %v, want SQLSTATE 23503", what, err)
	}
}

// A deadlock (40P01) is what MergeUsers retries; nothing else, and not a wrapped error of another kind.
func TestIsDeadlock(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("boom"), false},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped deadlock", fmt.Errorf("lock sessions: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, false},
		{"foreign key", &pgconn.PgError{Code: "23503"}, false},
	} {
		if got := isDeadlock(c.err); got != c.want {
			t.Errorf("%s: isDeadlock = %v, want %v", c.name, got, c.want)
		}
	}
}

func tombstones(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	return count(t, pool, `SELECT count(*) FROM deleted_users WHERE id = $1`, id)
}

func TestMergeWaitsForASyncInFlight(t *testing.T) {
	st, pool, from, to := mergePair(t)
	ctx := context.Background()

	// A sync's transaction holds the apply lock and has inserted a sample for from, not committed yet.
	tx := begin(t, pool)
	if _, err := tx.Exec(ctx, lockApply, from); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		VALUES ($1, 'steps', now(), now(), 10, 'count', 'test')`, from); err != nil {
		t.Fatal(err)
	}
	done := mergeAsync(t, st, from, to)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples WHERE user_id = $1`, to); n != 1 {
		t.Fatalf("the in-flight sync's sample is on to: %d, want 1", n)
	}
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("%d rows of the merged account left", n)
	}
	if n := tombstones(t, pool, from); n != 1 {
		t.Fatalf("from's tombstone: %d, want 1", n)
	}
	// A sync that arrives after the merge cannot write.
	_, err := st.UpsertSamples(ctx, from, []model.Sample{sample(time.Now())})
	wantForeignKeyViolation(t, "a sync after the merge", err)
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("the late sync left %d rows", n)
	}
}

func TestMergeWaitsForAJournalSaveInFlight(t *testing.T) {
	st, pool, from, to := mergePair(t)
	ctx := context.Background()
	b1, b2 := uuid.New(), uuid.New()
	for _, b := range []struct {
		id   uuid.UUID
		slug string
	}{{b1, "cold"}, {b2, "sauna"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO behaviors (id, user_id, slug, name_en, name_de, category, kind)
			VALUES ($1, $2, $3, $3, $3, 'custom', 'yes_no')`, b.id, from, b.slug); err != nil {
			t.Fatal(err)
		}
	}
	const day = "2026-11-20"
	if err := st.PutJournalDay(ctx, from, day, []model.JournalEntry{{BehaviorID: b1, ValueBool: true}}, nil); err != nil {
		t.Fatal(err)
	}

	// What PutJournalDay does on a day from already has, held open: the upsert updates the day (no foreign-key check,
	// so no lock on the users row), the old entry goes, a new one comes.
	tx := begin(t, pool)
	for _, q := range []string{
		`INSERT INTO journal_days (user_id, day, answered_at) VALUES ($1, $2::date, now())
			ON CONFLICT (user_id, day) DO UPDATE SET answered_at = now()`,
		`DELETE FROM journal_entries WHERE user_id = $1 AND day = $2::date`,
	} {
		if _, err := tx.Exec(ctx, q, from, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO journal_entries (user_id, day, behavior_id, value_bool) VALUES ($1, $2::date, $3, false)`,
		from, day, b2); err != nil {
		t.Fatal(err)
	}
	done := mergeAsync(t, st, from, to)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// to's day has exactly the new entry, not the old version the merge would have copied without the row locks.
	if n := count(t, pool, `SELECT count(*) FROM journal_entries WHERE user_id = $1 AND day = $2::date`, to, day); n != 1 {
		t.Fatalf("to's entries on the day: %d, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM journal_entries WHERE user_id = $1 AND day = $2::date AND behavior_id = $3 AND NOT value_bool`,
		to, day, b2); n != 1 {
		t.Fatal("to's day is not the saved version")
	}
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("%d rows of the merged account left", n)
	}
	// A save that arrives after the merge fails, on the day from had and on a new one.
	for _, d := range []string{day, "2026-11-21"} {
		// The day from had: its row is gone, so the upsert inserts and the foreign key to users fails.
		err := st.PutJournalDay(ctx, from, d, []model.JournalEntry{{BehaviorID: b2, ValueBool: true}}, nil)
		wantForeignKeyViolation(t, "a journal save on "+d+" after the merge", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM journal_days WHERE user_id = $1`, from); n != 0 {
		t.Fatalf("the late saves left %d days", n)
	}
}

// Writers that come while the merge is running wait for it and then fail; nothing of them remains. The merge is held
// open after it took its locks: another transaction holds a row lock on one of from's samples, which the merge's move
// of the samples waits for (the samples are not among the rows lockFromRows takes).
func TestMergeFailsAWriterThatWaitsForIt(t *testing.T) {
	st, pool, from, to := mergePair(t)
	ctx := context.Background()
	b := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO behaviors (id, user_id, slug, name_en, name_de, category, kind)
		VALUES ($1, $2, 'cold', 'Cold', 'Kalt', 'custom', 'yes_no')`, b, from); err != nil {
		t.Fatal(err)
	}
	const day, newDay = "2026-11-20", "2026-11-21"
	if err := st.PutJournalDay(ctx, from, day, []model.JournalEntry{{BehaviorID: b, ValueBool: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, from, []model.Sample{sample(time.Now().Add(-time.Hour))}); err != nil {
		t.Fatal(err)
	}

	hold := begin(t, pool)
	if _, err := hold.Exec(ctx, `SELECT 1 FROM samples WHERE user_id = $1 FOR UPDATE`, from); err != nil {
		t.Fatal(err)
	}
	merged := mergeAsync(t, st, from, to)
	waitForTransactionWaiters(t, pool, 1) // the merge, stopped at the samples, holding its locks

	type write struct {
		name string
		do   func() error
	}
	writes := []write{
		{"journal day from has (waits for the row lock)", func() error {
			return st.PutJournalDay(ctx, from, day, []model.JournalEntry{{BehaviorID: b, ValueBool: false}}, nil)
		}},
		{"new journal day (waits for the users row)", func() error {
			return st.PutJournalDay(ctx, from, newDay, []model.JournalEntry{{BehaviorID: b, ValueBool: true}}, nil)
		}},
		// UpsertSamples takes no lockApply (ApplySync and the HealthKit paths do): this write waits on the users row.
		{"plain sample write (waits for the users row)", func() error {
			_, err := st.UpsertSamples(ctx, from, []model.Sample{{Type: "steps", Start: time.Now(), End: time.Now(), Value: 4242, Unit: "count", Source: "late"}})
			return err
		}},
	}
	results := make([]chan error, len(writes))
	for i, w := range writes {
		results[i] = make(chan error, 1)
		go func() { results[i] <- w.do() }()
	}
	for i, w := range writes {
		mustWait(t, w.name, results[i])
	}
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-merged; err != nil {
		t.Fatal(err)
	}
	for i, w := range writes {
		// All three end in the foreign key to users: the existing day's upsert finds its row gone and inserts.
		wantForeignKeyViolation(t, w.name, <-results[i])
	}
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("%d rows of the merged account left", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM journal_days WHERE day = $1::date`, newDay); n != 0 {
		t.Fatalf("the new day exists for someone: %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples WHERE source = 'late'`); n != 0 {
		t.Fatalf("the late sample exists for someone: %d", n)
	}
	// to has the day as it was when the merge read it: the failed save changed nothing.
	if n := count(t, pool, `SELECT count(*) FROM journal_entries WHERE user_id = $1 AND day = $2::date AND value_bool`, to, day); n != 1 {
		t.Fatalf("to's day is not the one from had: %d matching entries", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM journal_entries WHERE user_id = $1`, to); n != 1 {
		t.Fatalf("to's entries: %d, want 1", n)
	}
}

// waitForTransactionWaiters polls until n sessions of this database wait for another transaction (a row lock),
// failing after 10 s.
func waitForTransactionWaiters(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for count(t, pool, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event_type = 'Lock' AND wait_event IN ('transactionid', 'tuple')`) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d session(s) never waited for a transaction", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The fast path of EnsureUser (a fresh last_seen_at, one read) cannot bring the merged account back: the row is gone,
// so the slow path finds the tombstone.
func TestMergeLeavesFromDeleted(t *testing.T) {
	st, pool, from, to := mergePair(t)
	ctx := context.Background()
	if _, err := st.EnsureUser(ctx, Identity{ID: from}); err != nil { // the fast path: last_seen_at is fresh
		t.Fatal(err)
	}
	if _, err := st.MergeUsers(ctx, from, to, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureUser(ctx, Identity{ID: from}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("EnsureUser after the merge: %v, want ErrDeleted", err)
	}
	if n := owned(t, pool, from); n != 0 {
		t.Fatalf("EnsureUser recreated the merged account: %d rows", n)
	}
	if n := tombstones(t, pool, from); n != 1 {
		t.Fatalf("tombstones of from: %d, want 1", n)
	}
	if n := tombstones(t, pool, to); n != 0 {
		t.Fatalf("tombstones of to: %d, want 0", n)
	}
	if _, err := st.EnsureUser(ctx, Identity{ID: to}); err != nil {
		t.Fatalf("to is untouched: %v", err)
	}
	if _, err := st.MergeUsers(ctx, from, to, false); !errors.Is(err, ErrFromNotFound) {
		t.Fatalf("a second merge: %v", err)
	}
}
