package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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

// The status in a refused admin call is the response's, not whatever the body
// or the status line says: a body whose "code" claims another status, or has
// none, or is not JSON, and a status line with free text for a reason phrase,
// still log the real status, the standard phrase and, from the body, only a
// well-formed error_code.
func TestAdminErrorsLogTheResponseStatus(t *testing.T) {
	status, reason, body := 0, "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			status, reason, len(body), body)
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	a, _ := NewAdmin(srv.URL+"/auth/v1", "sb_secret_abc", srv.Client())
	for _, c := range []struct {
		name   string
		status int
		reason string
		body   string
		want   string
	}{
		{"body code claims success", 502, "Bad Gateway", `{"code":200,"error_code":"bad_jwt"}`, "502 Bad Gateway: bad_jwt"},
		{"body code claims another error", 401, "Unauthorized", `{"code":404,"error_code":"user_not_found"}`, "401 Unauthorized: user_not_found"},
		{"body code is a string", 503, "Service Unavailable", `{"code":"418","error_code":"over_request_rate_limit"}`, "503 Service Unavailable: over_request_rate_limit"},
		{"body without a code", 500, "Internal Server Error", `{"error_code":"unexpected_failure"}`, "500 Internal Server Error: unexpected_failure"},
		{"empty body", 502, "Bad Gateway", ``, "502 Bad Gateway: no error_code"},
		{"body not JSON", 403, "Forbidden", `<html>403 for tim@example.com</html>`, "403 Forbidden: no error_code"},
		{"body code only", 429, "Too Many Requests", `{"code":200,"msg":"tim@example.com"}`, "429 Too Many Requests: no error_code"},
		{"free text in the reason phrase", 500, "tim@example.com is down", `{"error_code":"unexpected_failure"}`, "500 Internal Server Error: unexpected_failure"},
	} {
		status, reason, body = c.status, c.reason, c.body
		_, gerr := a.GetUser(context.Background(), uuid.New())
		derr := a.DeleteUser(context.Background(), uuid.New())
		for op, err := range map[string]error{"get user": gerr, "delete user": derr} {
			if err == nil || err.Error() != "supabase admin: "+op+": "+c.want {
				t.Errorf("%s (%s): %v, want %q", c.name, op, err, c.want)
			}
		}
	}
}

// A refused admin call is logged by the api and shown in the admin view: its
// error holds the status and GoTrue's error_code, never the body's free text.
func TestAdminErrorsHoldNoBody(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	a, _ := NewAdmin(srv.URL+"/auth/v1", "sb_secret_abc", srv.Client())
	for b, code := range map[string]string{
		`{"code":500,"error_code":"unexpected_failure","msg":"cannot load tim@example.com"}`: "unexpected_failure",
		`<html>tim@example.com is down</html>`:                                               "no error_code",
		`{"error_code":"tim@example.com"}`:                                                   "no error_code",
	} {
		body = b
		_, gerr := a.GetUser(context.Background(), uuid.New())
		derr := a.DeleteUser(context.Background(), uuid.New())
		for _, err := range []error{gerr, derr} {
			if err == nil || !strings.Contains(err.Error(), "500 Internal Server Error: "+code) || strings.Contains(err.Error(), "tim@") {
				t.Errorf("%s: %v, want the status and %q only", b, err, code)
			}
		}
	}
}
