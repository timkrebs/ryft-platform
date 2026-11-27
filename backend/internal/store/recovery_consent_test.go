package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

// The two recovery writes store health data, so a consent withdrawn after the request was admitted must leave
// nothing stored (D300): both check the admitting grant again inside their transaction under the lock RevokeConsent
// takes, as the HealthKit sync does (healthkit_sync_test.go). They run here as ryft_api, the role that serves them.

// recoveryAPIStore is a store whose connections run as ryft_api.
func recoveryAPIStore(t *testing.T, root *pgxpool.Pool) *Store {
	t.Helper()
	cfg := root.Config()
	cfg.MaxConns = 4
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE ryft_api`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

// recoveryGrant grants consent to the policy and returns the grant the way the request's authentication reads it.
func recoveryGrant(t *testing.T, st *Store, user uuid.UUID) *model.Consent {
	t.Helper()
	if err := st.GrantConsent(context.Background(), user, "2026-01-01", nil); err != nil {
		t.Fatal(err)
	}
	profile, _, err := st.GetUser(context.Background(), user)
	if err != nil || profile.Consent == nil {
		t.Fatalf("read the grant: %+v %v", profile.Consent, err)
	}
	return profile.Consent
}

// recoveryWrites is one write of each kind with the grant.
type recoveryWrites struct {
	name  string
	write func(st *Store, user uuid.UUID, grant *model.Consent) error
	rows  string // the rows the write leaves
}

func recoveryWriteCases(t *testing.T, root *pgxpool.Pool) []recoveryWrites {
	t.Helper()
	ctx := context.Background()
	now := utc("2026-11-26T10:00:00Z")
	return []recoveryWrites{
		{"check-in", func(st *Store, user uuid.UUID, grant *model.Consent) error {
			_, err := st.PutCheckin(ctx, user, grant, "2026-11-26", report("2026-11-26", 6), now, fakeScorer)
			return err
		}, `SELECT count(*) FROM recovery_checkins WHERE user_id=$1`},
		{"session RPE", func(st *Store, user uuid.UUID, grant *model.Consent) error {
			var id uuid.UUID
			if err := root.QueryRow(ctx, `SELECT id FROM workouts WHERE user_id=$1`, user).Scan(&id); err != nil {
				return err
			}
			_, err := st.PutWorkoutRating(ctx, user, grant, id, 7)
			return err
		}, `SELECT count(*) FROM workout_ratings WHERE user_id=$1`},
	}
}

func TestRecoveryWritesRefuseARevokedGrant(t *testing.T) {
	owner, root := testStore(t)
	api := recoveryAPIStore(t, root)
	ctx := context.Background()
	for _, c := range recoveryWriteCases(t, root) {
		t.Run(c.name, func(t *testing.T) {
			u := recoveryUserUTC(t, owner, root)
			seedWorkout(t, root, u, seedW{source: "Watch", start: utc("2026-11-25T17:00:00Z")})
			old := recoveryGrant(t, api, u)
			if err := api.RevokeConsent(ctx, u); err != nil { // committed between the admission and the write
				t.Fatal(err)
			}
			if err := c.write(api, u, old); !errors.Is(err, ErrHealthKitConsentChanged) {
				t.Fatalf("a revoked grant wrote: %v", err)
			}
			if n := countRows(t, root, c.rows, u); n != 0 {
				t.Fatalf("a refused write left %d rows", n)
			}
			// A new consent does not revive the old grant; the new grant, and no grant (consent not enforced), write.
			current := recoveryGrant(t, api, u)
			if err := c.write(api, u, old); !errors.Is(err, ErrHealthKitConsentChanged) {
				t.Fatalf("a regranted consent revived the old grant: %v", err)
			}
			if n := countRows(t, root, c.rows, u); n != 0 {
				t.Fatalf("a refused write left %d rows", n)
			}
			if err := c.write(api, u, current); err != nil {
				t.Fatalf("the current grant: %v", err)
			}
			if err := c.write(api, u, nil); err != nil {
				t.Fatalf("no grant: %v", err)
			}
			if n := countRows(t, root, c.rows, u); n != 1 {
				t.Fatalf("rows after the writes: %d, want 1", n)
			}
		})
	}
}

// A withdrawal that is in flight when the write starts is waited for, not raced: the write takes the lock
// RevokeConsent takes, so it sees the revocation once that commits.
func TestRecoveryWritesWaitForAnInFlightRevoke(t *testing.T) {
	owner, root := testStore(t)
	api := recoveryAPIStore(t, root)
	ctx := context.Background()
	for _, c := range recoveryWriteCases(t, root) {
		t.Run(c.name, func(t *testing.T) {
			u := recoveryUserUTC(t, owner, root)
			seedWorkout(t, root, u, seedW{source: "Watch", start: utc("2026-11-25T17:00:00Z")})
			grant := recoveryGrant(t, api, u)

			revoke, err := root.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer revoke.Rollback(ctx) //nolint:errcheck // already committed on the success path
			if _, err := revoke.Exec(ctx, lockApply, u); err != nil {
				t.Fatal(err)
			}
			if _, err := revoke.Exec(ctx, `UPDATE consents SET revoked_at = now() WHERE user_id=$1 AND revoked_at IS NULL`, u); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- c.write(api, u, grant) }()
			select {
			case err := <-done:
				t.Fatalf("the write did not wait for the revoke: %v", err)
			case <-time.After(300 * time.Millisecond):
			}
			if err := revoke.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrHealthKitConsentChanged) {
					t.Fatalf("the write after the revoke: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the write never finished")
			}
			if n := countRows(t, root, c.rows, u); n != 0 {
				t.Fatalf("a write racing a revoke left %d rows", n)
			}
		})
	}
}
