package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
)

func TestBetaGate(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	without, with := model.User{}, model.User{BetaAccess: true}
	for _, c := range []struct {
		method, path string
		user         model.User
		want         int
	}{
		{"GET", "/v1/days", without, 403}, {"POST", "/v1/devices/x/push", without, 403},
		{"GET", "/v1/me", without, 204}, {"GET", "/v1/config", without, 204}, {"GET", "/v1/model", without, 204},
		{"POST", "/v1/me/invite", without, 204}, {"POST", "/v1/me/consent", without, 204},
		{"DELETE", "/v1/me/consent", without, 204}, {"POST", "/v1/feedback", without, 204},
		{"PATCH", "/v1/me", without, 403}, {"GET", "/v1/days", with, 204},
	} {
		rec := httptest.NewRecorder()
		betaGate(true, inner).ServeHTTP(rec, asUser(httptest.NewRequest(c.method, c.path, nil), c.user, ""))
		if rec.Code != c.want {
			t.Errorf("%s %s (access %v): %d, want %d", c.method, c.path, c.user.BetaAccess, rec.Code, c.want)
		}
		if rec.Code == 403 && !strings.Contains(rec.Body.String(), `"code":"invite_required"`) {
			t.Errorf("%s %s: body %s", c.method, c.path, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	betaGate(false, inner).ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodGet, "/v1/days", nil), without, ""))
	if rec.Code != 204 {
		t.Fatal("gate off: everyone passes")
	}
}

func TestConsentAndInviteAPI(t *testing.T) {
	h, u, st := journalHandler(t)
	h.beta = config.Beta{PrivacyPolicyVersion: "2026-10-20", PrivacyPolicyURL: "https://ryftapp.de/privacy"}
	ctx := context.Background()
	if rec := call(t, h.postConsent, u, "POST", "/v1/me/consent", `{"policy_version": "2025-01-01"}`); rec.Code != 422 {
		t.Fatalf("an outdated version: %d", rec.Code)
	}
	if rec := call(t, h.postConsent, u, "POST", "/v1/me/consent", `{"policy_version": "2026-10-20", "app_version": "0.9.0 (41)"}`); rec.Code != 204 {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	if g, _, _ := st.GetUser(ctx, u.ID); g.Consent == nil || g.Consent.PolicyVersion != "2026-10-20" {
		t.Fatalf("stored: %+v", g.Consent)
	}
	if rec := call(t, h.deleteConsent, u, "DELETE", "/v1/me/consent", ""); rec.Code != 204 {
		t.Fatal("withdraw")
	}
	rec := call(t, h.remoteConfig, u, "GET", "/v1/config", "")
	var cfg model.RemoteConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.PrivacyPolicyVersion != "2026-10-20" || cfg.PrivacyPolicyURL != "https://ryftapp.de/privacy" {
		t.Fatalf("config: %+v", cfg)
	}

	// Invite codes through the admin routes and the user's redeem.
	a := admin{h: h, keyID: keyFingerprint("k")}
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/invites", strings.NewReader(`{"note": "Anna", "max_uses": 1}`))
	cr := httptest.NewRecorder()
	a.createInvite(cr, r)
	var inv model.BetaInvite
	if err := json.Unmarshal(cr.Body.Bytes(), &inv); err != nil || cr.Code != 201 || inv.Note != "Anna" {
		t.Fatalf("create: %d %s", cr.Code, cr.Body)
	}
	for body, want := range map[string]int{`{"max_uses": 0}`: 422, `{"expires_at": "2020-01-01T00:00:00Z"}`: 422} {
		rr := httptest.NewRecorder()
		a.createInvite(rr, httptest.NewRequest(http.MethodPost, "/v1/admin/invites", strings.NewReader(body)))
		if rr.Code != want {
			t.Errorf("create %s: %d", body, rr.Code)
		}
	}
	// journalHandler's user was created after 0017, so it starts without access.
	if rec := call(t, h.redeemInvite, u, "POST", "/v1/me/invite", `{"code": "RYFT-ZZZZ-ZZZZ"}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), "invalid or used code") {
		t.Fatalf("unknown code: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h.redeemInvite, u, "POST", "/v1/me/invite", `{"code": "`+strings.ToLower(inv.Code)+`"}`); rec.Code != 204 {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	if g, _, _ := st.GetUser(ctx, u.ID); !g.BetaAccess {
		t.Fatal("access after the code")
	}
	lr := httptest.NewRecorder()
	a.invites(lr, httptest.NewRequest(http.MethodGet, "/v1/admin/invites", nil))
	if !strings.Contains(lr.Body.String(), `"uses":1`) {
		t.Fatalf("list: %s", lr.Body)
	}
	dr := httptest.NewRecorder()
	d := httptest.NewRequest(http.MethodDelete, "/v1/admin/invites/"+inv.Code, nil)
	d.SetPathValue("code", inv.Code)
	a.deleteInvite(dr, d)
	if dr.Code != 204 {
		t.Fatalf("delete: %d", dr.Code)
	}
}
