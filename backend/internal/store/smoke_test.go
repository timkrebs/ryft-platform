package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// D286: only the bound account is the smoke account; a real user who is not bound, and an ID with
// no users row at all, are not.
func TestIsSmokeAccount(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	bound, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{bound, other} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	// The operator binds it (scripts/sql/bind_smoke_account.sql); no service role writes the table.
	if _, err := pool.Exec(ctx, `INSERT INTO smoke_account (user_id) VALUES ($1)`, bound); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"bound":               {bound, true},
		"existing, not bound": {other, false},
		"no users row at all": {uuid.New(), false},
		"the nil UUID":        {uuid.Nil, false},
		"bound, asked again":  {bound, true},
	} {
		got, err := st.IsSmokeAccount(ctx, tc.id)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", name, got, err, tc.want)
		}
	}
}
