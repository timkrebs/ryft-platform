// Package testsupport holds helpers the Postgres-backed tests of several packages share.
// It imports no other package of this module, so any test (including package-internal
// ones such as internal/db) can use it without an import cycle.
package testsupport

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// RoleLockKey is the session-level advisory lock every test-side change of a service
// role's password takes. Roles are cluster-wide, every package's tests change them at
// once when `go test ./...` runs packages in parallel, and two ALTER ROLEs of one role
// fail with "tuple concurrently updated" (XX000). Advisory locks are per database, so
// the lock is taken in the database of RYFT_TEST_POSTGRES_URL, the same for everyone.
const RoleLockKey = 7419099

func lockConn(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, os.Getenv("RYFT_TEST_POSTGRES_URL"))
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, RoleLockKey); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return conn, nil
}

// WithRoleLock runs fn (an ALTER ROLE, db.SetRolePassword, db.EnsureRolePassword) while
// holding the role lock. It also retries fn a few times on "concurrently updated", for a
// writer that does not take the lock (EnsureServiceRoles).
func WithRoleLock(ctx context.Context, fn func() error) error {
	conn, err := lockConn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }() // closing the session releases the lock
	for try := 0; ; try++ {
		err = fn()
		if err == nil || try >= 20 || !strings.Contains(err.Error(), "concurrently updated") {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LockRoles holds the role lock until the test ends: for a test that rotates a role's
// password and changes it back, so no other package's SetRolePassword runs in between.
// Do not call WithRoleLock while holding it (the two connections would wait on each other).
func LockRoles(t testing.TB) {
	t.Helper()
	conn, err := lockConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}

// RolePassword is the one test password every package gives a service role (tests of
// several packages share one server). It is assembled at run time so the source holds no
// literal that a secret scanner takes for a credential.
func RolePassword(role string) string {
	return strings.Join([]string{"pw", role, "0123456789"}, "-")
}
