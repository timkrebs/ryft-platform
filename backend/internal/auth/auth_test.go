package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

type fakeVerifier struct {
	p   Principal
	err error
}

func (f fakeVerifier) Verify(context.Context, string) (Principal, error) { return f.p, f.err }
func (f fakeVerifier) Ready(context.Context) error                       { return nil }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func serve(t *testing.T, o Options, header string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var seen *http.Request
	h := Middleware(o, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r; w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

func TestMiddlewareRejectsMissingOrMalformedBearer(t *testing.T) {
	o := Options{Log: discard, Verifier: fakeVerifier{}}
	for _, header := range []string{"", "Basic abc", "Bearer ", "Bearer" + strings.Repeat("x", 10)} {
		rec, seen := serve(t, o, header)
		if rec.Code != 401 || seen != nil {
			t.Errorf("%q: status %d", header, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
			t.Errorf("%q: missing WWW-Authenticate", header)
		}
	}
	if rec, _ := serve(t, o, "Bearer "+strings.Repeat("x", maxTokenLen+1)); rec.Code != 401 {
		t.Errorf("oversized token: %d", rec.Code)
	}
}

func TestMiddlewareStatusFollowsTheError(t *testing.T) {
	rec, _ := serve(t, Options{Log: discard, Verifier: fakeVerifier{err: Unauthorized("expired", nil)}}, "Bearer t")
	if rec.Code != 401 {
		t.Fatalf("401 expected, got %d", rec.Code)
	}
	rec, _ = serve(t, Options{Log: discard, Verifier: fakeVerifier{err: Unavailable("jwks_unavailable", nil)}}, "Bearer t")
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("503 with Retry-After expected, got %d %v", rec.Code, rec.Header())
	}
	// A failing user lookup (database down) must not look like a bad token.
	failing := Options{Log: discard, Verifier: fakeVerifier{p: Principal{UserID: uuid.New()}},
		OnAuth: func(ctx context.Context, _ Principal) (context.Context, error) { return ctx, errors.New("db down") }}
	if rec, _ = serve(t, failing, "bearer t"); rec.Code != 503 {
		t.Fatalf("OnAuth error must be 503, got %d", rec.Code)
	}
}

func TestMiddlewarePutsPrincipalAndUserInContext(t *testing.T) {
	id := uuid.New()
	o := Options{Log: discard, Verifier: fakeVerifier{p: Principal{UserID: id, Email: "a@b.c"}},
		OnAuth: LoadUser(func(_ context.Context, p Principal) (model.User, error) {
			return model.User{ID: p.UserID, TZ: "Europe/Berlin"}, nil
		})}
	rec, seen := serve(t, o, "Bearer good")
	if rec.Code != 204 || seen == nil {
		t.Fatalf("status %d", rec.Code)
	}
	p, ok := FromContext(seen.Context())
	u, uok := UserFrom(seen.Context())
	if !ok || !uok || p.UserID != id || u.ID != id || u.TZ != "Europe/Berlin" {
		t.Fatalf("principal %+v user %+v", p, u)
	}
}

func TestAPIKey(t *testing.T) {
	def := uuid.New()
	a := NewAPIKey("0123456789abcdef", def)
	if p, err := a.Verify(context.Background(), "0123456789abcdef"); err != nil || p.UserID != def || p.Provider != "apikey" {
		t.Fatalf("key alone acts as the configured user: %+v %v", p, err)
	}
	other := uuid.New()
	if p, err := a.Verify(context.Background(), "0123456789abcdef:"+other.String()); err != nil || p.UserID != other {
		t.Fatalf("key:uuid acts as that user: %+v %v", p, err)
	}
	for _, bad := range []string{"wrong", "0123456789abcdeX", "0123456789abcdef:not-a-uuid", ":" + other.String()} {
		if _, err := a.Verify(context.Background(), bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
