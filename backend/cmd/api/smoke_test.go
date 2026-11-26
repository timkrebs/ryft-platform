package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// smokeAllow is exactly the routes scripts/smoke_prod.sh calls with the smoke token.
func TestSmokeAllowIsExactlyTheSmokeScript(t *testing.T) {
	allowed := []string{
		"GET /v1/me", "POST /v1/me/invite", "GET /v1/days", "GET /v1/days/2026-11-26", "GET /v1/config",
		"GET /v1/model", "GET /v1/me/connections",
	}
	refused := []string{
		// Consent is a person's act: the smoke bearer neither grants nor withdraws it (D286).
		"POST /v1/me/consent", "DELETE /v1/me/consent", "PUT /v1/me/consent", "PATCH /v1/me/consent",
		"DELETE /v1/me", "PATCH /v1/me", "POST /v1/me", "PUT /v1/me", "HEAD /v1/me", "GET /v1/me/", "GET /v1/me/sync",
		"POST /v1/me/apple-authorization", "GET /v1/me/nutrition-export", "POST /v1/train/import",
		"GET /v1/nutrition/meals", "POST /v1/nutrition/meals", "DELETE /v1/nutrition/meals/1", "POST /v1/recompute",
		"POST /v1/days", "DELETE /v1/days/2026-11-26", "GET /v1/days/", "GET /v1/days/2026-11-26/x", "GET /v1/days/a/b",
		"GET /v1/daysx", "GET /v1/day/2026-11-26", "POST /v1/me/connections/garmin/start", "DELETE /v1/me/connections/garmin",
		"GET /v1/admin/users", "POST /v1/sync", "GET /v1/V1/me", "GET /", "GET /v1/me/consent", "POST /v1/me/invite/x",
	}
	for _, line := range allowed {
		method, path, _ := strings.Cut(line, " ")
		if !smokeAllow(httptest.NewRequest(method, path, nil)) {
			t.Errorf("%s must be allowed", line)
		}
	}
	for _, line := range refused {
		method, path, _ := strings.Cut(line, " ")
		if smokeAllow(httptest.NewRequest(method, path, nil)) {
			t.Errorf("%s must be refused", line)
		}
	}
}

// smokeStore is ensureUser's store without a database.
type smokeStore struct {
	user     model.User
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
	u := s.user
	u.ID = id.ID
	return u, nil
}

func (s *smokeStore) IsSmokeAccount(_ context.Context, id uuid.UUID) (bool, error) {
	s.boundFor = append(s.boundFor, id)
	return s.bound, s.boundErr
}

// D286: the smoke principal must be the bound account, checked first (an unbound or unreadable
// binding never reaches EnsureUser, which would write a real user's row) and on every request; a
// database error is not a pass. App users never ask.
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
	for i := 0; i < 2; i++ { // every request, not once
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

	// A database error is a 503 (a plain error to auth.Middleware), never a pass or a 401.
	down := &smokeStore{boundErr: errors.New("db down"), bound: true}
	if u, err := ensureUser(down)(ctx, auth.Principal{UserID: id, Smoke: true}); err == nil || u.ID != uuid.Nil {
		t.Fatalf("binding lookup error must fail the request: %+v %v", u, err)
	} else if status, _ := reasonOf(err); status != 0 {
		t.Fatalf("it is no *auth.Error, so Middleware answers 503, got %v", err)
	}
	if down.ensured != 0 {
		t.Fatalf("an unreadable binding must not reach EnsureUser, it ran %d times", down.ensured)
	}

	app := &smokeStore{}
	if u, err := ensureUser(app)(ctx, auth.Principal{UserID: id}); err != nil || u.ID != id || len(app.boundFor) != 0 {
		t.Fatalf("an app user is not asked about the binding: %+v %v %v", u, err, app.boundFor)
	}
}

type smokeVerifier struct{ p auth.Principal }

func (v smokeVerifier) Verify(context.Context, string) (auth.Principal, error) { return v.p, nil }
func (smokeVerifier) Ready(context.Context) error                              { return nil }

// D286 through the real routes: the smoke token is refused (403 smoke_route) before the handler on
// every route off the allowlist, DELETE /v1/me and the training import included, while an app user's
// token goes through to the handler.
func TestSmokePrincipalIsRefusedOffTheAllowlist(t *testing.T) {
	log := quietHandler().log
	loaded := 0
	mux := func(p auth.Principal) http.Handler {
		h := quietHandler()
		beta := config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}
		m := newMux(h, muxOptions{log: log, verifier: smokeVerifier{p}, ready: func(context.Context) error { return nil },
			tick: http.NotFoundHandler(), gate: beta.Gate, attest: config.AppAttest{Mode: config.ModeOff}, authMode: config.AuthAPIKey,
			clients: httpx.NewClientKeys(1),
			loadUser: func(_ context.Context, p auth.Principal) (model.User, error) {
				loaded++
				return model.User{ID: p.UserID, TZ: "UTC"}, nil
			}})
		return httpx.Recover(m)
	}
	do := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	smoke := mux(auth.Principal{UserID: uuid.New(), Smoke: true})
	for _, line := range []string{
		"DELETE /v1/me", "POST /v1/train/import", "PATCH /v1/me", "POST /v1/me/apple-authorization",
		"GET /v1/me/nutrition-export", "POST /v1/nutrition/meals", "GET /v1/nutrition/daily", "POST /v1/recompute",
		"POST /v1/feedback", "GET /v1/workouts", "DELETE /v1/me/connections/garmin",
	} {
		method, path, _ := strings.Cut(line, " ")
		rec := do(smoke, method, path)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"error":"forbidden"`) {
			t.Errorf("%s: want 403 forbidden, got %d %s", line, rec.Code, rec.Body.String())
		}
	}
	if loaded != 0 {
		t.Fatalf("a refused route must not load the user: %d", loaded)
	}

	// Consent is a person's legal act: both writes are refused for the smoke principal with the
	// smoke_route reason (the access line's code and the auth_failures label; the body is the plain
	// 403), before the handler and before the user lookup.
	refusals := func() float64 { return testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("smoke_route")) }
	for _, line := range []string{"POST /v1/me/consent", "DELETE /v1/me/consent"} {
		method, path, _ := strings.Cut(line, " ")
		before := refusals()
		rec := do(smoke, method, path)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"error":"forbidden"`) {
			t.Errorf("%s with the smoke token: want 403 forbidden, got %d %s", line, rec.Code, rec.Body.String())
		}
		if got := refusals() - before; got != 1 {
			t.Errorf("%s: smoke_route counted %v times, want 1", line, got)
		}
	}
	if loaded != 0 {
		t.Fatalf("a refused consent write must not load the user: %d", loaded)
	}

	// The allowlist lets an allowed route past the middleware, to the user lookup and the handler.
	// (The handler has no store here: Recover turns that into a 500, which is "not the 403".)
	if rec := do(smoke, "GET", "/v1/model"); rec.Code == http.StatusForbidden || loaded != 1 {
		t.Fatalf("GET /v1/model must pass the allowlist: %d, users loaded %d", rec.Code, loaded)
	}

	// An app user's DELETE /v1/me and import reach their handlers, as before.
	app := mux(auth.Principal{UserID: uuid.New()})
	// A normal user's consent POST and DELETE are unchanged: past the middleware, no smoke_route.
	before := refusals()
	for _, line := range []string{"DELETE /v1/me", "POST /v1/train/import", "PATCH /v1/me", "POST /v1/me/consent", "DELETE /v1/me/consent"} {
		method, path, _ := strings.Cut(line, " ")
		if rec := do(app, method, path); rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), `"error":"forbidden"`) {
			t.Errorf("app user %s refused as the smoke: %s", line, rec.Body.String())
		}
	}
	if got := refusals() - before; got != 0 {
		t.Errorf("an app user was counted as smoke_route %v times", got)
	}
}

// D286: the admin chain refuses a smoke principal before the role, the allow-list and the second
// factor, even one a verifier gave an operator's claims and an allow-list row.
func TestAdminAuthRefusesTheSmokePrincipal(t *testing.T) {
	op := uuid.New()
	accounts := newFakeAccounts(op)
	v := smokeVerifier{p: auth.Principal{UserID: op, Smoke: true, AppRole: adminRole, AAL: "aal2", MFAAt: time.Now()}}
	srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: v, Accounts: accounts})
	a := do(srv, "GET", "/v1/admin/flags", "a.b.c", "")
	if a.status != http.StatusUnauthorized || len(*actors) != 0 || accounts.calls != 0 {
		t.Fatalf("a smoke principal is no operator: %d %s, handler ran %d times, allow-list read %d times", a.status, a.body, len(*actors), accounts.calls)
	}
}
