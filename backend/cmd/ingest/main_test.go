package main

import (
	"encoding/json"
	"io"
	"log/slog"
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
