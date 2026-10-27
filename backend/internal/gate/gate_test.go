package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

func TestConsented(t *testing.T) {
	b := config.Beta{PrivacyPolicyVersion: "2026-10-01"}
	for _, tc := range []struct {
		name    string
		consent *model.Consent
		want    bool
	}{
		{"none", nil, false},
		{"the current policy", &model.Consent{PolicyVersion: "2026-10-01", GrantedAt: time.Now()}, true},
		{"an older policy", &model.Consent{PolicyVersion: "2026-06-01", GrantedAt: time.Now()}, false},
	} {
		if got := Consented(b, model.User{Consent: tc.consent}); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// One gate in each mode: off and passing serve without a header; warn serves with the
// code in X-RYFT-Gate and counts it; on answers 403 with the code and counts it.
func TestApply(t *testing.T) {
	b := config.Beta{PrivacyPolicyVersion: "2026-10-01"}
	for _, tc := range []struct {
		name    string
		mode    config.Mode
		consent bool
		served  bool
		header  string
		counted float64
	}{
		{"off", config.ModeOff, false, true, "", 0},
		{"on, consented", config.ModeOn, true, true, "", 0},
		{"warn", config.ModeWarn, false, true, model.CodeConsentRequired, 1},
		{"on", config.ModeOn, false, false, "", 1},
	} {
		b.Consent = tc.mode
		u := model.User{}
		if tc.consent {
			u.Consent = &model.Consent{PolicyVersion: b.PrivacyPolicyVersion}
		}
		c := metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(tc.mode), "test")
		before := testutil.ToFloat64(c)
		rec := httptest.NewRecorder()
		served := Consent(rec, httptest.NewRequest(http.MethodPost, "/v1/train/import", nil), b, u, "test")
		if served != tc.served || rec.Header().Get(httpx.GateHeader) != tc.header || testutil.ToFloat64(c)-before != tc.counted {
			t.Fatalf("%s: served %v, header %q, counted %v", tc.name, served, rec.Header().Get(httpx.GateHeader), testutil.ToFloat64(c)-before)
		}
		if !served {
			var body map[string]string
			if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["code"] != model.CodeConsentRequired {
				t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
			}
		}
	}
	rec := httptest.NewRecorder()
	if Invite(rec, httptest.NewRequest(http.MethodGet, "/v1/days", nil), config.ModeOn, model.User{}, "test") || rec.Code != http.StatusForbidden {
		t.Fatalf("invite on: %d", rec.Code)
	}
	if !Invite(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/days", nil), config.ModeOn, model.User{BetaAccess: true}, "test") {
		t.Fatal("an account with access passes")
	}
}
