package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func (s *stubSettings) EmailAutoconfirm(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err := ctx.Err(); err != nil { // as the HTTP client does
		return false, err
	}
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
	if (&handler{}).bindingState(ctx) == bindOK {
		t.Fatal("a handler without a gate must fail closed")
	}
}

// The api asks Supabase nothing at start (an instance starts on a request, D219): the guard is built without
// a read, and the first request that needs the answer reads it once. A tick's read spares the next requests
// one, and a request reads again only when this instance has not tried for lazyReadAfter, since a tick
// reaches one instance only. Until the read says Confirm email is on, bound codes are refused (D159).
func TestConfirmGuardReadsLazily(t *testing.T) {
	src := &stubSettings{}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	g := newConfirmGuard(src, quietHandler().log)
	g.now = func() time.Time { return now }
	h := &handler{binding: g}
	calls := func() int { src.mu.Lock(); defer src.mu.Unlock(); return src.calls }
	ctx := context.Background()

	if calls() != 0 || g.State() != bindUnknown {
		t.Fatalf("at start: %d reads, state %s: nothing is read, and unknown refuses bound codes", calls(), g.State())
	}
	if got := h.bindingState(ctx); got != bindOK || calls() != 1 {
		t.Fatalf("the first request that needs it: %s after %d reads, want ok after one", got, calls())
	}
	now = now.Add(lazyReadAfter - time.Second)
	if got := h.bindingState(ctx); got != bindOK || calls() != 1 {
		t.Fatalf("a request within %s: %s after %d reads, want no second read", lazyReadAfter, got, calls())
	}
	g.Refresh(ctx) // the tick's "invite settings"
	if calls() != 2 {
		t.Fatalf("the tick reads: %d", calls())
	}
	now = now.Add(lazyReadAfter - time.Second)
	if h.bindingState(ctx); calls() != 2 {
		t.Fatalf("the tick's read counts as this instance's try: %d reads", calls())
	}
	now = now.Add(2 * time.Second)
	src.set(true, nil)
	if got := h.bindingState(ctx); got != bindAutoconfirm || calls() != 3 {
		t.Fatalf("a request after %s without a try: %s after %d reads, want the new answer", lazyReadAfter, got, calls())
	}

	// A failed lazy read is unknown (fail closed) and is not repeated by the next request.
	f := &stubSettings{err: errors.New("supabase settings: dial tcp: i/o timeout")}
	fg := newConfirmGuard(f, quietHandler().log)
	fg.now = func() time.Time { return now }
	fh := &handler{binding: fg}
	if got := fh.bindingState(ctx); got != bindUnknown {
		t.Fatalf("unreadable: %s", got)
	}
	fh.bindingState(ctx)
	if f.calls != 1 {
		t.Fatalf("a failed read is retried after %s, not by every request: %d reads", lazyReadAfter, f.calls)
	}

	// A request whose client hung up still reads to the end: the read is the instance's, not the request's.
	c := &stubSettings{}
	cg := newConfirmGuard(c, quietHandler().log)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if got := (&handler{binding: cg}).bindingState(gone); got != bindOK {
		t.Fatalf("a cancelled request's read: %s", got)
	}

	// Without an admin client nothing is ever read.
	n := &handler{binding: newConfirmGuard(nil, quietHandler().log)}
	if got := n.bindingState(ctx); got != bindNoAdmin {
		t.Fatalf("no admin client: %s", got)
	}
}

// A tick's read that its budget cut short records nothing (as db.Ping): not unknown, not a try. Otherwise the
// instance would refuse bound codes and, the try being recent, not read again on a request for 15 minutes.
func TestConfirmGuardRefreshCutByTheBudget(t *testing.T) {
	src := &stubSettings{}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	g := newConfirmGuard(src, quietHandler().log)
	g.now = func() time.Time { return now }
	h := &handler{binding: g}
	calls := func() int { src.mu.Lock(); defer src.mu.Unlock(); return src.calls }
	failed := testutil.ToFloat64(metrics.InviteSettingsChecks.WithLabelValues("error"))
	spent, cancel := context.WithTimeout(context.Background(), time.Hour)
	cancel() // the tick's budget, gone while the step runs

	g.Refresh(spent)
	if calls() != 1 || g.State() != bindUnknown || !g.tried.IsZero() {
		t.Fatalf("a cut first read: %d reads, state %s, tried %v: want one read, unknown, no try recorded", calls(), g.State(), g.tried)
	}
	if got := h.bindingState(context.Background()); got != bindOK || calls() != 2 {
		t.Fatalf("the next request: %s after %d reads, want ok after its own read", got, calls())
	}
	now = now.Add(time.Minute)
	g.Refresh(spent)
	if g.State() != bindOK || !g.tried.Equal(now.Add(-time.Minute)) {
		t.Fatalf("a cut later read: state %s, tried %v: the good answer and its try stay", g.State(), g.tried)
	}
	if n := testutil.ToFloat64(metrics.InviteSettingsChecks.WithLabelValues("error")) - failed; n != 0 {
		t.Fatalf("cut reads counted as %v errors, want 0", n)
	}
	// A read that fails on its own, with the tick's budget left, is still unknown.
	src.set(false, errors.New("supabase settings: 502 Bad Gateway: no error_code"))
	g.Refresh(context.Background())
	if g.State() != bindUnknown {
		t.Fatalf("a failed read: %s", g.State())
	}
}

// Requests that need the answer at once share the one lazy read.
func TestConfirmGuardLazyReadIsShared(t *testing.T) {
	src := &stubSettings{}
	g := newConfirmGuard(src, quietHandler().log)
	h := &handler{binding: g}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := h.bindingState(context.Background()); got != bindOK {
				t.Errorf("state %s", got)
			}
		}()
	}
	wg.Wait()
	if src.calls != 1 {
		t.Fatalf("%d reads for 8 requests at once, want 1", src.calls)
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
		srv := adminRoutes(h, adminOptions{Key: testAdminKey, Limiter: ratelimit.NewCapped(1000, 1000, maxAdminAddrs), Timeout: 5 * time.Second})
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

// The purge step counts its passes; a failed one is counted, logged under the runbook's line and returned
// for the tick. Both series exist at 0 from the start (purgeSeries), so increase() sees an instance's first pass.
func TestPurgeInviteEmailsCountsItsPasses(t *testing.T) {
	purgeSeries()
	if n := testutil.CollectAndCount(metrics.InviteEmailPurges); n < 2 {
		t.Fatalf("%d series after purgeSeries, want ok and error", n)
	}
	r := newRedeemer(t)
	ctx := context.Background()
	okBefore := testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("ok"))
	for range 2 {
		if err := purgeInviteEmails(ctx, r.st, quietHandler().log); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("ok")) - okBefore; got != 2 {
		t.Fatalf("ok rose by %v, want 2", got)
	}
	// A failing pass (the pool is closed) counts as an error.
	errBefore := testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("error"))
	r.pool.Close()
	var logs bytes.Buffer
	err := purgeInviteEmails(ctx, r.st, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err == nil || testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("error")) != errBefore+1 {
		t.Errorf("a failed pass = %v, counted %v: it is returned and counted as an error", err,
			testutil.ToFloat64(metrics.InviteEmailPurges.WithLabelValues("error"))-errBefore)
	}
	if !strings.Contains(logs.String(), `"msg":"invite email purge failed"`) {
		t.Errorf("the runbook's line: %s", logs.String())
	}
}

// The wired retention is the 30 days the privacy text will name (D161), end to end through the tick's step: a
// code that stopped being redeemable 29 days 23 hours ago keeps its address, one that stopped 30 days 1 hour ago
// does not.
func TestPurgeInviteEmailsWiredToThirtyDays(t *testing.T) {
	if purgeInviteEmailsAfter != 30*24*time.Hour {
		t.Fatalf("purgeInviteEmailsAfter = %v, the retention is 30 days (D161, the privacy text)", purgeInviteEmailsAfter)
	}
	r := newRedeemer(t)
	ctx := context.Background()
	inside, outside := r.bound("inside@example.com"), r.bound("outside@example.com")
	for code, ago := range map[string]time.Duration{inside: 30*24*time.Hour - time.Hour, outside: 30*24*time.Hour + time.Hour} {
		if _, err := r.pool.Exec(ctx, `UPDATE beta_invite_codes SET revoked_at = now() - $2::interval, expires_at = now() - $2::interval WHERE code = $1`,
			store.NormalizeInvite(code), fmt.Sprintf("%d seconds", int64(ago.Seconds()))); err != nil {
			t.Fatal(err)
		}
	}
	if err := purgeInviteEmails(ctx, r.st, quietHandler().log); err != nil {
		t.Fatal(err)
	}
	has := func(code string) bool {
		var e *string
		if err := r.pool.QueryRow(context.Background(), `SELECT email FROM beta_invite_codes WHERE code = $1`, store.NormalizeInvite(code)).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e != nil
	}
	if !has(inside) {
		t.Error("29 days 23 hours: the address is still kept")
	}
	if has(outside) {
		t.Error("30 days 1 hour: the address is cleared")
	}
}
