package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// The actor fails closed: the zero adminActor is neither the key nor a user, so it gets neither the whole
// view of the users list and the feedback nor a key-only route, and nothing is logged or counted as either.
func TestZeroAdminActorFailsClosed(t *testing.T) {
	key, user, zero := adminActor{fingerprint: "3fa1c2d9"}, adminActor{user: uuid.New()}, adminActor{}
	if zero.isKey() || zero.isUser() || zero.kind() != "" || zero.label() != "unknown" {
		t.Errorf("zero: key %v user %v kind %q label %q", zero.isKey(), zero.isUser(), zero.kind(), zero.label())
	}
	if !key.isKey() || key.isUser() || key.kind() != "key" || !user.isUser() || user.isKey() || user.kind() != "user" {
		t.Fatalf("a key is a key and a user is a user: %+v %+v", key, user)
	}
	ask := func(a *adminActor) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), adminActorKey{}, *a))
		}
		return r
	}
	for name, tc := range map[string]struct {
		r    *http.Request
		want bool
	}{"key": {ask(&key), true}, "user": {ask(&user), false}, "zero": {ask(&zero), false}, "none": {ask(nil), false}} {
		if got := wholeView(tc.r); got != tc.want {
			t.Errorf("wholeView for %s: %v, want %v", name, got, tc.want)
		}
	}
	// A key-only route refuses the zero actor without running the handler.
	a := admin{h: quietHandler()}
	ran := false
	h := a.keyOnly(func(http.ResponseWriter, *http.Request) { ran = true })
	rec := httptest.NewRecorder()
	h(rec, ask(&zero))
	if ran || rec.Code != http.StatusForbidden {
		t.Errorf("the zero actor on a key-only route: ran %v, %d", ran, rec.Code)
	}
	h(httptest.NewRecorder(), ask(&key))
	if !ran {
		t.Error("the key still reaches a key-only route")
	}
	// No admin_actor attribute names a zero actor.
	if attrs := adminLogAttrs(ask(&zero).Context()); attrs != nil {
		t.Errorf("log attributes for the zero actor: %v", attrs)
	}
}
