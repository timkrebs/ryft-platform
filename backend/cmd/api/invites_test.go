package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Invites for a person (docs/work/2026-11-05-admin-web/design.md §6; PR2 acceptance criteria and abuse
// cases). The redeem tests run the handler on a real Postgres with a fake of the Supabase admin lookup; the
// admin tests run the real chain (CORS, limit, bearer check, audit) with the key and with an operator's token.

// fixedBinding is a bindingGate in one state (the settings guard without Supabase).
type fixedBinding string

func (f fixedBinding) State() string { return string(f) }

// fakeEmails is the Supabase admin lookup: the confirmed email per account ("" for none or unconfirmed), and
// an error for an outage. It counts the lookups, which is how the tests see when Supabase is asked.
type fakeEmails struct {
	mu    sync.Mutex
	email map[uuid.UUID]string
	err   error
	calls int
}

func newFakeEmails() *fakeEmails { return &fakeEmails{email: map[uuid.UUID]string{}} }

func (f *fakeEmails) VerifiedEmail(_ context.Context, id uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.email[id], f.err
}

func (f *fakeEmails) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeEmails) set(id uuid.UUID, email string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.email[id], f.err = email, err
}

// redeemer is the handler of POST /v1/me/invite on a database, with the lookup faked.
type redeemer struct {
	t      *testing.T
	h      *handler
	st     *store.Store
	pool   *pgxpool.Pool
	emails *fakeEmails
}

func newRedeemer(t *testing.T) redeemer {
	pool := testPool(t)
	st := store.New(pool)
	r := redeemer{t: t, st: st, pool: pool, emails: newFakeEmails()}
	r.h = &handler{st: st, log: quietHandler().log, cache: newInsightCache(), emails: r.emails, binding: fixedBinding(bindOK)}
	return r
}

func (r redeemer) user() model.User {
	r.t.Helper()
	u, err := r.st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		r.t.Fatal(err)
	}
	return u
}

func (r redeemer) code(in store.NewInvite) string {
	r.t.Helper()
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	in.CreatedBy = "key"
	if in.Email != nil && in.ExpiresAt == nil {
		in.ExpiresAt = ptrTo(time.Now().Add(14 * 24 * time.Hour))
	}
	inv, err := r.st.CreateInvite(context.Background(), in)
	if err != nil {
		r.t.Fatal(err)
	}
	return inv.Code
}

func (r redeemer) bound(email string) string { return r.code(store.NewInvite{Email: &email}) }

func (r redeemer) redeem(u model.User, code string) *httptest.ResponseRecorder {
	r.t.Helper()
	fresh, _, err := r.st.GetUser(context.Background(), u.ID) // the per-request user row, as LoadUser reads it
	if err != nil {
		r.t.Fatal(err)
	}
	return call(r.t, r.h.redeemInvite, fresh, "POST", "/v1/me/invite", `{"code": "`+code+`"}`)
}

func (r redeemer) access(u model.User) bool {
	r.t.Helper()
	g, _, err := r.st.GetUser(context.Background(), u.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return g.BetaAccess
}

// uses reads the spent count and the redemption rows of a code, as the operator's list shows them.
func (r redeemer) uses(code string) (uses, rows int) {
	r.t.Helper()
	list, err := r.st.Invites(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	if i, ok := findInvite(list, code); ok {
		return i.Uses, len(i.UsesBy)
	}
	r.t.Fatalf("code %s is not listed", code)
	return 0, 0
}

func body(rec *httptest.ResponseRecorder) string { return rec.Body.String() }

// A code bound to an email is redeemed by the account whose Supabase-confirmed email it is, and by nobody
// else; every refusal is the same 422, byte for byte.
func TestRedeemBoundInviteAPI(t *testing.T) {
	r := newRedeemer(t)
	code := r.bound("anna@example.com")

	// Refused: the answer is the one an unknown code gets.
	unknown := r.redeem(r.user(), "RYFT-ZZZZ-ZZZZ")
	if unknown.Code != 422 || !strings.Contains(body(unknown), `"error":"invalid or used code"`) || strings.Contains(body(unknown), `"code"`) {
		t.Fatalf("the reference refusal: %d %s", unknown.Code, body(unknown))
	}
	for _, c := range []struct{ name, confirmed string }{
		{"another confirmed email", "ben@example.com"},
		{"no confirmed email (unconfirmed, or none)", ""},
		{"a plus address of it", "anna+beta@example.com"},
		{"a relay address", "x7k2m9q4@privaterelay.appleid.com"},
	} {
		u := r.user()
		r.emails.set(u.ID, c.confirmed, nil)
		rec := r.redeem(u, code)
		if rec.Code != unknown.Code || body(rec) != body(unknown) || rec.Header().Get("Retry-After") != "" {
			t.Errorf("%s: %d %s, want the unknown code's answer %s", c.name, rec.Code, body(rec), body(unknown))
		}
		if r.access(u) {
			t.Errorf("%s: access granted", c.name)
		}
	}
	if uses, rows := r.uses(code); uses != 0 || rows != 0 {
		t.Fatalf("refusals spend nothing: %d %d", uses, rows)
	}

	// Accepted: the confirmed email matches, whatever the case Supabase holds it in.
	anna := r.user()
	r.emails.set(anna.ID, "Anna@Example.COM", nil)
	if rec := r.redeem(anna, code); rec.Code != 204 || body(rec) != "" {
		t.Fatalf("redeem: %d %s", rec.Code, body(rec))
	}
	if !r.access(anna) {
		t.Fatal("access after the code")
	}
	if uses, rows := r.uses(code); uses != 1 || rows != 1 {
		t.Fatalf("one use, one row: %d %d", uses, rows)
	}
	// Replay by someone else with the same address: used up, the same 422.
	again := r.user()
	r.emails.set(again.ID, "anna@example.com", nil)
	if rec := r.redeem(again, code); rec.Code != 422 || body(rec) != body(unknown) {
		t.Errorf("replay: %d %s", rec.Code, body(rec))
	}
}

// Supabase is asked only for a code that exists, is open and is bound; if it cannot answer, only that case
// is a 503, so the 503 is no oracle for anything a guesser does not already hold.
func TestRedeemSupabaseDown(t *testing.T) {
	r := newRedeemer(t)
	down := errors.New("supabase admin: get user: connection refused")
	bound := r.bound("anna@example.com")
	unbound := r.code(store.NewInvite{MaxUses: 5})
	revokedBound := r.bound("ben@example.com")
	if err := r.st.RevokeInvite(context.Background(), revokedBound); err != nil {
		t.Fatal(err)
	}
	usedBound := r.bound("carl@example.com")
	carl := r.user()
	r.emails.set(carl.ID, "carl@example.com", nil)
	if rec := r.redeem(carl, usedBound); rec.Code != 204 {
		t.Fatal(rec.Code)
	}

	u := r.user()
	r.emails.set(u.ID, "", down)
	before := r.emails.asked()

	rec := r.redeem(u, bound)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" || !strings.Contains(body(rec), `"error":"the invite could not be checked right now"`) ||
		strings.Contains(body(rec), `"code"`) || strings.Contains(body(rec), "connection refused") {
		t.Fatalf("bound code, Supabase down: %d %v %s", rec.Code, rec.Header(), body(rec))
	}
	if r.emails.asked() != before+1 {
		t.Fatalf("the lookup runs once: %d", r.emails.asked()-before)
	}
	if r.access(u) {
		t.Fatal("no access on a 503")
	}
	if uses, rows := r.uses(bound); uses != 0 || rows != 0 {
		t.Fatalf("a 503 spends nothing: %d %d", uses, rows)
	}

	// Everything else answers as it does when Supabase is up, and never asks.
	before = r.emails.asked()
	for _, c := range []struct {
		name, code string
		want       int
	}{
		{"unknown code", "RYFT-ZZZZ-ZZZZ", 422}, {"garbage", "garbage", 422}, {"revoked bound code", revokedBound, 422},
		{"used-up bound code", usedBound, 422},
	} {
		rec := r.redeem(u, c.code)
		if rec.Code != c.want || rec.Header().Get("Retry-After") != "" || !strings.Contains(body(rec), "invalid or used code") {
			t.Errorf("%s: %d %s", c.name, rec.Code, body(rec))
		}
	}
	if rec := r.redeem(u, unbound); rec.Code != 204 {
		t.Errorf("an unbound code does not depend on Supabase: %d %s", rec.Code, body(rec))
	}
	if r.emails.asked() != before {
		t.Errorf("Supabase was asked %d times for codes that are not open and bound", r.emails.asked()-before)
	}
	// An account that has access gets 204 first, whatever it sends and whatever Supabase does.
	if rec := r.redeem(u, bound); rec.Code != 204 || r.emails.asked() != before {
		t.Errorf("an account with access: %d, asked %d", rec.Code, r.emails.asked()-before)
	}
	// After the outage the tester sends the same request again.
	r.emails.set(u.ID, "", nil)
	other := r.user()
	r.emails.set(other.ID, "anna@example.com", down)
	if rec := r.redeem(other, bound); rec.Code != 503 {
		t.Fatal(rec.Code)
	}
	r.emails.set(other.ID, "anna@example.com", nil)
	if rec := r.redeem(other, bound); rec.Code != 204 {
		t.Fatalf("the retry after the outage: %d %s", rec.Code, body(rec))
	}
}

// Guessing and enumeration: every refusal has one status and one body, and what the server did differs only
// where the design accepts it (a lookup after a correct guess of a live, bound code).
func TestRedeemRefusalsAreIndistinguishable(t *testing.T) {
	r := newRedeemer(t)
	ctx := context.Background()
	soon := time.Now().Add(time.Hour)
	expired := r.code(store.NewInvite{ExpiresAt: &soon})
	if _, err := r.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = now() - interval '1 hour' WHERE code = $1`, store.NormalizeInvite(expired)); err != nil {
		t.Fatal(err)
	}
	revoked := r.code(store.NewInvite{})
	if err := r.st.RevokeInvite(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	used := r.code(store.NewInvite{})
	if rec := r.redeem(r.user(), used); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	bound := r.bound("anna@example.com")
	mine := r.user()
	r.emails.set(mine.ID, "ben@example.com", nil)

	type reply struct {
		status int
		body   string
		retry  string
		ctype  string
	}
	var replies []reply
	for _, c := range []string{"RYFT-ZZZZ-ZZZZ", "RYFT-2222-2222", "ryft", "", "   ", "RYFT-0O1I-LLLL", "%00", strings.Repeat("A", 500),
		"RYFT-2222-3333", expired, revoked, used, bound} {
		rec := r.redeem(mine, c)
		replies = append(replies, reply{rec.Code, body(rec), rec.Header().Get("Retry-After"), rec.Header().Get("Content-Type")})
	}
	for k, rep := range replies {
		if rep != replies[0] || rep.status != 422 {
			t.Errorf("attempt %d: %+v differs from %+v", k, rep, replies[0])
		}
	}
	if r.access(mine) {
		t.Fatal("no refusal grants access")
	}
	// The one lookup ran for the bound code and for nothing else.
	if n := r.emails.asked(); n != 1 {
		t.Errorf("Supabase was asked %d times, want 1 (the live bound code)", n)
	}
}

// The same account cannot use a code twice: after the operator takes its access away, the code it used is a
// 422 for it, and its use is not counted again.
func TestRedeemReplayBySameAccount(t *testing.T) {
	r := newRedeemer(t)
	ctx := context.Background()
	code := r.code(store.NewInvite{MaxUses: 3})
	u := r.user()
	if rec := r.redeem(u, code); rec.Code != 204 {
		t.Fatalf("first: %d %s", rec.Code, body(rec))
	}
	// With access: 204 and nothing spent.
	if rec := r.redeem(u, code); rec.Code != 204 {
		t.Fatalf("with access: %d", rec.Code)
	}
	if uses, _ := r.uses(code); uses != 1 {
		t.Fatalf("uses %d", uses)
	}
	if err := r.st.SetBetaAccess(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if rec := r.redeem(u, code); rec.Code != 422 || !strings.Contains(body(rec), "invalid or used code") {
		t.Fatalf("replay after the operator took access away: %d %s", rec.Code, body(rec))
	}
	if uses, rows := r.uses(code); uses != 1 || rows != 1 || r.access(u) {
		t.Fatalf("uses %d rows %d access %v", uses, rows, r.access(u))
	}
	// Another account can still use the code.
	if rec := r.redeem(r.user(), code); rec.Code != 204 {
		t.Fatalf("another account: %d", rec.Code)
	}
}

// Two accounts redeeming the last use of a code through the handler at once: exactly one 204 and one 422.
func TestRedeemConcurrentAccountsAPI(t *testing.T) {
	r := newRedeemer(t)
	for i := 0; i < 30; i++ {
		var code string
		a, b := r.user(), r.user()
		if i%2 == 0 {
			code = r.code(store.NewInvite{})
		} else {
			code = r.bound("anna@example.com")
			r.emails.set(a.ID, "anna@example.com", nil)
			r.emails.set(b.ID, "anna@example.com", nil)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		got := make([]int, 2)
		for k, u := range []model.User{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got[k] = r.redeem(u, code).Code
			}()
		}
		close(start)
		wg.Wait()
		if got[0]+got[1] != 204+422 || got[0] == got[1] {
			t.Fatalf("iteration %d: %v, want one 204 and one 422", i, got)
		}
		if uses, rows := r.uses(code); uses != 1 || rows != 1 {
			t.Fatalf("iteration %d: uses %d rows %d", i, uses, rows)
		}
	}
}

// accounts.VerifiedEmail is the lookup main wires: the Supabase admin API's email, only when it is confirmed.
func TestAccountsVerifiedEmail(t *testing.T) {
	id := uuid.New()
	status, reply := 200, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/auth/v1/admin/users/"+id.String() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	adm, err := auth.NewAdmin(srv.URL+"/auth/v1", "sb_secret_abc", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	a := &accounts{admin: adm}
	for _, c := range []struct {
		name   string
		status int
		reply  string
		want   string
		fail   bool
	}{
		{"confirmed", 200, `{"email":"Anna@example.com","email_confirmed_at":"2026-11-01T08:00:00Z"}`, "Anna@example.com", false},
		{"confirmed, but with a trailing space (refused, not trimmed)", 200, `{"email":"anna@example.com ","email_confirmed_at":"2026-11-01T08:00:00Z"}`, "", false},
		{"confirmed, but with a newline", 200, `{"email":"anna@example.com\n","email_confirmed_at":"2026-11-01T08:00:00Z"}`, "", false},
		{"confirmed, but not ASCII", 200, `{"email":"annä@example.com","email_confirmed_at":"2026-11-01T08:00:00Z"}`, "", false},
		{"unconfirmed", 200, `{"email":"anna@example.com"}`, "", false},
		{"unconfirmed, null", 200, `{"email":"anna@example.com","email_confirmed_at":null}`, "", false},
		{"no email (phone account)", 200, `{"email":"","email_confirmed_at":"2026-11-01T08:00:00Z"}`, "", false},
		{"no auth user", 404, `{}`, "", false},
		{"Supabase error", 500, `{"error_code":"unexpected_failure"}`, "", true},
		{"unreadable answer", 200, `{"email":`, "", true},
	} {
		status, reply = c.status, c.reply
		got, err := a.VerifiedEmail(context.Background(), id)
		if got != c.want || (err != nil) != c.fail {
			t.Errorf("%s: %q %v", c.name, got, err)
		}
	}
	// Without the admin client (apikey mode, or no service key) nothing is confirmed: bound codes never redeem.
	if got, err := (&accounts{}).VerifiedEmail(context.Background(), id); got != "" || err != nil {
		t.Errorf("no admin client: %q %v", got, err)
	}
	if got, err := (*accounts)(nil).VerifiedEmail(context.Background(), id); got != "" || err != nil {
		t.Errorf("nil accounts: %q %v", got, err)
	}
}

func TestInviteStatus(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ago, soon := now.Add(-time.Minute), now.Add(time.Minute)
	for _, c := range []struct {
		name string
		inv  model.BetaInvite
		want string
	}{
		{"open, no expiry", model.BetaInvite{MaxUses: 1}, model.InviteOpen},
		{"open, expires later", model.BetaInvite{MaxUses: 3, Uses: 2, ExpiresAt: &soon}, model.InviteOpen},
		{"expired", model.BetaInvite{MaxUses: 1, ExpiresAt: &ago}, model.InviteExpired},
		{"expires exactly now", model.BetaInvite{MaxUses: 1, ExpiresAt: &now}, model.InviteExpired},
		{"used", model.BetaInvite{MaxUses: 1, Uses: 1}, model.InviteUsed},
		{"used beats expired", model.BetaInvite{MaxUses: 1, Uses: 1, ExpiresAt: &ago}, model.InviteUsed},
		{"revoked", model.BetaInvite{MaxUses: 1, RevokedAt: &ago}, model.InviteRevoked},
		{"revoked beats used", model.BetaInvite{MaxUses: 1, Uses: 1, RevokedAt: &ago}, model.InviteRevoked},
		{"revoked beats expired and used", model.BetaInvite{MaxUses: 1, Uses: 1, ExpiresAt: &ago, RevokedAt: &ago}, model.InviteRevoked},
	} {
		if got := inviteStatus(c.inv, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// ---- the admin routes ---------------------------------------------------------------------------

func decodeInvite(t *testing.T, a answer) model.BetaInvite {
	t.Helper()
	var inv model.BetaInvite
	if err := json.Unmarshal([]byte(a.body), &inv); err != nil {
		t.Fatalf("%s: %v", a.body, err)
	}
	return inv
}

func decodeInvites(t *testing.T, a answer) []model.BetaInvite {
	t.Helper()
	var list []model.BetaInvite
	if err := json.Unmarshal([]byte(a.body), &list); err != nil {
		t.Fatalf("%s: %v", a.body, err)
	}
	return list
}

func findInvite(list []model.BetaInvite, code string) (model.BetaInvite, bool) {
	for _, i := range list {
		if i.Code == code {
			return i, true
		}
	}
	return model.BetaInvite{}, false
}

func TestAdminCreateInvite(t *testing.T) {
	d := newAdminDB(t)
	tok := d.token(d.op, nil)
	post := func(bearer, body string) answer { return do(d.srv, "POST", "/v1/admin/invites", bearer, body) }

	// Validation, with the contract's texts.
	long := strings.Repeat("a", 250) + "@example.com"
	for _, c := range []struct {
		body string
		want int
		msg  string
	}{
		{`{"email": "Anna@Example.com", "max_uses": 2}`, 422, "a code for one email has max_uses 1"},
		{`{"email": "anna@example.com", "max_uses": 1000}`, 422, "a code for one email has max_uses 1"},
		{`{"email": "not an email"}`, 422, "email is not a valid address"},
		{`{"email": ""}`, 422, "email is not a valid address"},
		{`{"email": "   "}`, 422, "email is not a valid address"},
		{`{"email": "anna@example"}`, 422, "email is not a valid address"},
		{`{"email": "anna@@example.com"}`, 422, "email is not a valid address"},
		{`{"email": "@example.com"}`, 422, "email is not a valid address"},
		{`{"email": "änna@example.com"}`, 422, "email is not a valid address"},
		{`{"email": "K@example.com"}`, 422, "email is not a valid address"},
		{`{"email": "anna@example.com\n"}`, 201, ""}, // surrounding whitespace is trimmed
		{`{"email": "` + long + `"}`, 422, "email is not a valid address"},
		{`{"email": 7}`, 400, ""},
		{`{"max_uses": 0, "email": "anna@example.com"}`, 422, "max_uses must be 1-1000"},
		{`{"note": "` + strings.Repeat("n", 201) + `", "email": "anna@example.com"}`, 422, "note has at most 200 characters"},
		{`{"expires_at": "2020-01-01T00:00:00Z", "email": "anna@example.com"}`, 422, "expires_at must be in the future"},
		{`{"email": null, "max_uses": 3}`, 201, ""},
		{`{"max_uses": 3}`, 201, ""},
	} {
		a := post(testAdminKey, c.body)
		if a.status != c.want {
			t.Errorf("%.60s: %d %s, want %d", c.body, a.status, a.body, c.want)
		}
		if c.msg != "" && !strings.Contains(a.body, `"error":"`+c.msg+`"`) {
			t.Errorf("%.60s: %s, want %q", c.body, a.body, c.msg)
		}
	}

	// A bound code: stored lower-cased, single use, shown as stored in the answer.
	exp := time.Now().Add(14 * 24 * time.Hour).UTC().Truncate(time.Second)
	a := post(tok, fmt.Sprintf(`{"note": "Anna", "email": " Anna@Example.com ", "expires_at": %q}`, exp.Format(time.RFC3339)))
	if a.status != 201 {
		t.Fatalf("create: %d %s", a.status, a.body)
	}
	inv := decodeInvite(t, a)
	if inv.Email == nil || *inv.Email != "anna@example.com" || inv.MaxUses != 1 || inv.Uses != 0 || inv.Status != model.InviteOpen ||
		inv.RevokedAt != nil || inv.Note != "Anna" || inv.ExpiresAt == nil || !inv.ExpiresAt.Equal(exp) ||
		inv.Link != "https://ryftapp.de/invite/"+inv.Code || !strings.HasPrefix(inv.Code, "RYFT-") ||
		inv.CreatedBy.Kind != model.ActorUser || inv.CreatedBy.ID == nil || *inv.CreatedBy.ID != d.op.String() {
		t.Fatalf("created by an operator's token: %+v", inv)
	}
	if !strings.Contains(a.body, `"uses_by":[]`) || !strings.Contains(a.body, `"revoked_at":null`) {
		t.Errorf("uses_by is [] and revoked_at null, not absent: %s", a.body)
	}
	var stored string
	if err := d.pool.QueryRow(context.Background(), `SELECT email FROM beta_invite_codes WHERE code = $1`, store.NormalizeInvite(inv.Code)).Scan(&stored); err != nil || stored != "anna@example.com" {
		t.Fatalf("stored: %q %v", stored, err)
	}

	// By the key: created_by names the fingerprint; no email, an unbound code.
	k := decodeInvite(t, post(testAdminKey, `{"note": "wave", "max_uses": 20}`))
	if k.Email != nil || k.MaxUses != 20 || k.CreatedBy.Kind != model.ActorKey || k.CreatedBy.ID == nil || *k.CreatedBy.ID != keyFingerprint(testAdminKey) {
		t.Fatalf("created by the key: %+v", k)
	}
	// The token and the key see the same list (a token never sees more, the key no less).

	// Audit: the code in the details, never the address.
	var rows int
	for _, rec := range d.audit() {
		if rec.Action != "POST /v1/admin/invites" || rec.Status != 201 {
			continue
		}
		var det map[string]any
		if err := json.Unmarshal(rec.Details, &det); err != nil || len(det) != 1 || det["code"] == nil {
			t.Errorf("audit details: %s", rec.Details)
		}
		if strings.Contains(string(rec.Details), "@") || strings.Contains(string(rec.Details), "example.com") {
			t.Errorf("an address in the audit details: %s", rec.Details)
		}
		if rec.Actor.Kind == model.ActorUser && (rec.Actor.ID == nil || *rec.Actor.ID != d.op.String()) {
			t.Errorf("actor: %+v", rec.Actor)
		}
		rows++
	}
	if rows < 3 {
		t.Errorf("audit rows for creates: %d", rows)
	}
}

// The list masks emails unless ?emails=1, for the key and for a token alike (the M1 projection rule); the
// call is audited with whether they were asked for.
func TestAdminListInvites(t *testing.T) {
	d := newAdminDB(t)
	ctx := context.Background()
	tok := d.token(d.op, nil)
	mk := func(body string) model.BetaInvite {
		t.Helper()
		a := do(d.srv, "POST", "/v1/admin/invites", testAdminKey, body)
		if a.status != 201 {
			t.Fatalf("%s: %d %s", body, a.status, a.body)
		}
		return decodeInvite(t, a)
	}
	user := func(email string) uuid.UUID {
		u, err := d.st.EnsureUser(ctx, store.Identity{ID: uuid.New(), Email: email})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	redeem := func(id uuid.UUID, code string) {
		t.Helper()
		if _, err := d.st.RedeemInvite(ctx, id, code, ""); err != nil {
			t.Fatal(err)
		}
	}
	bound := mk(`{"note": "Anna", "email": "anna@example.com"}`)
	bound.Email = ptrTo("anna@example.com")
	annaID := user("anna@example.com")
	if _, err := d.st.RedeemInvite(ctx, annaID, bound.Code, "anna@example.com"); err != nil {
		t.Fatal(err)
	}
	open := mk(`{"note": "wave", "max_uses": 5}`)
	relayID, mia := user("x7k2m9q4@privaterelay.appleid.com"), user("mia@icloud.com")
	redeem(relayID, open.Code)
	time.Sleep(10 * time.Millisecond)
	redeem(mia, open.Code)
	revoked := mk(`{"email": "ben@example.com"}`)
	if a := do(d.srv, "DELETE", "/v1/admin/invites/"+revoked.Code, tok, ""); a.status != 204 {
		t.Fatal(a.status)
	}
	expired := mk(`{"note": "late"}`)
	if _, err := d.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = now() - interval '1 hour' WHERE code = $1`, store.NormalizeInvite(expired.Code)); err != nil {
		t.Fatal(err)
	}

	for _, bearer := range []string{testAdminKey, tok} {
		a := do(d.srv, "GET", "/v1/admin/invites", bearer, "")
		if a.status != 200 || !strings.HasPrefix(a.body, "[") {
			t.Fatalf("list: %d %s", a.status, a.body)
		}
		for _, raw := range []string{"anna@example.com", "ben@example.com", "mia@icloud.com", "x7k2m9q4@privaterelay"} {
			if strings.Contains(a.body, raw) {
				t.Errorf("an address in the list without ?emails=1: %s in %s", raw, a.body)
			}
		}
		// Every @ in the bytes of the default answer is part of a masked form (x***@y***.tld): the
		// projection holds for the key and for a token alike, whatever the field.
		if bad := regexp.MustCompile(`[^*]@`).FindString(a.body); bad != "" {
			t.Errorf("an unmasked address in the default answer (%q): %s", bad, a.body)
		}
		list := decodeInvites(t, a)
		if len(list) != 4 {
			t.Fatalf("listed: %d", len(list))
		}
		anna, _ := findInvite(list, bound.Code)
		if anna.Email == nil || *anna.Email != "a***@e***.com" || anna.Status != model.InviteUsed || len(anna.UsesBy) != 1 ||
			anna.UsesBy[0].Email == nil || *anna.UsesBy[0].Email != "a***@e***.com" || anna.UsesBy[0].UserID != annaID ||
			anna.UsesBy[0].ShortID != annaID.String()[:8] || anna.UsesBy[0].PrivateRelay {
			t.Errorf("masked, used: %+v", anna)
		}
		wave, _ := findInvite(list, open.Code)
		if wave.Email != nil || wave.Status != model.InviteOpen || wave.Uses != 2 || len(wave.UsesBy) != 2 || wave.MaxUses != 5 ||
			wave.UsesBy[0].UserID != relayID || wave.UsesBy[1].UserID != mia ||
			!wave.UsesBy[0].PrivateRelay || wave.UsesBy[1].PrivateRelay ||
			*wave.UsesBy[1].Email != "m***@i***.com" || *wave.UsesBy[0].Email != "x***@p***.com" ||
			wave.CreatedBy.Kind != model.ActorKey || *wave.CreatedBy.ID != keyFingerprint(testAdminKey) {
			t.Errorf("an unbound code with two uses, oldest first, the relay flagged: %+v", wave)
		}
		if r, _ := findInvite(list, revoked.Code); r.Status != model.InviteRevoked || r.RevokedAt == nil || r.Email == nil || *r.Email != "b***@e***.com" {
			t.Errorf("revoked: %+v", r)
		}
		if e, _ := findInvite(list, expired.Code); e.Status != model.InviteExpired {
			t.Errorf("expired: %+v", e)
		}
		for _, i := range list {
			if i.Link != "https://ryftapp.de/invite/"+i.Code {
				t.Errorf("link: %s", i.Link)
			}
			if i.UsesBy == nil {
				t.Errorf("uses_by is [] and never null: %+v", i)
			}
		}
		// Newest first.
		if list[0].Code != expired.Code || list[3].Code != bound.Code {
			t.Errorf("order: %v ... %v", list[0].Code, list[3].Code)
		}

		a = do(d.srv, "GET", "/v1/admin/invites?emails=1", bearer, "")
		list = decodeInvites(t, a)
		anna, _ = findInvite(list, bound.Code)
		wave, _ = findInvite(list, open.Code)
		if anna.Email == nil || *anna.Email != "anna@example.com" || *anna.UsesBy[0].Email != "anna@example.com" ||
			*wave.UsesBy[0].Email != "x7k2m9q4@privaterelay.appleid.com" || !wave.UsesBy[0].PrivateRelay {
			t.Errorf("?emails=1: %+v %+v", anna, wave)
		}
		// Anything but exactly 1 is not a request for addresses.
		for _, q := range []string{"emails=true", "emails=0", "emails=", "emails=1x", "email=1"} {
			if a := do(d.srv, "GET", "/v1/admin/invites?"+q, bearer, ""); strings.Contains(a.body, "anna@example.com") {
				t.Errorf("?%s shows addresses", q)
			}
		}
	}
	// The audit knows whether addresses were asked for.
	var asked, plain int
	for _, rec := range d.audit() {
		if rec.Action != "GET /v1/admin/invites" {
			continue
		}
		var det struct {
			Emails bool `json:"emails"`
		}
		_ = json.Unmarshal(rec.Details, &det)
		if det.Emails {
			asked++
		} else {
			plain++
		}
	}
	if asked != 2 || plain != 12 {
		t.Errorf("audited ?emails=1 calls: %d, other: %d", asked, plain)
	}
}

func TestAdminRevokeInvite(t *testing.T) {
	d := newAdminDB(t)
	ctx := context.Background()
	tok := d.token(d.op, nil)
	create := func(body string) model.BetaInvite {
		a := do(d.srv, "POST", "/v1/admin/invites", tok, body)
		if a.status != 201 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		return decodeInvite(t, a)
	}
	inv := create(`{"note": "Ben", "email": "ben@example.com"}`)
	dashless := strings.ReplaceAll(inv.Code, "-", "")

	// Revoke, with the code as shown, without dashes and in lower case; again and again.
	for _, c := range []string{inv.Code, dashless, strings.ToLower(inv.Code), inv.Code[5:]} {
		for _, bearer := range []string{tok, testAdminKey} {
			if a := do(d.srv, "DELETE", "/v1/admin/invites/"+c, bearer, ""); a.status != 204 || a.body != "" {
				t.Errorf("revoke %q: %d %s", c, a.status, a.body)
			}
		}
	}
	for _, c := range []string{"RYFT-ZZZZ-ZZZZ", "garbage", "RYFT-0O1I-LLLL"} {
		a := do(d.srv, "DELETE", "/v1/admin/invites/"+c, tok, "")
		if a.status != 404 || !strings.Contains(a.body, `"error":"no such code"`) || a.code() != "" {
			t.Errorf("unknown %q: %d %s", c, a.status, a.body)
		}
	}
	list := decodeInvites(t, do(d.srv, "GET", "/v1/admin/invites?emails=1", tok, ""))
	got, ok := findInvite(list, inv.Code)
	if !ok || got.Status != model.InviteRevoked || got.RevokedAt == nil || got.ExpiresAt == nil || got.ExpiresAt.After(time.Now()) {
		t.Fatalf("revoked code stays listed: %+v", got)
	}
	first := *got.RevokedAt
	do(d.srv, "DELETE", "/v1/admin/invites/"+inv.Code, tok, "")
	list = decodeInvites(t, do(d.srv, "GET", "/v1/admin/invites?emails=1", tok, ""))
	if got, _ = findInvite(list, inv.Code); !got.RevokedAt.Equal(first) {
		t.Errorf("the first revoke time is kept: %v then %v", first, got.RevokedAt)
	}
	// Nobody redeems it any more (the bound address matches, and still 422).
	u, _ := d.st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if _, err := d.st.RedeemInvite(ctx, u.ID, inv.Code, "ben@example.com"); !errors.Is(err, store.ErrInvalidCode) {
		t.Fatalf("redeem a revoked code: %v", err)
	}
	// Accounts that used a code keep their access when it is revoked.
	used := create(`{"note": "used", "max_uses": 3}`)
	if _, err := d.st.RedeemInvite(ctx, u.ID, used.Code, ""); err != nil {
		t.Fatal(err)
	}
	if a := do(d.srv, "DELETE", "/v1/admin/invites/"+used.Code, tok, ""); a.status != 204 {
		t.Fatal(a.status)
	}
	if g, _, _ := d.st.GetUser(ctx, u.ID); !g.BetaAccess {
		t.Error("revoking keeps the access it gave")
	}
	// The audit names the code and the actor.
	revokes := 0
	for _, rec := range d.audit() {
		if rec.Action == "DELETE /v1/admin/invites/{code}" && rec.Status == 204 {
			var det struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(rec.Details, &det) != nil || !strings.HasPrefix(det.Code, "RYFT-") || len(det.Code) != 14 {
				t.Errorf("audit details: %s", rec.Details)
			}
			revokes++
		}
	}
	if revokes != 10 { // 4 spellings x 2 bearers + the repeat + the used code
		t.Errorf("revoke audit rows: %d", revokes)
	}
	// The 404s are audited without a code.
	missing := 0
	for _, rec := range d.audit() {
		if rec.Action == "DELETE /v1/admin/invites/{code}" && rec.Status == 404 {
			missing++
			if strings.Contains(string(rec.Details), "RYFT") {
				t.Errorf("a code in the audit of a 404: %s", rec.Details)
			}
		}
	}
	if missing != 3 {
		t.Errorf("404 audit rows: %d", missing)
	}
}

// The whole path the page walks: a token creates a bound code, the tester redeems it with the confirmed
// address, the list shows who, a second tester is refused, the operator revokes.
func TestInviteEndToEnd(t *testing.T) {
	d := newAdminDB(t)
	ctx := context.Background()
	fake := newFakeEmails()
	h := &handler{st: d.st, log: quietHandler().log, cache: newInsightCache(), emails: fake, binding: fixedBinding(bindOK)}
	tok := d.token(d.op, nil)

	inv := decodeInvite(t, do(d.srv, "POST", "/v1/admin/invites", tok, `{"note": "Anna", "email": "anna@example.com"}`))
	anna, err := d.st.EnsureUser(ctx, store.Identity{ID: uuid.New(), Email: "anna.old@example.com"}) // users.email is a stale copy
	if err != nil {
		t.Fatal(err)
	}
	fake.set(anna.ID, "anna@example.com", nil)
	if rec := call(t, h.redeemInvite, anna, "POST", "/v1/me/invite", `{"code": "`+strings.ToLower(inv.Code)+`"}`); rec.Code != 204 {
		t.Fatalf("redeem: %d %s", rec.Code, body(rec))
	}
	// The users.email copy is not what is compared: an account whose copy matches but whose confirmed email does not is refused.
	inv2 := decodeInvite(t, do(d.srv, "POST", "/v1/admin/invites", tok, `{"email": "carl@example.com"}`))
	carl, _ := d.st.EnsureUser(ctx, store.Identity{ID: uuid.New(), Email: "carl@example.com"})
	fake.set(carl.ID, "someone.else@example.com", nil)
	if rec := call(t, h.redeemInvite, carl, "POST", "/v1/me/invite", `{"code": "`+inv2.Code+`"}`); rec.Code != 422 {
		t.Fatalf("a matching users.email copy is not proof: %d", rec.Code)
	}

	list := decodeInvites(t, do(d.srv, "GET", "/v1/admin/invites?emails=1", tok, ""))
	got, _ := findInvite(list, inv.Code)
	if got.Status != model.InviteUsed || got.Uses != 1 || len(got.UsesBy) != 1 || got.UsesBy[0].UserID != anna.ID ||
		*got.UsesBy[0].Email != "anna.old@example.com" {
		t.Fatalf("who redeemed: %+v", got)
	}
	if a := do(d.srv, "DELETE", "/v1/admin/invites/"+inv.Code, tok, ""); a.status != 204 {
		t.Fatal(a.status)
	}
	list = decodeInvites(t, do(d.srv, "GET", "/v1/admin/invites?emails=1", tok, ""))
	if got, _ = findInvite(list, inv.Code); got.Status != model.InviteRevoked || len(got.UsesBy) != 1 {
		t.Errorf("revoked after use: %+v", got)
	}
}

// The purge ticker clears a stale bound email and stops with its context.
func TestPurgeInviteEmailsTicker(t *testing.T) {
	r := newRedeemer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stale := r.bound("old@example.com")
	fresh := r.bound("new@example.com")
	pool := r.pool
	if _, err := pool.Exec(ctx, `UPDATE beta_invite_codes SET revoked_at = now() - interval '31 days', expires_at = now() - interval '31 days'
		WHERE code = $1`, store.NormalizeInvite(stale)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { purgeInviteEmailsEvery(ctx, r.st, quietHandler().log, 20*time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM beta_invite_codes WHERE email IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stale email was not cleared (%d codes keep one)", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var kept *string
	if err := pool.QueryRow(ctx, `SELECT email FROM beta_invite_codes WHERE code = $1`, store.NormalizeInvite(fresh)).Scan(&kept); err != nil || kept == nil {
		t.Fatalf("an open code keeps its email: %v %v", kept, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge loop did not stop with its context")
	}
}

// The redeem path logs no address and no code: a lookup failure and a success are lines without either.
func TestRedeemLogsHoldNoEmailsOrCodes(t *testing.T) {
	r := newRedeemer(t)
	buf := &bytes.Buffer{}
	r.h.log = logx.NewWriter(buf, "api", slog.LevelDebug)
	code := r.bound("anna@example.com")
	u := r.user()
	r.emails.set(u.ID, "anna@example.com", errors.New("supabase admin: get user: status 500"))
	if rec := r.redeem(u, code); rec.Code != 503 {
		t.Fatal(rec.Code)
	}
	r.emails.set(u.ID, "anna@example.com", nil)
	if rec := r.redeem(u, code); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"invite email lookup failed"`) || !strings.Contains(out, `"msg":"invite redeemed"`) {
		t.Fatalf("expected lines missing:\n%s", out)
	}
	for _, secret := range []string{"anna@example.com", code, strings.ReplaceAll(code, "-", "")} {
		if strings.Contains(out, secret) {
			t.Errorf("%q in the logs:\n%s", secret, out)
		}
	}
}
