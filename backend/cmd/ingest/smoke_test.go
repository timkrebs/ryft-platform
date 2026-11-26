package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// D286: ingest lets the smoke token reach /v1/sync with any method (the smoke expects ingest's own
// 404 for a GET) and nothing else.
func TestSmokeAllowIsSyncOnly(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if !smokeAllow(httptest.NewRequest(method, "/v1/sync", nil)) {
			t.Errorf("%s /v1/sync must be allowed", method)
		}
	}
	for _, path := range []string{"/v1/sync/", "/v1/syncx", "/v1/wearables/healthkit/sync", "/v1/me", "/v1/", "/"} {
		if smokeAllow(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("POST %s must be refused", path)
		}
	}
}

// smokeStore is ensureUser's store without a database.
type smokeStore struct {
	ensure   error
	bound    bool
	boundErr error
	boundFor []uuid.UUID
	ensured  int // EnsureUser calls: it locks and writes the row, so a refused principal must make none
}

func (s *smokeStore) EnsureUser(_ context.Context, id store.Identity) (model.User, error) {
	s.ensured++
	if s.ensure != nil {
		return model.User{}, s.ensure
	}
	return model.User{ID: id.ID}, nil
}

func (s *smokeStore) IsSmokeAccount(_ context.Context, id uuid.UUID) (bool, error) {
	s.boundFor = append(s.boundFor, id)
	return s.bound, s.boundErr
}

// D286: the smoke principal must be the bound account, checked first (an unbound or unreadable
// binding never reaches EnsureUser, which would write a real user's row) and on every request; a
// database error is no pass.
func TestEnsureUserSmokeBinding(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	reasonOf := func(err error) (int, string) {
		var ae *auth.Error
		if !errors.As(err, &ae) {
			return 0, ""
		}
		return ae.Status, ae.Reason
	}

	bound := &smokeStore{bound: true}
	for i := 0; i < 2; i++ {
		if u, err := ensureUser(bound)(ctx, auth.Principal{UserID: id, Smoke: true}); err != nil || u.ID != id {
			t.Fatalf("a bound smoke account: %+v %v", u, err)
		}
	}
	if len(bound.boundFor) != 2 || bound.boundFor[0] != id {
		t.Fatalf("the binding is checked on every request, for the principal: %v", bound.boundFor)
	}

	unbound := &smokeStore{}
	u, err := ensureUser(unbound)(ctx, auth.Principal{UserID: id, Smoke: true})
	if status, reason := reasonOf(err); status != http.StatusUnauthorized || reason != "smoke_unbound" || u.ID != uuid.Nil {
		t.Fatalf("an existing user that is not bound: want 401 smoke_unbound and no user, got %d %q %+v", status, reason, u)
	}
	if unbound.ensured != 0 {
		t.Fatalf("an unbound principal must not reach EnsureUser, it ran %d times", unbound.ensured)
	}

	for name, st := range map[string]*smokeStore{
		"no users row": {ensure: store.ErrNotFound, bound: true},
		"deleted":      {ensure: store.ErrDeleted, bound: true},
	} {
		_, err := ensureUser(st)(ctx, auth.Principal{UserID: id, Smoke: true})
		if status, reason := reasonOf(err); status != http.StatusUnauthorized || reason != "smoke_unknown_user" {
			t.Errorf("%s: want 401 smoke_unknown_user, got %d %q", name, status, reason)
		}
		if len(st.boundFor) != 1 || st.ensured != 1 {
			t.Errorf("%s: the binding is read first, then the row: %d reads, %d loads", name, len(st.boundFor), st.ensured)
		}
	}

	down := &smokeStore{boundErr: errors.New("db down"), bound: true}
	u, err = ensureUser(down)(ctx, auth.Principal{UserID: id, Smoke: true})
	if status, _ := reasonOf(err); err == nil || status != 0 || u.ID != uuid.Nil {
		t.Fatalf("a binding lookup error is a plain error (Middleware answers 503), never a pass or a 401: %+v %v", u, err)
	}
	if down.ensured != 0 {
		t.Fatalf("an unreadable binding must not reach EnsureUser, it ran %d times", down.ensured)
	}

	app := &smokeStore{}
	if u, err := ensureUser(app)(ctx, auth.Principal{UserID: id}); err != nil || u.ID != id || len(app.boundFor) != 0 {
		t.Fatalf("an app user is not asked about the binding: %+v %v %v", u, err, app.boundFor)
	}
}
