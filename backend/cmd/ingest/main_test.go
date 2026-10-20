package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
)

func TestAdmitted(t *testing.T) {
	current := &model.Consent{PolicyVersion: "2026-10-20", GrantedAt: time.Now()}
	old := &model.Consent{PolicyVersion: "2026-01-01", GrantedAt: time.Now()}
	off, warn, on := config.ModeOff, config.ModeWarn, config.ModeOn
	for _, c := range []struct {
		name          string
		gate, consent config.Mode
		user          model.User
		code          string // "" = admitted
		header        string // X-RYFT-Gate when admitted in warn mode
	}{
		{"consented, gate off", off, on, model.User{Consent: current}, "", ""},
		{"no consent", off, on, model.User{BetaAccess: true}, model.CodeConsentRequired, ""},
		{"an older policy", off, on, model.User{BetaAccess: true, Consent: old}, model.CodeConsentRequired, ""},
		{"gate on, no access", on, on, model.User{Consent: current}, model.CodeInviteRequired, ""},
		{"gate on, access and consent", on, on, model.User{BetaAccess: true, Consent: current}, "", ""},
		{"gate on, neither: the invite first", on, on, model.User{}, model.CodeInviteRequired, ""},
		{"consent warn: served, flagged", off, warn, model.User{BetaAccess: true}, "", "consent_required"},
		{"both warn: both flagged", warn, warn, model.User{}, "", "invite_required,consent_required"},
		{"gate warn, consent on: still refused", warn, on, model.User{}, model.CodeConsentRequired, ""},
		{"consent off", off, off, model.User{}, "", ""},
	} {
		h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			beta: config.Beta{Gate: c.gate, Consent: c.consent, PrivacyPolicyVersion: "2026-10-20"}}
		rec := httptest.NewRecorder()
		ok := h.admitted(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", nil), c.user)
		var body struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		header := strings.Join(rec.Header().Values("X-RYFT-Gate"), ",")
		if ok != (c.code == "") || body.Code != c.code || (!ok && rec.Code != 403) || (ok && header != c.header) {
			t.Errorf("%s: admitted=%v status=%d code=%q header=%q", c.name, ok, rec.Code, body.Code, header)
		}
	}
}

func TestValidateBetaFields(t *testing.T) {
	long := func(n int) *string { s := strings.Repeat("x", n); return &s }
	now := time.Now()
	base := func() model.SyncRequest {
		return model.SyncRequest{DeviceID: uuid.New(), AppVersion: long(64), OSVersion: long(64),
			Samples:  []model.Sample{{Type: model.SampleSteps, Start: now, End: now, SourceBundleID: long(255)}},
			Sleep:    []model.SleepSegment{{Start: now, End: now.Add(time.Hour), Stage: "core", SourceBundleID: long(255)}},
			Workouts: []model.Workout{{ID: uuid.New(), Start: now, End: now.Add(time.Hour), SourceBundleID: long(255)}}}
	}
	ok := base()
	if err := validate(&ok); err != nil {
		t.Fatalf("at the limits: %v", err)
	}
	for name, breakIt := range map[string]func(*model.SyncRequest){
		"app_version": func(r *model.SyncRequest) { r.AppVersion = long(65) },
		"os_version":  func(r *model.SyncRequest) { r.OSVersion = long(65) },
		"sample":      func(r *model.SyncRequest) { r.Samples[0].SourceBundleID = long(256) },
		"sleep":       func(r *model.SyncRequest) { r.Sleep[0].SourceBundleID = long(256) },
		"workout":     func(r *model.SyncRequest) { r.Workouts[0].SourceBundleID = long(256) },
	} {
		r := base()
		breakIt(&r)
		if err := validate(&r); err == nil {
			t.Errorf("%s too long must be refused", name)
		}
	}
}
