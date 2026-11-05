package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Invites bound to an email, revoke and who redeemed (docs/work/2026-11-05-admin-web/design.md §6;
// migration 0033). These run on a real Postgres: the guarantees are those of the one statement that spends
// a use, the row lock and the cascades.

func TestFoldEmail(t *testing.T) {
	for in, want := range map[string]string{
		"Anna@Example.COM":    "anna@example.com",
		"  anna@example.com":  "  anna@example.com",  // nothing but ASCII letters changes: a space is not trimmed away
		"a.b+tag@example.com": "a.b+tag@example.com", // no dot or plus-address folding
		"\u212a@example.com":  "\u212a@example.com",  // the Kelvin sign is not an ASCII k
		"":                    "",
	} {
		if got := FoldEmail(in); got != want {
			t.Errorf("FoldEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseInviteEmail(t *testing.T) {
	for in, want := range map[string]string{
		"anna@example.com":                  "anna@example.com",
		"  Anna@Example.com\n":              "anna@example.com",
		"a.b+c@sub.example.co.uk":           "a.b+c@sub.example.co.uk",
		"x7k2m9q4@privaterelay.appleid.com": "x7k2m9q4@privaterelay.appleid.com",
	} {
		if got, ok := ParseInviteEmail(in); !ok || got != want {
			t.Errorf("ParseInviteEmail(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	long := make([]byte, 250)
	for i := range long {
		long[i] = 'a'
	}
	for _, bad := range []string{
		"", " ", "anna", "anna@", "@example.com", "anna@example", "anna@@example.com", "a@b@example.com", "anna@example.",
		"anna@.example.com", "anna@example..com", "an na@example.com", "anna@exa mple.com", "anna\t@example.com",
		"änna@example.com", "anna@exämple.com", "\u212a@example.com", "anna@example.com\x7f", "anna@example.com\x00",
		string(long) + "@example.com", // 262 characters
	} {
		if got, ok := ParseInviteEmail(bad); ok {
			t.Errorf("ParseInviteEmail(%q) = %q, want it refused", bad, got)
		}
	}
	if _, ok := ParseInviteEmail(string(long[:240]) + "@e.co"); !ok { // 245 characters
		t.Error("a long address within 254 characters is fine")
	}
}

// inviteHarness creates accounts without beta access (every new account starts so) and codes.
type inviteHarness struct {
	t    *testing.T
	st   *Store
	pool *pgxpool.Pool
}

func newInviteHarness(t *testing.T) inviteHarness {
	st, pool := testStore(t)
	return inviteHarness{t, st, pool}
}

func (h inviteHarness) user() uuid.UUID {
	h.t.Helper()
	id := uuid.New()
	if _, err := h.st.EnsureUser(context.Background(), Identity{ID: id, Email: fmt.Sprintf("%s@example.test", id.String()[:8])}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.pool.Exec(context.Background(), `UPDATE users SET beta_access_at = NULL WHERE id=$1`, id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h inviteHarness) code(in NewInvite) string {
	h.t.Helper()
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.CreatedBy == "" {
		in.CreatedBy = "key"
	}
	if in.Email != nil && in.ExpiresAt == nil { // a bound code expires (CHECK beta_invite_codes_bound_expires)
		in.ExpiresAt = ptr(time.Now().Add(14 * 24 * time.Hour))
	}
	inv, err := h.st.CreateInvite(context.Background(), in)
	if err != nil {
		h.t.Fatal(err)
	}
	return inv.Code
}

func (h inviteHarness) bound(email string) string {
	return h.code(NewInvite{Email: &email})
}

func (h inviteHarness) uses(code string) (uses, rows int) {
	h.t.Helper()
	n := NormalizeInvite(code)
	if err := h.pool.QueryRow(context.Background(), `SELECT uses, (SELECT count(*) FROM beta_invite_uses WHERE code = $1)
		FROM beta_invite_codes WHERE code = $1`, n).Scan(&uses, &rows); err != nil {
		h.t.Fatal(err)
	}
	return uses, rows
}

func (h inviteHarness) hasAccess(id uuid.UUID) bool {
	h.t.Helper()
	u, _, err := h.st.GetUser(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return u.BetaAccess
}

func (h inviteHarness) redeem(id uuid.UUID, code, verified string) (bool, error) {
	return h.st.RedeemInvite(context.Background(), id, code, verified)
}

// A bound code is spent only for the account whose Supabase-confirmed email it was made for; every other
// case is the one ErrInvalidCode and spends nothing.
func TestBoundInviteRedemption(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	code := h.bound("anna@example.com")

	// BoundInviteEmail says what the handler needs before it asks Supabase.
	if e, ok, err := h.st.BoundInviteEmail(ctx, code); err != nil || !ok || e != "anna@example.com" {
		t.Fatalf("a bound open code: %q %v %v", e, ok, err)
	}
	if _, ok, _ := h.st.BoundInviteEmail(ctx, h.code(NewInvite{MaxUses: 3})); ok {
		t.Fatal("an unbound code is not bound")
	}
	for _, c := range []string{"RYFT-ZZZZ-ZZZZ", "nonsense", ""} {
		if _, ok, err := h.st.BoundInviteEmail(ctx, c); err != nil || ok {
			t.Fatalf("%q (unknown): bound %v, %v", c, ok, err)
		}
	}

	refused := []struct{ name, verified string }{
		{"no email at Supabase, or unconfirmed (the handler passes nothing)", ""},
		{"another address", "ben@example.com"},
		{"a plus address of it", "anna+x@example.com"},
		{"a dotted variant", "an.na@example.com"},
		{"a sub-address of the domain", "anna@mail.example.com"},
		{"the address with a suffix", "anna@example.com.evil"},
		{"a relay address", "x7k2m9q4@privaterelay.appleid.com"},
		{"an address with a space", "anna@example.com x"},
		{"the address with a trailing space (not trimmed into a match)", "anna@example.com "},
		{"the address with a leading space", " anna@example.com"},
		{"the address with a newline", "anna@example.com\n"},
		{"the address with a non-ASCII byte", "anna@example.com\u00a0"},
	}
	for _, c := range refused {
		id := h.user()
		if used, err := h.redeem(id, code, c.verified); !errors.Is(err, ErrInvalidCode) || used {
			t.Errorf("%s: %v %v, want ErrInvalidCode", c.name, used, err)
		}
		if h.hasAccess(id) {
			t.Errorf("%s: access granted", c.name)
		}
	}
	if uses, rows := h.uses(code); uses != 0 || rows != 0 {
		t.Fatalf("a refusal spends nothing: uses %d, rows %d", uses, rows)
	}

	// The address matches however its ASCII letters are cased, nothing else.
	anna := h.user()
	if used, err := h.redeem(anna, code, "Anna@EXAMPLE.com"); err != nil || !used {
		t.Fatalf("matching email: %v %v", used, err)
	}
	if !h.hasAccess(anna) {
		t.Fatal("access after the code")
	}
	if uses, rows := h.uses(code); uses != 1 || rows != 1 {
		t.Fatalf("one use, one row: %d %d", uses, rows)
	}
	var user uuid.UUID
	var viaCode string
	if err := h.pool.QueryRow(ctx, `SELECT user_id FROM beta_invite_uses WHERE code=$1`, NormalizeInvite(code)).Scan(&user); err != nil || user != anna {
		t.Fatalf("the use names the account: %v %v", user, err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT beta_invite_code FROM users WHERE id=$1`, anna).Scan(&viaCode); err != nil || viaCode != NormalizeInvite(code) {
		t.Fatalf("users.beta_invite_code: %q %v", viaCode, err)
	}
	// Used up now: the lookup no longer calls it bound, and the same email on another account is refused.
	if _, ok, _ := h.st.BoundInviteEmail(ctx, code); ok {
		t.Error("a used-up code reads like an unknown one")
	}
	if _, err := h.redeem(h.user(), code, "anna@example.com"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("replay on another account: %v", err)
	}
	// An account that has access gets no refusal and spends nothing, whatever it sends.
	if used, err := h.redeem(anna, "RYFT-ZZZZ-ZZZZ", ""); err != nil || used {
		t.Errorf("an account with access: %v %v", used, err)
	}
	if uses, _ := h.uses(code); uses != 1 {
		t.Errorf("uses %d", uses)
	}
}

// Whatever the reason, the refusal is the same error value, and no state differs but the one the reason
// names: this is what makes the 422 an answer without an oracle.
func TestInviteRefusalsAreOneError(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	past, soon := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	expired := h.code(NewInvite{ExpiresAt: &soon})
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = $2 WHERE code = $1`, NormalizeInvite(expired), past); err != nil {
		t.Fatal(err)
	}
	revoked := h.code(NewInvite{})
	if err := h.st.RevokeInvite(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	used := h.code(NewInvite{})
	if _, err := h.redeem(h.user(), used, ""); err != nil {
		t.Fatal(err)
	}
	bound := h.bound("anna@example.com")
	// Replay by the same account: access taken away by the operator, then the same code again.
	reuse := h.code(NewInvite{MaxUses: 5})
	again := h.user()
	if _, err := h.redeem(again, reuse, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetBetaAccess(ctx, again, false); err != nil {
		t.Fatal(err)
	}

	var errs []error
	for _, c := range []struct{ name, code, verified string }{
		{"unknown", "RYFT-ZZZZ-ZZZZ", ""}, {"malformed", "no code", ""}, {"empty", "", ""}, {"expired", expired, ""},
		{"revoked", revoked, ""}, {"used up", used, ""}, {"bound to another email", bound, "ben@example.com"},
		{"bound, no confirmed email", bound, ""}, {"redeemed by this account before", reuse, ""},
	} {
		id := again
		if c.name != "redeemed by this account before" {
			id = h.user()
		}
		_, err := h.redeem(id, c.code, c.verified)
		if !errors.Is(err, ErrInvalidCode) {
			t.Errorf("%s: %v", c.name, err)
		}
		errs = append(errs, err)
	}
	for _, e := range errs {
		if e.Error() != ErrInvalidCode.Error() {
			t.Errorf("the refusal is the sentinel itself, not a wrapped variant: %v", e)
		}
	}
	if uses, _ := h.uses(reuse); uses != 1 {
		t.Errorf("a replay by the same account spends nothing: uses %d", uses)
	}
	if h.hasAccess(again) {
		t.Error("the operator's revocation of access holds against a replay")
	}
}

// Two accounts on the last use of a code: exactly one gets it, whatever the interleaving. The
// iterations run against a real Postgres and under -race.
func TestInviteLastUseGoesToOneAccount(t *testing.T) {
	h := newInviteHarness(t)
	for i := 0; i < 50; i++ {
		bound := i%2 == 1
		var code string
		if bound {
			code = h.bound("anna@example.com")
		} else {
			code = h.code(NewInvite{})
		}
		verified := ""
		if bound {
			verified = "anna@example.com" // both accounts claim the confirmed address (a mailbox moved between accounts)
		}
		a, b := h.user(), h.user()
		start := make(chan struct{})
		var wg sync.WaitGroup
		res := make([]error, 2)
		got := make([]bool, 2)
		for k, id := range []uuid.UUID{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got[k], res[k] = h.redeem(id, code, verified)
			}()
		}
		close(start)
		wg.Wait()
		wins, refusals := 0, 0
		for k := range res {
			switch {
			case res[k] == nil && got[k]:
				wins++
			case errors.Is(res[k], ErrInvalidCode):
				refusals++
			default:
				t.Fatalf("iteration %d account %d: %v %v", i, k, got[k], res[k])
			}
		}
		if wins != 1 || refusals != 1 {
			t.Fatalf("iteration %d (bound %v): %d winners, %d refused", i, bound, wins, refusals)
		}
		if uses, rows := h.uses(code); uses != 1 || rows != 1 {
			t.Fatalf("iteration %d: uses %d, rows %d", i, uses, rows)
		}
		if h.hasAccess(a) == h.hasAccess(b) {
			t.Fatalf("iteration %d: exactly one account has access", i)
		}
	}
}

// One account sending the same code twice at once spends one use.
func TestInviteSameAccountTwiceAtOnce(t *testing.T) {
	h := newInviteHarness(t)
	for i := 0; i < 20; i++ {
		code := h.code(NewInvite{MaxUses: 5})
		id := h.user()
		start := make(chan struct{})
		var wg sync.WaitGroup
		got := make([]bool, 2)
		errs := make([]error, 2)
		for k := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got[k], errs[k] = h.redeem(id, code, "")
			}()
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || got[0] == got[1] {
			t.Fatalf("iteration %d: one spends, the other finds access: %v %v %v", i, got, errs[0], errs[1])
		}
		if uses, rows := h.uses(code); uses != 1 || rows != 1 {
			t.Fatalf("iteration %d: uses %d rows %d", i, uses, rows)
		}
	}
}

// A revoke and a redeem on the same code never leave a use behind a revoked code that was revoked first. The
// ordering is forced by holding the revoke's row lock: the redeem waits on it, and after the commit it finds
// the code revoked.
func TestInviteRevokeBeforeRedeemWins(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	code := h.code(NewInvite{MaxUses: 3})
	id := h.user()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE beta_invite_codes SET revoked_at = now(), expires_at = now() WHERE code = $1`, NormalizeInvite(code)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := h.redeem(id, code, ""); done <- err }()
	waitForLockWait(t, h.pool)
	select {
	case err := <-done:
		t.Fatalf("the redeem must wait for the revoke's row lock, it returned %v", err)
	default:
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("redeem after the revoke: %v", err)
	}
	if uses, rows := h.uses(code); uses != 0 || rows != 0 || h.hasAccess(id) {
		t.Fatalf("nothing spent: uses %d, rows %d, access %v", uses, rows, h.hasAccess(id))
	}
}

// ... and a redeem that gets the row first keeps its use: the revoke does not take access away.
func TestInviteRedeemBeforeRevokeKeepsAccess(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	code := h.code(NewInvite{MaxUses: 3})
	id := h.user()

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM beta_invite_codes WHERE code = $1 FOR UPDATE`, NormalizeInvite(code)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { _, err := h.redeem(id, code, ""); done <- err }()
	waitForLockWait(t, h.pool)
	go func() { done <- h.st.RevokeInvite(ctx, code) }()
	time.Sleep(100 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for k := 0; k < 2; k++ {
		if err := <-done; err != nil && !errors.Is(err, ErrInvalidCode) {
			t.Fatal(err)
		}
	}
	// Whichever of the two got the lock first, the state is consistent: a use exists iff the account has access.
	uses, rows := h.uses(code)
	if (uses == 1) != h.hasAccess(id) || uses != rows {
		t.Fatalf("uses %d, rows %d, access %v", uses, rows, h.hasAccess(id))
	}
	var revoked bool
	if err := h.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(code)).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("revoked: %v %v", revoked, err)
	}
}

// Free-running revoke against redeem: whatever the interleaving, a use is never recorded without access, and
// after the revoke has committed nobody redeems any more.
func TestInviteRevokeRaceInvariants(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		code := h.code(NewInvite{})
		id := h.user()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var redeemErr, revokeErr error
		var used bool
		wg.Add(2)
		go func() { defer wg.Done(); <-start; used, redeemErr = h.redeem(id, code, "") }()
		go func() { defer wg.Done(); <-start; revokeErr = h.st.RevokeInvite(ctx, code) }()
		close(start)
		wg.Wait()
		if revokeErr != nil || (redeemErr != nil && !errors.Is(redeemErr, ErrInvalidCode)) {
			t.Fatalf("iteration %d: %v %v", i, revokeErr, redeemErr)
		}
		uses, rows := h.uses(code)
		if used != (uses == 1) || uses != rows || h.hasAccess(id) != used {
			t.Fatalf("iteration %d: used %v uses %d rows %d access %v", i, used, uses, rows, h.hasAccess(id))
		}
		if _, err := h.redeem(h.user(), code, ""); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("iteration %d: a revoked code redeems nothing: %v", i, err)
		}
	}
}

// waitForLockWait blocks until some backend waits for a row lock (the redeem or revoke under test).
func waitForLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no backend is waiting for a lock")
}

func TestRevokeInvite(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	later := time.Now().Add(14 * 24 * time.Hour)
	earlier := time.Now().Add(-3 * 24 * time.Hour)

	forever := h.code(NewInvite{})
	until := h.code(NewInvite{ExpiresAt: &later})
	lapsed := h.code(NewInvite{ExpiresAt: ptr(time.Now().Add(time.Hour))})
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = $2 WHERE code = $1`, NormalizeInvite(lapsed), earlier); err != nil {
		t.Fatal(err)
	}
	read := func(code string) (revoked, expires *time.Time) {
		t.Helper()
		if err := h.pool.QueryRow(ctx, `SELECT revoked_at, expires_at FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(code)).Scan(&revoked, &expires); err != nil {
			t.Fatal(err)
		}
		return
	}

	for _, typed := range []string{forever, forever[5:], "ryft " + forever[5:]} {
		if err := h.st.RevokeInvite(ctx, typed); err != nil {
			t.Fatalf("revoke %q: %v", typed, err)
		}
	}
	first, expires := read(forever)
	if first == nil || expires == nil || expires.After(time.Now()) || expires.Before(first.Add(-time.Second)) {
		t.Fatalf("a code without expiry is capped at the revoke time: %v %v", first, expires)
	}
	time.Sleep(20 * time.Millisecond)
	if err := h.st.RevokeInvite(ctx, forever); err != nil {
		t.Fatal(err)
	}
	if again, expiresAgain := read(forever); !again.Equal(*first) || !expiresAgain.Equal(*expires) {
		t.Fatalf("revoking again changes nothing: %v %v then %v %v", first, expires, again, expiresAgain)
	}

	if err := h.st.RevokeInvite(ctx, until); err != nil {
		t.Fatal(err)
	}
	if r, e := read(until); r == nil || e.After(time.Now()) {
		t.Fatalf("a later expiry is capped at the revoke time: %v %v", r, e)
	}
	if err := h.st.RevokeInvite(ctx, lapsed); err != nil {
		t.Fatal(err)
	}
	if r, e := read(lapsed); r == nil || !e.Equal(earlier.Truncate(time.Microsecond)) {
		t.Fatalf("an earlier expiry is kept: %v %v", r, e)
	}

	// The image before 0033 reads no revoked_at: its redemption statement must refuse every revoked code.
	for _, c := range []string{forever, until, lapsed} {
		tag, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET uses = uses + 1
			WHERE code=$1 AND uses < max_uses AND (expires_at IS NULL OR expires_at > now())`, NormalizeInvite(c))
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("the old image's redeem of a revoked code: %v %v", tag, err)
		}
	}

	for _, unknown := range []string{"RYFT-ZZZZ-ZZZZ", "garbage", ""} {
		if err := h.st.RevokeInvite(ctx, unknown); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke %q: %v, want ErrNotFound", unknown, err)
		}
	}
	// Revoke keeps the row and the redemptions: accounts that used the code keep their access.
	code := h.code(NewInvite{MaxUses: 2})
	id := h.user()
	if _, err := h.redeem(id, code, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RevokeInvite(ctx, code); err != nil {
		t.Fatal(err)
	}
	if uses, rows := h.uses(code); uses != 1 || rows != 1 || !h.hasAccess(id) {
		t.Fatalf("uses %d rows %d access %v", uses, rows, h.hasAccess(id))
	}
}

// Two revokes at once are two successes and one revoke time.
func TestRevokeInviteConcurrent(t *testing.T) {
	h := newInviteHarness(t)
	for i := 0; i < 20; i++ {
		code := h.code(NewInvite{})
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for k := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); errs[k] = h.st.RevokeInvite(context.Background(), code) }()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var n int
		if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM beta_invite_codes WHERE code=$1 AND revoked_at IS NOT NULL
			AND expires_at <= revoked_at + interval '1 second'`, NormalizeInvite(code)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("revoked once: %d %v", n, err)
		}
	}
}

func TestInvitesListWithUses(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	user := fmt.Sprintf("user:%s", uuid.New())
	key := h.code(NewInvite{Note: "wave", MaxUses: 5, CreatedBy: "key:3fa1c2d9"})
	bound := h.code(NewInvite{Note: "Anna", Email: ptr("anna@example.com"), CreatedBy: user})
	old := h.code(NewInvite{Note: "old"}) // created_by 'key': the fingerprint was not kept

	first, second, third := h.user(), h.user(), h.user()
	for _, id := range []uuid.UUID{first, second, third} {
		if _, err := h.redeem(id, key, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.redeem(h.user(), bound, "anna@example.com"); err != nil {
		t.Fatal(err)
	}
	// Distinct times, to pin "oldest first" independent of the order of the inserts.
	for k, id := range []uuid.UUID{third, first, second} {
		if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_uses SET at = now() - ($2 || ' hours')::interval WHERE user_id = $1`, id, fmt.Sprint(10-k)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(ctx, `UPDATE users SET email = NULL WHERE id = $1`, second); err != nil {
		t.Fatal(err)
	}

	list, err := h.st.Invites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Code != old || list[1].Code != bound || list[2].Code != key {
		t.Fatalf("newest first: %+v", list)
	}
	if list[0].CreatedBy.Kind != "key" || list[0].CreatedBy.ID != nil || list[0].Email != nil || list[0].RevokedAt != nil ||
		list[0].UsesBy == nil || len(list[0].UsesBy) != 0 {
		t.Errorf("a code from before 0033: %+v", list[0])
	}
	if got := list[1]; got.Email == nil || *got.Email != "anna@example.com" || got.CreatedBy.Kind != "user" ||
		got.CreatedBy.ID == nil || "user:"+*got.CreatedBy.ID != user || got.Uses != 1 || len(got.UsesBy) != 1 {
		t.Errorf("a bound code: %+v", got)
	}
	k := list[2]
	if k.CreatedBy.Kind != "key" || k.CreatedBy.ID == nil || *k.CreatedBy.ID != "3fa1c2d9" || len(k.UsesBy) != 3 {
		t.Fatalf("a key code with three uses: %+v", k)
	}
	if k.UsesBy[0].UserID != third || k.UsesBy[1].UserID != first || k.UsesBy[2].UserID != second {
		t.Errorf("uses_by is oldest first: %v", k.UsesBy)
	}
	for _, u := range k.UsesBy {
		if u.ShortID != u.UserID.String()[:8] || u.At.IsZero() || u.At.Location() != time.UTC {
			t.Errorf("use: %+v", u)
		}
	}
	if k.UsesBy[0].Email == nil || k.UsesBy[2].Email != nil {
		t.Errorf("the account's email, nil when it has none: %v %v", k.UsesBy[0].Email, k.UsesBy[2].Email)
	}

	// A deleted account's row goes with it; uses stays the count.
	if err := h.st.DeleteUser(ctx, second); err != nil {
		t.Fatal(err)
	}
	list, _ = h.st.Invites(ctx)
	if k = list[2]; k.Uses != 3 || len(k.UsesBy) != 2 {
		t.Errorf("uses is the count, uses_by lists accounts that still exist: %d %d", k.Uses, len(k.UsesBy))
	}
}

// The email of a prospective tester is not kept for good: 30 days after the code stopped being redeemable it
// is cleared, and not before; open codes and unbound codes are never touched.
func TestPurgeInviteEmails(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	const age = 30 * 24 * time.Hour
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }
	day := 24 * time.Hour
	set := func(code, sql string, args ...any) {
		t.Helper()
		if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET `+sql+` WHERE code = $1`, append([]any{NormalizeInvite(code)}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	mk := func() string { return h.bound(fmt.Sprintf("p%s@example.com", uuid.NewString()[:8])) }

	open := mk()
	revokedOld, revokedNew := mk(), mk()
	set(revokedOld, `revoked_at = $2, expires_at = $2`, ago(31*day))
	set(revokedNew, `revoked_at = $2, expires_at = $2`, ago(29*day))
	expiredOld, expiredNew := mk(), mk()
	set(expiredOld, `expires_at = $2`, ago(31*day))
	set(expiredNew, `expires_at = $2`, ago(29*day))
	usedOld, usedNew := mk(), mk()
	for _, c := range []string{usedOld, usedNew} {
		var e string
		if err := h.pool.QueryRow(ctx, `SELECT email FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(c)).Scan(&e); err != nil {
			t.Fatal(err)
		}
		if _, err := h.redeem(h.user(), c, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_uses SET at = $2 WHERE code = $1`, NormalizeInvite(usedOld), ago(31*day)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_uses SET at = $2 WHERE code = $1`, NormalizeInvite(usedNew), ago(5*day)); err != nil {
		t.Fatal(err)
	}
	// Used up by an account that is gone since: no use time is left, the creation time stands in.
	usedGone, usedGoneNew := mk(), mk()
	for _, c := range []string{usedGone, usedGoneNew} {
		set(c, `uses = 1`)
	}
	set(usedGone, `created_at = $2`, ago(40*day))
	unbound := h.code(NewInvite{})

	n, err := h.st.PurgeInviteEmails(ctx, age)
	if err != nil {
		t.Fatal(err)
	}
	cleared := map[string]bool{revokedOld: true, expiredOld: true, usedOld: true, usedGone: true}
	for name, c := range map[string]string{"open": open, "revoked 31 days ago": revokedOld, "revoked 29 days ago": revokedNew,
		"expired 31 days ago": expiredOld, "expired 29 days ago": expiredNew, "used up 31 days ago": usedOld, "used up 5 days ago": usedNew,
		"used up, account gone, 40 days old": usedGone, "used up, account gone, new": usedGoneNew} {
		var email *string
		if err := h.pool.QueryRow(ctx, `SELECT email FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(c)).Scan(&email); err != nil {
			t.Fatal(err)
		}
		if got := email == nil; got != cleared[c] {
			t.Errorf("%s: email cleared %v, want %v", name, got, cleared[c])
		}
	}
	if n != int64(len(cleared)) {
		t.Errorf("rows cleared: %d, want %d", n, len(cleared))
	}
	if n, err := h.st.PurgeInviteEmails(ctx, age); err != nil || n != 0 {
		t.Errorf("a second pass clears nothing: %d %v", n, err)
	}
	// A cleared code is what it was, minus the binding: it cannot be redeemed, so nothing was widened.
	if _, err := h.redeem(h.user(), revokedOld, ""); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("a cleared revoked code: %v", err)
	}
	if _, err := h.redeem(h.user(), unbound, ""); err != nil {
		t.Errorf("an unbound code is untouched: %v", err)
	}
}

// DeleteUser removes the account's redemptions with it; the code keeps its count. (owned() in
// delete_test.go counts the table too.)
func TestDeleteUserRemovesRedemptions(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	code := h.code(NewInvite{MaxUses: 2})
	id, other := h.user(), h.user()
	for _, u := range []uuid.UUID{id, other} {
		if _, err := h.redeem(u, code, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if uses, rows := h.uses(code); uses != 2 || rows != 1 {
		t.Fatalf("uses stays 2, one row left: %d %d", uses, rows)
	}
	var n int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM beta_invite_uses WHERE user_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows of the deleted account: %d %v", n, err)
	}
	// Deleting the code takes its redemptions with it, and the other account keeps its access.
	if _, err := h.pool.Exec(ctx, `DELETE FROM beta_invite_codes WHERE code = $1`, NormalizeInvite(code)); err != nil {
		t.Fatal(err)
	}
	if !h.hasAccess(other) {
		t.Fatal("access stays")
	}
}

// The merge moves the redemptions of from to to; the same code on both is one row, to's.
func TestMergeMovesRedemptions(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	a, b, shared := h.code(NewInvite{MaxUses: 3}), h.code(NewInvite{MaxUses: 3}), h.code(NewInvite{MaxUses: 3})
	from, to := h.user(), h.user()
	if _, err := h.pool.Exec(ctx, `UPDATE users SET beta_access_at = NULL WHERE id IN ($1, $2)`, from, to); err != nil {
		t.Fatal(err)
	}
	add := func(id uuid.UUID, codes ...string) {
		for _, c := range codes {
			if _, err := h.pool.Exec(ctx, `INSERT INTO beta_invite_uses (code, user_id) VALUES ($1, $2)`, NormalizeInvite(c), id); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(from, a, shared)
	add(to, b, shared)

	plan, err := h.st.MergeUsers(ctx, from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	var got *MergeTable
	for k := range plan.Tables {
		if plan.Tables[k].Table == "beta_invite_uses" {
			got = &plan.Tables[k]
		}
	}
	if got == nil || got.Moved != 1 || got.Dropped != 1 {
		t.Fatalf("the plan counts one moved, one dropped: %+v", got)
	}
	if n := h.countUses(to); n != 2 {
		t.Fatalf("a dry run keeps nothing: to has %d", n)
	}
	if _, err := h.st.MergeUsers(ctx, from, to, false); err != nil {
		t.Fatal(err)
	}
	if h.countUses(to) != 3 || h.countUses(from) != 1 {
		t.Fatalf("to has a, b and shared; from keeps its duplicate for the deletion: to %d from %d", h.countUses(to), h.countUses(from))
	}
	if err := h.st.DeleteUser(ctx, from); err != nil {
		t.Fatal(err)
	}
	if h.countUses(from) != 0 || h.countUses(to) != 3 {
		t.Fatalf("after deleting from: to %d from %d", h.countUses(to), h.countUses(from))
	}
}

func (h inviteHarness) countUses(id uuid.UUID) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM beta_invite_uses WHERE user_id=$1`, id).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// A failed redemption rolls everything back: the statement and the access update are one transaction, so a
// failure of the second leaves neither a spent use nor a row.
func TestRedeemRollsBackWhenTheAccessUpdateFails(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	code := h.code(NewInvite{})
	id := h.user()
	// A trigger that refuses the access update, standing in for any failure after the use was spent.
	for _, q := range []string{
		`CREATE FUNCTION refuse_access() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`,
		`CREATE TRIGGER refuse_access BEFORE UPDATE OF beta_access_at ON users FOR EACH ROW EXECUTE FUNCTION refuse_access()`,
	} {
		if _, err := h.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.redeem(id, code, ""); err == nil || errors.Is(err, ErrInvalidCode) {
		t.Fatalf("want the trigger's error: %v", err)
	}
	if uses, rows := h.uses(code); uses != 0 || rows != 0 {
		t.Fatalf("nothing spent after the rollback: uses %d rows %d", uses, rows)
	}
	if _, err := h.pool.Exec(ctx, `DROP TRIGGER refuse_access ON users`); err != nil {
		t.Fatal(err)
	}
	if used, err := h.redeem(id, code, ""); err != nil || !used {
		t.Fatalf("the code is still good: %v %v", used, err)
	}
}

// The purge also clears the note of a bound code (it may name the person) and sets revoked_at on a code that is
// not used up, so a purged code stays dead if expires_at is edited later; a used-up code keeps its status; the
// note of an unbound code is the operator's label and stays.
func TestPurgeInviteEmailsClearsNoteAndKeepsTheCodeDead(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	old := time.Now().Add(-31 * 24 * time.Hour)
	expired := h.code(NewInvite{Note: "Anna from the gym", Email: ptr("anna@example.com")})
	used := h.code(NewInvite{Note: "Ben", Email: ptr("ben@example.com")})
	unbound := h.code(NewInvite{Note: "wave 2", MaxUses: 3, ExpiresAt: ptr(time.Now().Add(time.Hour))})
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = $2 WHERE code IN ($1, $3)`, NormalizeInvite(expired), old, NormalizeInvite(unbound)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.redeem(h.user(), used, "ben@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_uses SET at = $2 WHERE code = $1`, NormalizeInvite(used), old); err != nil {
		t.Fatal(err)
	}
	if n, err := h.st.PurgeInviteEmails(ctx, 30*24*time.Hour); err != nil || n != 2 {
		t.Fatalf("purge: %d %v", n, err)
	}
	row := func(code string) (email *string, note string, revoked *time.Time) {
		t.Helper()
		if err := h.pool.QueryRow(ctx, `SELECT email, note, revoked_at FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(code)).Scan(&email, &note, &revoked); err != nil {
			t.Fatal(err)
		}
		return
	}
	if e, n, r := row(expired); e != nil || n != "" || r == nil {
		t.Errorf("expired bound code: email %v note %q revoked %v", e, n, r)
	}
	if e, n, r := row(used); e != nil || n != "" || r != nil {
		t.Errorf("used-up bound code keeps its status: email %v note %q revoked %v", e, n, r)
	}
	if _, n, r := row(unbound); n != "wave 2" || r != nil {
		t.Errorf("an unbound code is untouched: %q %v", n, r)
	}
	// Someone edits expires_at in SQL: the purged code stays dead.
	if _, err := h.pool.Exec(ctx, `UPDATE beta_invite_codes SET expires_at = now() + interval '30 days' WHERE code = $1`, NormalizeInvite(expired)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.redeem(h.user(), expired, ""); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("a purged code redeems nothing: %v", err)
	}
	list, _ := h.st.Invites(ctx)
	for _, i := range list {
		if i.Code == used && i.RevokedAt != nil {
			t.Errorf("used-up code reads revoked: %+v", i)
		}
	}
}

// DeleteUser clears the bound email and the note of a code the account redeemed (they identify the person),
// together with the redemption row; another account's bound code is untouched.
func TestDeleteUserClearsTheBoundEmailOfItsCode(t *testing.T) {
	h := newInviteHarness(t)
	ctx := context.Background()
	mine := h.code(NewInvite{Note: "Anna", Email: ptr("anna@example.com")})
	theirs := h.code(NewInvite{Note: "Ben", Email: ptr("ben@example.com")})
	pending := h.code(NewInvite{Note: "Carl", Email: ptr("carl@example.com")})
	anna := h.user()
	if _, err := h.redeem(anna, mine, "anna@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.redeem(h.user(), theirs, "ben@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.DeleteUser(ctx, anna); err != nil {
		t.Fatal(err)
	}
	read := func(code string) (email *string, note string) {
		t.Helper()
		if err := h.pool.QueryRow(ctx, `SELECT email, note FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(code)).Scan(&email, &note); err != nil {
			t.Fatal(err)
		}
		return
	}
	if e, n := read(mine); e != nil || n != "" {
		t.Errorf("the deleted account's code: email %v note %q", e, n)
	}
	if e, n := read(theirs); e == nil || n != "Ben" {
		t.Errorf("another account's code is untouched: %v %q", e, n)
	}
	if e, n := read(pending); e == nil || n != "Carl" {
		t.Errorf("an unredeemed code is untouched: %v %q", e, n)
	}
	if uses, rows := h.uses(mine); uses != 1 || rows != 0 {
		t.Errorf("uses stays the count, the row is gone: %d %d", uses, rows)
	}
}
