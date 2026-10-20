package main

import (
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestTodayState(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	scored := now.Add(-time.Hour)
	fresh := map[string]time.Time{"hr": now.Add(-10 * time.Minute)}
	for _, c := range []struct {
		name string
		st   store.SyncState
		want string
	}{
		{"queued", store.SyncState{Latest: fresh, Pending: []string{"2026-10-21"}, TodayScoredAt: &scored, TodayHasSleep: true}, "pending"},
		{"scored with a night", store.SyncState{Latest: fresh, TodayScoredAt: &scored, TodayHasSleep: true}, "scored"},
		{"scored, no night yet", store.SyncState{Latest: fresh, TodayScoredAt: &scored}, "waiting_for_sleep"},
		{"fresh data, no score yet", store.SyncState{Latest: fresh}, "pending"},
		{"nothing for 21 h", store.SyncState{Latest: map[string]time.Time{"hr": now.Add(-21 * time.Hour)}, TodayScoredAt: &scored}, "stale"},
		{"never any data", store.SyncState{Latest: map[string]time.Time{}}, "stale"},
		{"another day pending only", store.SyncState{Latest: fresh, Pending: []string{"2026-10-19"}, TodayScoredAt: &scored, TodayHasSleep: true}, "scored"},
	} {
		if got := todayState(c.st, "2026-10-21", now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestGateFor(t *testing.T) {
	current := &model.Consent{PolicyVersion: "2026-10-20"}
	h := &handler{beta: config.Beta{Gate: config.ModeWarn, Consent: config.ModeWarn, PrivacyPolicyVersion: "2026-10-20"}}
	if g := h.gateFor(model.User{}); g == nil || *g != "invite_required" {
		t.Fatalf("warn still says what is missing, the invite first: %v", g)
	}
	if g := h.gateFor(model.User{BetaAccess: true}); g == nil || *g != "consent_required" {
		t.Fatalf("consent: %v", g)
	}
	if g := h.gateFor(model.User{BetaAccess: true, Consent: current}); g != nil {
		t.Fatalf("nothing missing: %v", *g)
	}
	h.beta = config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}
	if g := h.gateFor(model.User{}); g != nil {
		t.Fatalf("both off: %v", *g)
	}
}
