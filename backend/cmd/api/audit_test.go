package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
)

// The access grant through the real admin chain: key, audit, routes (D88).
func TestAdminAccessGrantIsAudited(t *testing.T) {
	h, u, st := journalHandler(t)
	ctx := context.Background()
	key := "test-admin-key-0123456789abcdef0123456789"
	srv := httpx.Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), adminRoutes(h, key, 5*time.Second))
	do := func(method, path, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}
	start := time.Now().Add(-time.Second)
	if rec := do(http.MethodPost, "/v1/admin/users/"+u.ID.String()[:8]+"/access", "wrong"); rec.Code != 401 {
		t.Fatalf("without the key: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/v1/admin/users/"+u.ID.String()[:8]+"/access", key); rec.Code != 204 {
		t.Fatalf("grant by short id: %d %s", rec.Code, rec.Body)
	}
	if g, _, _ := st.GetUser(ctx, u.ID); !g.BetaAccess {
		t.Fatal("access granted")
	}
	if rec := do(http.MethodDelete, "/v1/admin/users/"+u.ID.String()+"/access", key); rec.Code != 204 {
		t.Fatalf("revoke by full id: %d", rec.Code)
	}
	if g, _, _ := st.GetUser(ctx, u.ID); g.BetaAccess {
		t.Fatal("access revoked")
	}
	if rec := do(http.MethodPost, "/v1/admin/users/ffffffff/access", key); rec.Code != 404 {
		t.Fatalf("unknown user: %d", rec.Code)
	}

	rec := do(http.MethodGet, "/v1/admin/audit?since="+start.UTC().Format(time.RFC3339), key)
	var audit []model.AuditRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &audit); err != nil || rec.Code != 200 {
		t.Fatalf("audit: %d %v", rec.Code, err)
	}
	var grants int
	for _, a := range audit {
		if a.Action == "POST /v1/admin/users/{id}/access" && a.Status == 204 && a.TargetUser != nil && *a.TargetUser == u.ID &&
			a.KeyFingerprint == keyFingerprint(key) && a.RequestID != "" {
			grants++
		}
		if a.Status == 401 {
			t.Errorf("an unauthorized call is not audited (no key to name): %+v", a)
		}
	}
	if grants != 1 {
		t.Fatalf("one audited grant with its target, key and request id: %+v", audit)
	}
}
