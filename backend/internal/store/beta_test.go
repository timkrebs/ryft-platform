package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConsentHistory(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	u, _ := st.EnsureUser(ctx, Identity{ID: id})
	if u.Consent != nil || u.BetaAccess {
		t.Fatalf("a new account has no consent and no beta access (only accounts from before 0017 got it): %v %v", u.Consent, u.BetaAccess)
	}
	app := "0.9.0 (41)"
	if err := st.GrantConsent(ctx, id, "2026-10-20", &app); err != nil {
		t.Fatal(err)
	}
	g, _, _ := st.GetUser(ctx, id)
	if g.Consent == nil || g.Consent.PolicyVersion != "2026-10-20" {
		t.Fatalf("granted: %+v", g.Consent)
	}
	first := g.Consent.GrantedAt
	_ = st.GrantConsent(ctx, id, "2026-10-20", nil)
	if g, _, _ = st.GetUser(ctx, id); !g.Consent.GrantedAt.Equal(first) {
		t.Fatal("the same version again keeps the first grant")
	}
	_ = st.GrantConsent(ctx, id, "2026-12-01", nil)
	if g, _, _ = st.GetUser(ctx, id); g.Consent.PolicyVersion != "2026-12-01" {
		t.Fatalf("a new policy version is the current one: %+v", g.Consent)
	}
	if err := st.RevokeConsent(ctx, id); err != nil || st.RevokeConsent(ctx, id) != nil {
		t.Fatal("revoke, twice")
	}
	if g, _, _ = st.GetUser(ctx, id); g.Consent != nil {
		t.Fatalf("revoked: %+v", g.Consent)
	}
	var rows, revoked int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(revoked_at) FROM consents WHERE user_id=$1`, id).Scan(&rows, &revoked); err != nil || rows != 2 || revoked != 2 {
		t.Fatalf("the history stays: %d rows, %d revoked (%v)", rows, revoked, err)
	}
	if err := st.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consents WHERE user_id=$1`, id).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("deleted with the account")
	}
}

func TestInviteCodes(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	inv, err := st.CreateInvite(ctx, "Anna", 2, nil)
	if err != nil || len(inv.Code) != 14 || !strings.HasPrefix(inv.Code, "RYFT-") || inv.Uses != 0 || inv.MaxUses != 2 {
		t.Fatalf("create: %+v %v", inv, err)
	}
	for _, typed := range []string{inv.Code, strings.ToLower(inv.Code), strings.ReplaceAll(inv.Code, "-", " "), inv.Code[5:]} {
		if NormalizeInvite(typed) != strings.ReplaceAll(inv.Code, "-", "") {
			t.Errorf("%q does not normalise", typed)
		}
	}
	if NormalizeInvite("RYFT-0O1I-LLLL") != "" || NormalizeInvite("short") != "" {
		t.Fatal("ambiguous or short codes are not codes")
	}
	newUser := func() uuid.UUID {
		id := uuid.New()
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE users SET beta_access_at = NULL WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := newUser(), newUser(), newUser()
	if used, err := st.RedeemInvite(ctx, a, strings.ToLower(inv.Code)); err != nil || !used {
		t.Fatalf("a: %v %v", used, err)
	}
	if used, err := st.RedeemInvite(ctx, a, "RYFT-2222-2222"); err != nil || used {
		t.Fatalf("an account with access spends nothing, whatever it sends: %v %v", used, err)
	}
	if _, err := st.RedeemInvite(ctx, b, inv.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemInvite(ctx, c, inv.Code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("used up: %v", err)
	}
	if ga, _, _ := st.GetUser(ctx, a); !ga.BetaAccess {
		t.Fatal("a has access")
	}
	past := time.Now().Add(-time.Hour)
	expired, _ := st.CreateInvite(ctx, "old", 5, &past)
	if _, err := st.RedeemInvite(ctx, c, expired.Code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := st.RedeemInvite(ctx, c, "RYFT-ZZZZ-ZZZZ"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("unknown: %v", err)
	}
	list, _ := st.Invites(ctx)
	if len(list) != 2 || list[1].Uses != 2 {
		t.Fatalf("list: %+v", list)
	}
	if err := st.DeleteInvite(ctx, inv.Code); err != nil || !errors.Is(st.DeleteInvite(ctx, inv.Code), ErrNotFound) {
		t.Fatal("delete, then unknown")
	}
	if ga, _, _ := st.GetUser(ctx, a); !ga.BetaAccess {
		t.Fatal("deleting the code keeps the access it gave")
	}
}
