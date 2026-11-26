package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/store"
)

// D286: the smoke principal never creates a users row. A missing or deleted user is a 401
// smoke_unknown_user; an existing one loads, with its stored identity untouched.
func TestEnsureUserSmokePrincipalNeverInserts(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()
	load := ensureUser(st)
	rows := func(id uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	refusedAs := func(err error, reason string) bool {
		var ae *auth.Error
		return errors.As(err, &ae) && ae.Status == http.StatusUnauthorized && ae.Reason == reason
	}
	refused := func(err error) bool { return refusedAs(err, "smoke_unknown_user") }

	missing := uuid.New()
	if _, err := load(ctx, auth.Principal{UserID: missing, Smoke: true}); !refused(err) {
		t.Fatalf("a missing smoke user: want 401 smoke_unknown_user, got %v", err)
	}
	if rows(missing) != 0 {
		t.Fatal("the smoke principal inserted a users row")
	}

	gone := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: gone}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := load(ctx, auth.Principal{UserID: gone, Smoke: true}); !refused(err) {
		t.Fatalf("a deleted smoke user: want 401 smoke_unknown_user, got %v", err)
	}

	id := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: id, Email: "smoke@example.com", Provider: "apple"}); err != nil {
		t.Fatal(err)
	}
	// A real user that nobody bound as the smoke account (a wrong SMOKE_USER_ID) is refused.
	if _, err := load(ctx, auth.Principal{UserID: id, Smoke: true}); !refusedAs(err, "smoke_unbound") {
		t.Fatalf("an existing user that is not bound: want 401 smoke_unbound, got %v", err)
	}
	// The operator binds it (scripts/sql/bind_smoke_account.sql); no service role writes the table.
	if _, err := pool.Exec(ctx, `INSERT INTO smoke_account (user_id) VALUES ($1)`, id); err != nil {
		t.Fatal(err)
	}
	u, err := load(ctx, auth.Principal{UserID: id, Smoke: true})
	if err != nil || u.ID != id || u.Email == nil || *u.Email != "smoke@example.com" {
		t.Fatalf("a bound smoke user: %+v %v", u, err)
	}
	// Another real user stays refused, and so does an ID that only Supabase Auth knows (no users row).
	other := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := load(ctx, auth.Principal{UserID: other, Smoke: true}); !refusedAs(err, "smoke_unbound") {
		t.Fatalf("another existing user: want 401 smoke_unbound, got %v", err)
	}
	if _, err := load(ctx, auth.Principal{UserID: uuid.New(), Smoke: true}); !refused(err) {
		t.Fatalf("a Supabase-only ID: want 401 smoke_unknown_user, got %v", err)
	}

	// An app user's token still creates the row, as before.
	app := uuid.New()
	if _, err := load(ctx, auth.Principal{UserID: app}); err != nil || rows(app) != 1 {
		t.Fatalf("an app user's first request: %v, rows %d", err, rows(app))
	}
}
