package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
)

func TestAdmitted(t *testing.T) {
	current := &model.Consent{PolicyVersion: "2026-10-20", GrantedAt: time.Now()}
	old := &model.Consent{PolicyVersion: "2026-01-01", GrantedAt: time.Now()}
	for _, c := range []struct {
		name string
		gate bool
		user model.User
		code string // "" = admitted
	}{
		{"consented, gate off", false, model.User{Consent: current}, ""},
		{"no consent", false, model.User{BetaAccess: true}, model.CodeConsentRequired},
		{"an older policy", false, model.User{BetaAccess: true, Consent: old}, model.CodeConsentRequired},
		{"gate on, no access", true, model.User{Consent: current}, model.CodeInviteRequired},
		{"gate on, access and consent", true, model.User{BetaAccess: true, Consent: current}, ""},
		{"gate on, neither: the invite first", true, model.User{}, model.CodeInviteRequired},
	} {
		h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			beta: config.Beta{Gate: c.gate, PrivacyPolicyVersion: "2026-10-20"}}
		rec := httptest.NewRecorder()
		ok := h.admitted(rec, c.user)
		var body struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if ok != (c.code == "") || body.Code != c.code || (!ok && rec.Code != 403) {
			t.Errorf("%s: admitted=%v status=%d code=%q", c.name, ok, rec.Code, body.Code)
		}
	}
}
