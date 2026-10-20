package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"tim@gmail.com": "t***@g***.com", "x7k2@privaterelay.appleid.com": "x***@p***.com",
		"ö@ümlaut.de": "ö***@ü***.de", "a@localhost": "a***@l***", "nonsense": "***", "@x.de": "***",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if !isRelay("X7K2@PrivateRelay.AppleID.com") || isRelay("tim@gmail.com") {
		t.Fatal("relay detection")
	}
}

// The detail through the admin chain: masked unless asked, duplicates by device,
// identities noted as unavailable without a Supabase admin key (D89).
func TestAdminUserDetail(t *testing.T) {
	h, u, st := journalHandler(t)
	ctx := context.Background()
	h.accounts = &accounts{st: st, log: h.log}
	email := "tim@gmail.com"
	if _, err := st.EnsureUser(ctx, store.Identity{ID: u.ID, Email: email, Provider: "apple"}); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: other, Provider: "email"}); err != nil {
		t.Fatal(err)
	}
	phone := uuid.New()
	for _, id := range []uuid.UUID{u.ID, other} { // one iPhone, two accounts
		if err := st.TouchDevice(ctx, id, phone, "iPhone", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordSync(ctx, u.ID, phone, map[string]any{"samples": 12, "days": 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, u.ID, "2026-10-20", nil); err != nil {
		t.Fatal(err)
	}
	key := "test-admin-key-0123456789abcdef0123456789"
	srv := httpx.Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), adminRoutes(h, key, 5*time.Second))
	get := func(path string) (int, model.AdminUserDetail) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		var d model.AdminUserDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return rec.Code, d
	}
	code, d := get("/v1/admin/users/" + u.ID.String()[:8])
	a := d.Account
	if code != 200 || a.ID != u.ID || a.Email == nil || *a.Email != "t***@g***.com" || a.Consent == nil ||
		a.Consent.PolicyVersion != "2026-10-20" || a.Identities != nil || !strings.Contains(a.IdentitiesNote, "not available") {
		t.Fatalf("account: %d %+v", code, a)
	}
	if len(d.Devices) != 1 || d.Devices[0].ID != phone || string(d.Devices[0].LastSync) != `{"days":1,"samples":12}` ||
		d.Data.LastSyncAt == nil || len(d.Duplicates) != 1 || d.Duplicates[0].ID != other || *d.Duplicates[0].Provider != "email" {
		t.Fatalf("devices, last sync, duplicates: %+v %+v %+v", d.Devices, d.Data, d.Duplicates)
	}
	if _, d := get("/v1/admin/users/" + u.ID.String() + "?emails=1"); d.Account.Email == nil || *d.Account.Email != email {
		t.Fatalf("?emails=1 shows it: %+v", d.Account.Email)
	}
	if code, _ := get("/v1/admin/users/" + uuid.NewString()); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	users, err := st.AdminUsers(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range users {
		if (row.ID == u.ID || row.ID == other) && row.Duplicates != 1 {
			t.Errorf("%s: dup %d, want 1", row.ShortID, row.Duplicates)
		}
	}
	audit, _ := st.AuditSince(ctx, time.Now().Add(-time.Minute), 10)
	var shown bool
	for _, e := range audit {
		var det map[string]any
		_ = json.Unmarshal(e.Details, &det)
		if e.Action == "GET /v1/admin/users/{id}" && e.TargetUser != nil && *e.TargetUser == u.ID && det["emails"] == true {
			shown = true
		}
	}
	if !shown {
		t.Fatalf("the ?emails=1 read is audited with its flag: %+v", audit)
	}
}
