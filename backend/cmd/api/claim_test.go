package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// fakeClaimer counts the claims and fails them when err is set.
type fakeClaimer struct {
	calls []struct{ user, device uuid.UUID }
	err   error
}

func (f *fakeClaimer) ClaimDevice(_ context.Context, user, device uuid.UUID) (int, error) {
	f.calls = append(f.calls, struct{ user, device uuid.UUID }{user, device})
	return 0, f.err
}

func claimCount(result string) float64 {
	return testutil.ToFloat64(metrics.PushDeviceClaims.WithLabelValues(result))
}

// The middleware alone: which requests claim, and that a failed claim never fails the request.
func TestClaimDeviceMiddleware(t *testing.T) {
	user, device, other := uuid.New(), uuid.New(), uuid.New()
	for _, c := range []struct {
		name   string
		header *string // nil: no header
		list   []uuid.UUID
		err    error
		claims int
		failed int
		method string // "" is GET
		path   string // "" is /v1/me
	}{
		{name: "the nil UUID", header: ptr(uuid.Nil.String()), claims: 0},
		{name: "a route App Attest exempts", header: ptr(device.String()), method: http.MethodPost, path: "/v1/app-attest/challenge", claims: 0},
		{name: "the exempt sign-out DELETE", header: ptr(device.String()), method: http.MethodDelete, path: "/v1/devices/" + device.String() + "/push", claims: 0},
		{name: "the registration POST is not exempt", header: ptr(device.String()), method: http.MethodPost, path: "/v1/devices/" + device.String() + "/push", claims: 1},
		{name: "no header", list: nil, claims: 0},
		{name: "malformed header", header: ptr("not-a-uuid"), claims: 0},
		{name: "empty header", header: ptr(""), claims: 0},
		{name: "the hot path: the install is in the list", header: ptr(device.String()), list: []uuid.UUID{other, device}, claims: 0},
		{name: "the install is new", header: ptr(device.String()), list: []uuid.UUID{other}, claims: 1},
		{name: "an empty list", header: ptr(device.String()), claims: 1},
		{name: "a failed claim is served", header: ptr(device.String()), err: errors.New("deadlock detected"), claims: 1, failed: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			fake := &fakeClaimer{err: c.err}
			served := false
			h := claimDevice(fake, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				served = true
				w.WriteHeader(http.StatusNoContent)
			}))
			method, path := c.method, c.path
			if method == "" {
				method = http.MethodGet
			}
			if path == "" {
				path = "/v1/me"
			}
			req := httptest.NewRequest(method, path, nil)
			if c.header != nil {
				req.Header.Set(DeviceHeader, *c.header)
			}
			claimed, failed := claimCount("claimed"), claimCount("failed")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, asUser(req, model.User{ID: user, PushDevices: c.list}, ""))
			if !served || rec.Code != http.StatusNoContent {
				t.Fatalf("the request must be served: served %v, status %d", served, rec.Code)
			}
			if len(fake.calls) != c.claims {
				t.Fatalf("%d claims, want %d", len(fake.calls), c.claims)
			}
			if c.claims == 1 && (fake.calls[0].user != user || fake.calls[0].device != device) {
				t.Fatalf("claimed %+v, want user %s device %s", fake.calls[0], user, device)
			}
			if d := claimCount("failed") - failed; d != float64(c.failed) {
				t.Errorf("failed counter moved by %v, want %d", d, c.failed)
			}
			if d := claimCount("claimed") - claimed; d != float64(c.claims-c.failed) {
				t.Errorf("claimed counter moved by %v, want %d", d, c.claims-c.failed)
			}
			if c.failed == 1 {
				if !strings.Contains(logs.String(), "claim device failed") {
					t.Errorf("a failed claim is logged: %q", logs.String())
				}
				if strings.Contains(logs.String(), device.String()) {
					t.Errorf("the log carries the device id: %q", logs.String())
				}
			} else if logs.Len() != 0 {
				t.Errorf("nothing to log: %q", logs.String())
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// claimBench is the api's real routes and chain on a test database, called with API-key bearers.
type claimBench struct {
	t    *testing.T
	srv  http.Handler
	pool *pgxpool.Pool
	st   *store.Store
}

const claimAPIKey = "device-claim-test-key-0123456789"

func newClaimBench(t *testing.T, gate config.Mode) *claimBench {
	t.Helper()
	pool := testPool(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	h := &handler{st: st, log: log, cache: newInsightCache(), beta: defaultBeta()}
	mux := newMux(h, muxOptions{log: log, verifier: auth.NewAPIKey(claimAPIKey, uuid.New()),
		ready: func(context.Context) error { return nil }, tick: http.NotFoundHandler(),
		gate: gate, authMode: config.AuthAPIKey})
	return &claimBench{t: t, srv: httpx.Instrument(log, nil, httpx.Recover(httpx.SecurityHeaders(mux))), pool: pool, st: st}
}

// do sends a request as user; device "" sends no X-RYFT-Device-ID. It answers the status and body.
func (b *claimBench) do(user uuid.UUID, method, path, body, device string) (int, string) {
	b.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+claimAPIKey+":"+user.String())
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if device != "" {
		req.Header.Set(DeviceHeader, device)
	}
	rec := httptest.NewRecorder()
	b.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (b *claimBench) mustDo(want int, user uuid.UUID, method, path, body, device string) {
	b.t.Helper()
	if got, resp := b.do(user, method, path, body, device); got != want {
		b.t.Fatalf("%s %s: %d %s, want %d", method, path, got, resp, want)
	}
}

// register is POST /v1/devices/{id}/push by user, sent from the install header (device) or none.
func (b *claimBench) register(want int, user uuid.UUID, id uuid.UUID, token, header string) {
	b.t.Helper()
	b.mustDo(want, user, http.MethodPost, "/v1/devices/"+id.String()+"/push",
		`{"token":"`+token+`","env":"sandbox","locale":"en"}`, header)
}

// owners are the accounts holding a token for the install, sorted.
func (b *claimBench) owners(device uuid.UUID) []string {
	b.t.Helper()
	rows, err := b.pool.Query(context.Background(), `SELECT user_id::text FROM push_tokens WHERE device_id=$1 ORDER BY 1`, device)
	if err != nil {
		b.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			b.t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		b.t.Fatal(err)
	}
	return out
}

// list is the account's push_devices.
func (b *claimBench) list(user uuid.UUID) []uuid.UUID {
	b.t.Helper()
	var l []uuid.UUID
	if err := b.pool.QueryRow(context.Background(), `SELECT push_devices FROM users WHERE id=$1`, user).Scan(&l); err != nil {
		b.t.Fatal(err)
	}
	return l
}

func (b *claimBench) wantOwners(device uuid.UUID, users ...uuid.UUID) {
	b.t.Helper()
	want := []string{}
	for _, u := range users {
		want = append(want, u.String())
	}
	slices.Sort(want)
	if got := b.owners(device); !slices.Equal(got, want) {
		b.t.Fatalf("tokens of device %s are held by %v, want %v", device, got, want)
	}
}

func (b *claimBench) wantList(user uuid.UUID, devices ...uuid.UUID) {
	b.t.Helper()
	if got := b.list(user); !slices.Equal(got, devices) {
		b.t.Fatalf("push_devices of %s: %v, want %v", user, got, devices)
	}
}

// A token that matches the app's hex form, built at run time.
func claimToken(c string) string { return strings.Repeat(c, 64) }

// A shared iPhone: A registered a token for the install and its session ended without a DELETE.
// B's first request from the install drops A's token, with no POST by B. Then A's request from
// the install drops B's.
func TestClaimSharedPhone(t *testing.T) {
	b := newClaimBench(t, config.ModeOff)
	a, bb, d := uuid.New(), uuid.New(), uuid.New()

	b.register(http.StatusNoContent, a, d, claimToken("a"), d.String())
	b.wantOwners(d, a)
	b.wantList(a, d)

	b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", d.String())
	b.wantOwners(d)
	b.wantList(a)
	b.wantList(bb, d)

	b.register(http.StatusNoContent, bb, d, claimToken("b"), d.String())
	b.wantOwners(d, bb)

	b.mustDo(http.StatusOK, a, http.MethodGet, "/v1/me", "", d.String())
	b.wantOwners(d)
	b.wantList(a, d)
	b.wantList(bb)
}

// A's sign-out DELETE failed, so its row stays. B's first request ends it; A's token on another
// install, another account's token on another install, and the other installs' lists stay.
func TestClaimAfterAFailedDelete(t *testing.T) {
	b := newClaimBench(t, config.ModeOff)
	a, bb, c := uuid.New(), uuid.New(), uuid.New()
	d, d2, d3 := uuid.New(), uuid.New(), uuid.New()

	b.register(http.StatusNoContent, a, d, claimToken("a"), d.String())
	b.register(http.StatusNoContent, a, d2, claimToken("b"), d2.String())
	b.register(http.StatusNoContent, c, d3, claimToken("c"), d3.String())
	b.wantList(a, d, d2)

	b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", d.String())
	b.wantOwners(d)
	b.wantOwners(d2, a)
	b.wantOwners(d3, c)
	b.wantList(a, d2)
	b.wantList(c, d3)
	b.wantList(bb, d)
}

// The invite gate refuses B's POST with 403 invite_required, and A's token for the install is gone
// anyway: the claim runs before the gate. Without the header nothing is claimed.
func TestClaimBeforeTheInviteGate(t *testing.T) {
	b := newClaimBench(t, config.ModeOn)
	a, bb, d := uuid.New(), uuid.New(), uuid.New()
	ctx := context.Background()
	for _, u := range []uuid.UUID{a, bb} {
		if _, err := b.st.EnsureUser(ctx, store.Identity{ID: u}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.st.RegisterPushToken(ctx, a, d, model.PushRegistration{Token: claimToken("a"), Env: "sandbox", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	b.wantOwners(d, a)

	// No header: the gate refuses, nothing is claimed.
	code, body := b.do(bb, http.MethodPost, "/v1/devices/"+d.String()+"/push", `{"token":"`+claimToken("b")+`","env":"sandbox","locale":"en"}`, "")
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"invite_required"`) {
		t.Fatalf("without the header: %d %s", code, body)
	}
	b.wantOwners(d, a)
	b.wantList(bb)

	// With it: the same refusal, and A's row is gone.
	code, body = b.do(bb, http.MethodPost, "/v1/devices/"+d.String()+"/push", `{"token":"`+claimToken("b")+`","env":"sandbox","locale":"en"}`, d.String())
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"invite_required"`) {
		t.Fatalf("with the header: %d %s", code, body)
	}
	b.wantOwners(d)
	b.wantList(a)
	b.wantList(bb, d)
}

// The second request from (B, D) runs no claim: the counters stay, and a token another account
// registered for D in between (written straight into the table) is not touched. A request without
// the header, or with a malformed one, claims nothing.
func TestClaimOnlyOnce(t *testing.T) {
	b := newClaimBench(t, config.ModeOff)
	a, bb, d, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ctx := context.Background()
	if _, err := b.st.EnsureUser(ctx, store.Identity{ID: a}); err != nil {
		t.Fatal(err)
	}
	// Written straight in, so A's list does not hold D (as with a row from before the claim).
	if _, err := b.pool.Exec(ctx, `INSERT INTO push_tokens (user_id, device_id, token, env, locale) VALUES ($1, $2, $3, 'sandbox', 'en')`,
		a, d, claimToken("a")); err != nil {
		t.Fatal(err)
	}

	for _, header := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-00000000000"} {
		b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", header)
		b.wantOwners(d, a)
		b.wantList(bb)
	}

	claimed := claimCount("claimed")
	b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", d.String())
	if got := claimCount("claimed") - claimed; got != 1 {
		t.Fatalf("the first request from the install ran %v claims, want 1", got)
	}
	b.wantOwners(d)
	b.wantList(bb, d)

	// A token written for A in between survives B's next requests: they run no claim.
	if _, err := b.pool.Exec(ctx, `INSERT INTO push_tokens (user_id, device_id, token, env, locale) VALUES ($1, $2, $3, 'sandbox', 'en')`,
		a, d, claimToken("b")); err != nil {
		t.Fatal(err)
	}
	claimed = claimCount("claimed")
	for range 3 {
		b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", d.String())
	}
	if got := claimCount("claimed") - claimed; got != 0 {
		t.Fatalf("later requests from the install ran %v claims, want 0", got)
	}
	b.wantOwners(d, a)
	b.wantList(bb, d)

	// Another account's token on another install is untouched by all of it.
	d2 := uuid.New()
	b.register(http.StatusNoContent, other, d2, claimToken("c"), d2.String())
	b.mustDo(http.StatusOK, bb, http.MethodGet, "/v1/me", "", d.String())
	b.wantOwners(d2, other)
}

// A POST for the same {id} by the same account replaces its token: one row.
func TestRegisterSameInstallReplacesTheToken(t *testing.T) {
	b := newClaimBench(t, config.ModeOff)
	a, d := uuid.New(), uuid.New()
	b.register(http.StatusNoContent, a, d, claimToken("a"), d.String())
	b.register(http.StatusNoContent, a, d, claimToken("b"), "")
	b.wantOwners(d, a)
	var token string
	if err := b.pool.QueryRow(context.Background(), `SELECT token FROM push_tokens WHERE user_id=$1 AND device_id=$2`, a, d).Scan(&token); err != nil || token != claimToken("b") {
		t.Fatalf("the token is %q (%v), want the second", token, err)
	}
	b.wantList(a, d)
}
