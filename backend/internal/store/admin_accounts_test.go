package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The allow-list of operator accounts: a row admits, deleting it revokes at once, and a deleted
// account's tombstone refuses even with a row (design 2026-11-05-admin-web §3.3).
func TestIsAdminAccount(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	admin, other := uuid.New(), uuid.New()
	is := func(id uuid.UUID) bool {
		t.Helper()
		ok, err := st.IsAdminAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if is(admin) {
		t.Fatal("no row, no admin")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (user_id, note) VALUES ($1, 'operator')`, admin); err != nil {
		t.Fatal(err)
	}
	if !is(admin) {
		t.Fatal("a row admits")
	}
	if is(other) {
		t.Fatal("another user is not admitted by it")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deleted_users (id) VALUES ($1)`, admin); err != nil {
		t.Fatal(err)
	}
	if is(admin) {
		t.Fatal("a tombstone refuses the account although its row is still there")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM deleted_users WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	if !is(admin) {
		t.Fatal("setup: admitted again")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM admin_accounts WHERE user_id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	if is(admin) {
		t.Fatal("deleting the row revokes on the next call")
	}
}

// The audit names its actor: a key call by its fingerprint, an admin-token call by the user id (stored
// with an empty fingerprint). Both together, or neither, are refused by the table.
func TestAuditActor(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	start := time.Now().Add(-time.Second)
	user := uuid.New()
	if err := st.Audit(ctx, AuditEntry{KeyFingerprint: "3fa1c2d9", Action: "GET /v1/admin/flags", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(ctx, AuditEntry{ActorUser: &user, Action: "GET /v1/admin/invites", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(ctx, AuditEntry{KeyFingerprint: "3fa1c2d9", ActorUser: &user, Action: "x", Status: 200}); err == nil {
		t.Error("a fingerprint and a user together are refused")
	}
	if err := st.Audit(ctx, AuditEntry{Action: "x", Status: 200}); err == nil {
		t.Error("an audit row without an actor is refused")
	}
	list, err := st.AuditSince(ctx, start, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("audit: %v %v", list, err)
	}
	u, k := list[0], list[1] // newest first
	if u.Actor.Kind != "user" || u.Actor.ID == nil || *u.Actor.ID != user.String() || u.KeyFingerprint != "" {
		t.Errorf("user actor: %+v", u)
	}
	if k.Actor.Kind != "key" || k.Actor.ID == nil || *k.Actor.ID != "3fa1c2d9" || k.KeyFingerprint != "3fa1c2d9" {
		t.Errorf("key actor: %+v", k)
	}
}
