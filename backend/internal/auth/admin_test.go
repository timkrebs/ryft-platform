package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fakeJWT(role string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(`{"iss":"supabase","role":"`+role+`"}`)) + ".sig"
}

func TestNewAdminChecksTheKeyKind(t *testing.T) {
	for key, ok := range map[string]bool{
		"sb_secret_abc123":          true,
		"sb_secret_change-me":       false,
		fakeJWT("service_role"):     true,
		fakeJWT("anon"):             false,
		"sb_publishable_abc123":     false,
		"":                          false,
		"just-some-password-string": false,
	} {
		if _, err := NewAdmin("https://x.supabase.co/auth/v1", key, nil); (err == nil) != ok {
			t.Errorf("%.20s…: err %v, want ok=%v", key, err, ok)
		}
	}
}

func TestAdminDeleteUser(t *testing.T) {
	id := uuid.New()
	var got *http.Request
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"msg":"x"}`))
	}))
	t.Cleanup(srv.Close)

	secret, _ := NewAdmin(srv.URL+"/auth/v1/", "sb_secret_abc", srv.Client())
	if err := secret.DeleteUser(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodDelete || got.URL.Path != "/auth/v1/admin/users/"+id.String() {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("apikey") != "sb_secret_abc" || got.Header.Get("Authorization") != "" {
		t.Fatalf("a secret key goes in apikey only, never as the bearer: %v", got.Header)
	}

	legacy, _ := NewAdmin(srv.URL+"/auth/v1", fakeJWT("service_role"), srv.Client())
	if err := legacy.DeleteUser(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("apikey") == "" || !strings.HasPrefix(got.Header.Get("Authorization"), "Bearer ey") {
		t.Fatalf("a service_role JWT goes in both headers: %v", got.Header)
	}

	status = http.StatusNotFound
	if err := secret.DeleteUser(context.Background(), id); err != nil {
		t.Fatalf("404 means already deleted: %v", err)
	}
	for _, s := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		status = s
		if err := secret.DeleteUser(context.Background(), id); err == nil {
			t.Errorf("%d must be an error (retried later)", s)
		}
	}
}

func TestAdminGetUser(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/auth/v1/admin/users/"+id.String() || r.Header.Get("apikey") != "sb_secret_abc123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + id.String() + `","email":"x7k2@privaterelay.appleid.com",
			"last_sign_in_at":"2026-10-20T17:40:00Z",
			"identities":[{"provider":"apple","identity_data":{"email":"x7k2@privaterelay.appleid.com"},"last_sign_in_at":"2026-10-20T17:40:00Z"},
			              {"provider":"email","identity_data":{"email":"tim@example.com"}}]}`))
	}))
	defer srv.Close()
	a, err := NewAdmin(srv.URL+"/auth/v1", "sb_secret_abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.GetUser(context.Background(), id)
	if err != nil || u.Email != "x7k2@privaterelay.appleid.com" || u.LastSignInAt == nil || len(u.Identities) != 2 ||
		u.Identities[0].Provider != "apple" || u.Identities[1].Email != "tim@example.com" || u.Identities[1].LastSignInAt != nil {
		t.Fatalf("user: %+v %v", u, err)
	}
	if _, err := a.GetUser(context.Background(), uuid.New()); !errors.Is(err, ErrNoAuthUser) {
		t.Fatalf("unknown user: %v", err)
	}
}
