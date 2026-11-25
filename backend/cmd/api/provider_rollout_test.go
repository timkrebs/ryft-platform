package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/wearables"
)

func TestProviderRolloutIsDarkByDefault(t *testing.T) {
	dark := providerCatalog(nil, nil)
	if len(dark) != 2 || dark[0].Provider != "garmin" || dark[1].Provider != "suunto" {
		t.Fatalf("unexpected catalog: %+v", dark)
	}
	for _, p := range dark {
		if p.Availability != "partner_onboarding" {
			t.Fatalf("unexpected active provider: %+v", p)
		}
	}
	for _, tc := range []struct {
		offered []model.AvailableConnection
		want    string
	}{
		{nil, "unavailable"},
		{[]model.AvailableConnection{{Provider: "garmin"}}, "full"},
		{[]model.AvailableConnection{{Provider: "garmin", Accepting: true}}, "available"},
	} {
		if got := providerCatalog([]string{"garmin"}, tc.offered)[0].Availability; got != tc.want {
			t.Fatalf("catalog state %s, want %s", got, tc.want)
		}
	}
}

func TestCoachingWebhookRejectsUnsafeRequests(t *testing.T) {
	const secret = "fixture-only-webhook-signing-key"
	signed := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}
	invalid := []byte("{")
	oversized := bytes.Repeat([]byte{' '}, wearables.PartnerBodyLimit+1)
	for _, tc := range []struct {
		name       string
		configured bool
		body       []byte
		signatures []string
		mediaType  string
		want       int
	}{
		{"disabled", false, invalid, []string{signed(invalid)}, "application/json", http.StatusNotFound},
		{"missing signature", true, invalid, nil, "application/json", http.StatusUnauthorized},
		{"wrong signature", true, invalid, []string{"abcdef"}, "application/json", http.StatusUnauthorized},
		{"tampered body", true, invalid, []string{signed([]byte("{}"))}, "application/json", http.StatusUnauthorized},
		{"duplicate signature", true, invalid, []string{signed(invalid), signed(invalid)}, "application/json", http.StatusUnauthorized},
		{"signed missing type", true, invalid, []string{signed(invalid)}, "", http.StatusUnsupportedMediaType},
		{"signed unsupported type", true, invalid, []string{signed(invalid)}, "text/plain", http.StatusUnsupportedMediaType},
		{"signed malformed request", true, invalid, []string{signed(invalid)}, "application/json", http.StatusUnprocessableEntity},
		{"oversized", true, oversized, []string{signed(oversized)}, "application/json", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{}
			if tc.configured {
				h.partners.secrets = map[string]string{"garmin": secret}
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/garmin", bytes.NewReader(tc.body))
			for _, signature := range tc.signatures {
				req.Header.Add("X-HMAC-SHA256-Signature", signature)
			}
			if tc.mediaType != "" {
				req.Header.Set("Content-Type", tc.mediaType)
			}
			w := httptest.NewRecorder()
			h.partnerWebhook("garmin").ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestCoachingRelayConfigRefusesUnapprovedCloudRunActivation(t *testing.T) {
	for _, key := range []string{"COACHING_GARMIN_WEBHOOK_SECRET", "COACHING_SUUNTO_WEBHOOK_SECRET",
		"COACHING_PARTNER_FETCH_TOKEN", "COACHING_GARMIN_FETCH_URL", "COACHING_SUUNTO_FETCH_URL", "K_SERVICE", "CLOUD_RUN_JOB"} {
		t.Setenv(key, "")
	}
	const fixtureKey = "fixture-garmin-coaching-auth-key"
	for _, marker := range []string{"K_SERVICE", "CLOUD_RUN_JOB"} {
		t.Run(marker, func(t *testing.T) {
			t.Setenv(marker, "fixture-runtime")
			if _, err := loadPartnerConfig(); err != nil {
				t.Fatalf("dark config: %v", err)
			}
			t.Setenv("COACHING_GARMIN_WEBHOOK_SECRET", fixtureKey)
			if _, err := loadPartnerConfig(); err == nil || !strings.Contains(err.Error(), "approved connector") {
				t.Fatalf("accepted relay on Cloud Run: %v", err)
			}
		})
	}
	t.Setenv("COACHING_GARMIN_WEBHOOK_SECRET", fixtureKey)
	if cfg, err := loadPartnerConfig(); err != nil || cfg.secrets["suunto"] != "" {
		t.Fatalf("local isolated provider config failed: %v", err)
	}
	t.Setenv("COACHING_SUUNTO_WEBHOOK_SECRET", fixtureKey)
	if _, err := loadPartnerConfig(); err == nil || !strings.Contains(err.Error(), "distinct signing keys") {
		t.Fatalf("accepted shared signing key: %v", err)
	}
}

func TestCoachingWebhookKeysAreIndependentOnRotation(t *testing.T) {
	const garminOld = "fixture-garmin-old-key-0000000000000"
	const garminNew = "fixture-garmin-new-key-0000000000000"
	const suunto = "fixture-suunto-independent-key-000000"
	h := &handler{partners: partnerConfig{secrets: map[string]string{"garmin": garminNew, "suunto": suunto}}}
	for _, tc := range []struct {
		provider, key string
		status        int
	}{
		{"garmin", garminOld, http.StatusUnauthorized},
		{"garmin", suunto, http.StatusUnauthorized},
		{"garmin", garminNew, http.StatusUnprocessableEntity},
		{"suunto", garminNew, http.StatusUnauthorized},
		{"suunto", suunto, http.StatusUnprocessableEntity},
	} {
		body := []byte("{")
		mac := hmac.New(sha256.New, []byte(tc.key))
		_, _ = mac.Write(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/"+tc.provider, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-HMAC-SHA256-Signature", hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		h.partnerWebhook(tc.provider).ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s fixture status %d, want %d", tc.provider, w.Code, tc.status)
		}
	}
}
