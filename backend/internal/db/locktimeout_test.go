package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// shortLockTimeout injects the migrator's lock_timeout for one test.
func shortLockTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockTimeout
	lockTimeout = d
	t.Cleanup(func() { lockTimeout = old })
}

// A statement that waits on a lock inside a migration transaction gives up after
// the migrator's lock_timeout, and the setting ends with the transaction.
func TestMigratorLockTimeoutIsPerTransaction(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	shortLockTimeout(t, 700*time.Millisecond)

	var inside string
	if err := locked(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SHOW lock_timeout`).Scan(&inside)
	}); err != nil {
		t.Fatal(err)
	}
	if inside != "700ms" {
		t.Fatalf("lock_timeout inside the migration transaction: %s", inside)
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE held (id int)`); err != nil {
		t.Fatal(err)
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `LOCK TABLE held IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = locked(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `ALTER TABLE held ADD COLUMN x int`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("want a lock timeout, got %v", err)
	}
	if took := time.Since(start); took < 500*time.Millisecond || took > 5*time.Second {
		t.Fatalf("gave up after %s, want about 700 ms", took)
	}
	if got := scalar[string](t, pool, `SHOW lock_timeout`); got != "0" {
		t.Fatalf("lock_timeout leaked into the pool: %s", got)
	}
}

// The migration's own SET LOCAL runs after the migrator's setting and wins (0027
// asks for 5 s while the migrator's is 300 ms); the error names the migration, and
// schema_migrations stays as it was.
func TestMigrateFailureNamesTheMigrationAndTheMigrationsSettingWins(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 26); err != nil {
		t.Fatal(err)
	}
	shortLockTimeout(t, 300*time.Millisecond)
	reader, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx)
	if _, err := reader.Exec(ctx, `SELECT count(*) FROM daily_scores`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = Migrate(ctx, pool, quiet)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("want a lock timeout, got %v", err)
	}
	if took := time.Since(start); took < 4*time.Second {
		t.Fatalf("0027's own 5 s must win over the migrator's 300 ms, gave up after %s", took)
	}
	if !strings.Contains(err.Error(), "0027_calibrating_reason.sql") || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("the error must name the migration and the lock timeout: %v", err)
	}
	if got := scalar[int](t, pool, `SELECT max(version) FROM schema_migrations`); got != 26 {
		t.Fatalf("schema_migrations after the failed 0027: max version %d, want 26", got)
	}
}

// A second migrator waiting on the advisory lock is not cut by the lock timeout:
// it waits for the first and then runs.
func TestMigratorWaitOnTheAdvisoryLockIsNotCut(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	shortLockTimeout(t, 200*time.Millisecond)
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	if _, err := first.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- locked(ctx, pool, func(pgx.Tx) error { return nil }) }()
	select {
	case err := <-done:
		t.Fatalf("the second migrator must wait for the first, returned %v", err)
	case <-time.After(1500 * time.Millisecond): // several lock timeouts
	}
	if err := first.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second migrator after the first finished: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second migrator never got the lock")
	}
}
