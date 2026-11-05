package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
	"github.com/timkrebs/ryft/internal/store"
)

// Email-bound invites rest on the Supabase project confirming emails (D159): while Confirm email is off,
// unknown, or the api cannot ask Supabase, bound codes are neither created nor redeemed.

// stubSettings is the project's public auth settings.
type stubSettings struct {
	mu    sync.Mutex
	auto  bool
	err   error
	calls int
}

func (s *stubSettings) EmailAutoconfirm(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.auto, s.err
}

func (s *stubSettings) set(auto bool, err error) {
	s.mu.Lock()
	s.auto, s.err = auto, err
	s.mu.Unlock()
}

func TestConfirmGuardStates(t *testing.T) {
	src := &stubSettings{}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	g := newConfirmGuard(src, quietHandler().log)
	g.now = func() time.Time { return now }
	ctx := context.Background()

	if g.State() != bindUnknown {
		t.Fatalf("before the first read: %s", g.State())
	}
	g.Refresh(ctx)
	if g.State() != bindOK || testutil.ToFloat64(metrics.InviteBindingAvailable) != 1 {
		t.Fatalf("Confirm email on: %s", g.State())
	}
	src.set(true, nil)
	g.Refresh(ctx)
	if g.State() != bindAutoconfirm || testutil.ToFloat64(metrics.InviteBindingAvailable) != 0 {
		t.Fatalf("Confirm email off: %s", g.State())
	}
	src.set(false, nil)
	g.Refresh(ctx)
	if g.State() != bindOK {
		t.Fatalf("on again: %s", g.State())
	}
	// A failed read is unknown at once: the last good answer is not kept.
	src.set(false, errors.New("supabase settings: 502 Bad Gateway: no error_code"))
	g.Refresh(ctx)
	if g.State() != bindUnknown || testutil.ToFloat64(metrics.InviteBindingAvailable) != 0 {
		t.Fatalf("unreadable: %s", g.State())
	}
	src.set(false, nil)
	g.Refresh(ctx)
	if g.State() != bindOK {
		t.Fatalf("readable again: %s", g.State())
	}
	// A good read that is too old (the loop is stuck) is unknown too.
	now = now.Add(bindingMaxAge + time.Minute)
	if g.State() != bindUnknown {
		t.Fatalf("a stale read: %s", g.State())
	}
	// No admin client: never available, nothing is read.
	n := newConfirmGuard(nil, quietHandler().log)
	n.Refresh(ctx)
	if n.State() != bindNoAdmin || testutil.ToFloat64(metrics.InviteBindingAvailable) != 0 {
		t.Fatalf("no admin client: %s", n.State())
	}
	// A handler without a gate cannot bind.
	if (&handler{}).bindingState() == bindOK {
		t.Fatal("a handler without a gate must fail closed")
	}
}

func TestConfirmGuardRunReadsAgain(t *testing.T) {
	src := &stubSettings{}
	g := newConfirmGuard(src, quietHandler().log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx, 10*time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for g.State() != bindOK {
		if time.Now().After(deadline) {
			t.Fatal("the loop never read the settings")
		}
		time.Sleep(5 * time.Millisecond)
	}
	src.set(true, nil)
	for g.State() != bindAutoconfirm {
		if time.Now().After(deadline) {
			t.Fatal("the loop never saw Confirm email turned off")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

// While bound invites do not work, the operator cannot make one (422 with the reason) and a bound code is
// not redeemable; unbound codes are untouched in every state.
func TestBoundInvitesRefuseWhileTheProjectDoesNotConfirmEmails(t *testing.T) {
	d := newAdminDB(t)
	ctx := context.Background()
	for _, c := range []struct {
		state string
		want  string
	}{
		{bindAutoconfirm, "email-bound invites are not available: the Supabase project confirms no emails (Confirm email is off)"},
		{bindUnknown, "email-bound invites are not available right now: the Supabase sign-up settings could not be read"},
		{bindNoAdmin, "email-bound invites are not available: the api has no Supabase admin key"},
	} {
		h := quietHandler()
		h.st, h.binding = d.st, fixedBinding(c.state)
		srv := adminRoutes(h, adminOptions{Key: testAdminKey, Limiter: ratelimit.New(1000, 1000), Timeout: 5 * time.Second})
		a := do(srv, "POST", "/v1/admin/invites", testAdminKey, `{"email": "anna@example.com"}`)
		if a.status != 422 || !strings.Contains(a.body, `"error":"`+c.want+`"`) || a.code() != "" {
			t.Errorf("%s: create with an email: %d %s", c.state, a.status, a.body)
		}
		if a := do(srv, "POST", "/v1/admin/invites", testAdminKey, `{"note": "plain", "max_uses": 2}`); a.status != 201 {
			t.Errorf("%s: an unbound code is made: %d %s", c.state, a.status, a.body)
		}
		// An invalid address is still the validation error, not the state's.
		if a := do(srv, "POST", "/v1/admin/invites", testAdminKey, `{"email": "nope"}`); !strings.Contains(a.body, "email is not a valid address") {
			t.Errorf("%s: %s", c.state, a.body)
		}
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM beta_invite_codes WHERE email IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no bound code was stored: %d %v", n, err)
	}

	// Redeeming: a bound code made while it worked, redeemed after Confirm email was turned off.
	r := newRedeemer(t)
	code := r.bound("anna@example.com")
	unbound := r.code(store.NewInvite{MaxUses: 5})
	unknownCode := r.redeem(r.user(), "RYFT-ZZZZ-ZZZZ")
	anna := r.user()
	r.emails.set(anna.ID, "anna@example.com", nil)

	r.h.binding = fixedBinding(bindAutoconfirm)
	rec := r.redeem(anna, code)
	if rec.Code != 422 || body(rec) != body(unknownCode) || r.emails.asked() != 0 {
		t.Errorf("Confirm email off: %d %s (asked Supabase %d times)", rec.Code, body(rec), r.emails.asked())
	}
	r.h.binding = fixedBinding(bindNoAdmin)
	if rec := r.redeem(anna, code); rec.Code != 422 || body(rec) != body(unknownCode) {
		t.Errorf("no admin client: %d %s", rec.Code, body(rec))
	}
	r.h.binding = fixedBinding(bindUnknown)
	rec = r.redeem(anna, code)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" || !strings.Contains(body(rec), "the invite could not be checked right now") ||
		strings.Contains(body(rec), `"code"`) || r.emails.asked() != 0 {
		t.Errorf("settings unknown: %d %v %s", rec.Code, rec.Header(), body(rec))
	}
	for _, state := range []string{bindAutoconfirm, bindUnknown, bindNoAdmin} {
		r.h.binding = fixedBinding(state)
		u := r.user()
		if rec := r.redeem(u, unbound); rec.Code != 204 {
			t.Errorf("%s: an unbound code does not depend on it: %d %s", state, rec.Code, body(rec))
		}
		// An unknown code is the same 422 in every state: the state is no oracle for a code that is not live and bound.
		if rec := r.redeem(r.user(), "RYFT-ZZZZ-ZZZZ"); rec.Code != 422 || body(rec) != body(unknownCode) {
			t.Errorf("%s: unknown code: %d %s", state, rec.Code, body(rec))
		}
	}
	if uses, _ := r.uses(code); uses != 0 {
		t.Errorf("a refused bound code spends nothing: %d", uses)
	}
	r.h.binding = fixedBinding(bindOK)
	if rec := r.redeem(anna, code); rec.Code != 204 {
		t.Errorf("Confirm email on again: %d %s", rec.Code, body(rec))
	}
}

// A code for one email always has an end: 14 days when none is named, at most 90 days away.
func TestBoundInviteExpiry(t *testing.T) {
	d := newAdminDB(t)
	post := func(body string) answer { return do(d.srv, "POST", "/v1/admin/invites", testAdminKey, body) }

	inv := decodeInvite(t, post(`{"email": "anna@example.com"}`))
	if inv.ExpiresAt == nil {
		t.Fatal("a bound code without expires_at gets one")
	}
	if left := time.Until(*inv.ExpiresAt); left < 14*24*time.Hour-time.Minute || left > 14*24*time.Hour+time.Minute {
		t.Fatalf("default expiry: %v", inv.ExpiresAt)
	}
	if inv.Status != model.InviteOpen {
		t.Errorf("status %s", inv.Status)
	}
	within := time.Now().Add(89 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if a := post(`{"email": "ben@example.com", "expires_at": "` + within + `"}`); a.status != 201 {
		t.Errorf("89 days: %d %s", a.status, a.body)
	}
	beyond := time.Now().Add(91 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if a := post(`{"email": "carl@example.com", "expires_at": "` + beyond + `"}`); a.status != http.StatusUnprocessableEntity ||
		!strings.Contains(a.body, `"error":"expires_at is at most 90 days away for a code for one email"`) {
		t.Errorf("91 days: %d %s", a.status, a.body)
	}
	// An unbound code keeps today's rule: no expiry unless named, and any date in the future.
	if u := decodeInvite(t, post(`{"note": "wave"}`)); u.ExpiresAt != nil {
		t.Errorf("an unbound code without expires_at never expires: %v", u.ExpiresAt)
	}
	if a := post(`{"note": "far", "expires_at": "` + beyond + `"}`); a.status != 201 {
		t.Errorf("an unbound code may expire later than 90 days: %d %s", a.status, a.body)
	}
	var n int
	if err := d.pool.QueryRow(context.Background(), `SELECT count(*) FROM beta_invite_codes WHERE email IS NOT NULL AND expires_at IS NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a bound code without an end: %d %v", n, err)
	}
}

// The access line logs the route pattern for the revoke, not the code in its path.
func TestRevokeAccessLineHoldsNoCode(t *testing.T) {
	buf := &bytes.Buffer{}
	d := newAdminDBLog(t, logx.NewWriter(buf, "api", slog.LevelDebug))
	inv := decodeInvite(t, do(d.srv, "POST", "/v1/admin/invites", testAdminKey, `{"note": "x"}`))
	for _, c := range []string{inv.Code, strings.ReplaceAll(inv.Code, "-", ""), "RYFT-ZZZZ-ZZZZ"} {
		if a := do(d.srv, "DELETE", "/v1/admin/invites/"+c, testAdminKey, ""); a.status != 204 && a.status != 404 {
			t.Fatal(a.status)
		}
	}
	out := buf.String()
	for _, secret := range []string{inv.Code, strings.ReplaceAll(inv.Code, "-", ""), "RYFT-ZZZZ-ZZZZ"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q in the logs:\n%s", secret, out)
		}
	}
	if n := strings.Count(out, `"method":"DELETE","path":"/v1/admin/invites/{code}"`); n != 3 {
		t.Errorf("want 3 access lines with the pattern, got %d:\n%s", n, out)
	}
}

// The purge job counts its passes, and both series exist at 0 from its start.
func TestPurgeInviteEmailsCountsItsPasses(t *testing.T) {
	r := newRedeemer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	okBefore := testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("ok"))
	go func() { purgeInviteEmailsEvery(ctx, r.st, quietHandler().log, 10*time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("ok")) < okBefore+2 {
		if time.Now().After(deadline) {
			t.Fatal("no passes counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	// A failing pass (the pool is closed) counts as an error.
	errBefore := testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("error"))
	r.pool.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	purgeInviteEmailsEvery(ctx2, r.st, quietHandler().log, 10*time.Millisecond)
	if testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("error")) <= errBefore {
		t.Error("a failed pass is counted as an error")
	}
}
