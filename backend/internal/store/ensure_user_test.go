package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// D286: the smoke account's token only looks its user up. A missing or deleted user is an error and
// no row appears; an existing one is read and touched, and keeps its email, provider and name.
func TestEnsureUserMustExistNeverInserts(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	count := func(id uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	missing := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: missing, MustExist: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing user: want ErrNotFound, got %v", err)
	}
	if count(missing) != 0 {
		t.Fatal("MustExist inserted a row")
	}

	deleted := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: deleted}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureUser(ctx, Identity{ID: deleted, MustExist: true}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("a deleted user: want ErrDeleted, got %v", err)
	}
	if count(deleted) != 0 {
		t.Fatal("MustExist recreated a deleted user")
	}

	// An existing row, fresh and stale: returned as stored, identity untouched, last_seen_at moves.
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id, Email: "a@example.com", Provider: "apple", Name: "Stored Name"}); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []bool{false, true} {
		if stale {
			if _, err := pool.Exec(ctx, `UPDATE users SET last_seen_at = now() - interval '1 hour' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		u, err := st.EnsureUser(ctx, Identity{ID: id, MustExist: true})
		if err != nil {
			t.Fatalf("stale=%v: %v", stale, err)
		}
		if u.ID != id || u.Email == nil || *u.Email != "a@example.com" || u.Provider == nil || *u.Provider != "apple" || u.Name != "Stored Name" {
			t.Fatalf("stale=%v: the stored identity changed: %+v", stale, u)
		}
	}
	var fresh bool
	if err := pool.QueryRow(ctx, `SELECT last_seen_at > now() - interval '5 minutes' FROM users WHERE id=$1`, id).Scan(&fresh); err != nil || !fresh {
		t.Fatalf("last_seen_at was not touched: %v %v", fresh, err)
	}
}
