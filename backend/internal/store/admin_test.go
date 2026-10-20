package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestResolveUserGrantAndAudit(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000001")
	twin := uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000002") // same first 8 digits
	solo := uuid.New()
	for _, id := range []uuid.UUID{a, twin, solo} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := st.ResolveUser(ctx, a.String()); err != nil || got != a {
		t.Fatalf("full id: %v %v", got, err)
	}
	if got, err := st.ResolveUser(ctx, strings.ToUpper(solo.String()[:8])); err != nil || got != solo {
		t.Fatalf("a unique short id, any case: %v %v", got, err)
	}
	if _, err := st.ResolveUser(ctx, "a1b2c3d4"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("a shared short id: %v", err)
	}
	for _, bad := range []string{uuid.NewString(), "ffffffff", "nope", "a1b2c3d4'--"} {
		if _, err := st.ResolveUser(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	if err := st.SetBetaAccess(ctx, solo, true); err != nil {
		t.Fatal(err)
	}
	var at1, at2 time.Time
	_ = pool.QueryRow(ctx, `SELECT beta_access_at FROM users WHERE id=$1`, solo).Scan(&at1)
	if err := st.SetBetaAccess(ctx, solo, true); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT beta_access_at FROM users WHERE id=$1`, solo).Scan(&at2)
	if at1.IsZero() || !at1.Equal(at2) {
		t.Fatalf("a second grant keeps the first time: %v %v", at1, at2)
	}
	if u, _, _ := st.GetUser(ctx, solo); !u.BetaAccess {
		t.Fatal("granted")
	}
	if err := st.SetBetaAccess(ctx, solo, false); err != nil {
		t.Fatal(err)
	}
	if u, _, _ := st.GetUser(ctx, solo); u.BetaAccess {
		t.Fatal("revoked")
	}
	if err := st.SetBetaAccess(ctx, uuid.New(), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}

	start := time.Now().Add(-time.Second)
	if err := st.Audit(ctx, AuditEntry{KeyFingerprint: "abcd1234", Action: "POST /v1/admin/users/{id}/access",
		TargetUser: &solo, Status: 204, RequestID: "01a1-test", Details: map[string]any{"grant": true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(ctx, AuditEntry{KeyFingerprint: "abcd1234", Action: "GET /v1/admin/users", Status: 200}); err != nil {
		t.Fatal(err)
	}
	list, err := st.AuditSince(ctx, start, 10)
	if err != nil || len(list) < 2 {
		t.Fatalf("audit: %v %v", list, err)
	}
	var details map[string]any
	_ = json.Unmarshal(list[1].Details, &details)
	if list[0].Action != "GET /v1/admin/users" || list[0].TargetUser != nil || list[1].TargetUser == nil ||
		*list[1].TargetUser != solo || list[1].Status != 204 || list[1].RequestID != "01a1-test" || details["grant"] != true {
		t.Fatalf("newest first, with target and details: %+v", list[:2])
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit`).Scan(&n); err != nil || n < 2 {
		t.Fatalf("rows: %d %v", n, err)
	}
}
