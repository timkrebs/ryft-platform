package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
	for _, tc := range []struct {
		name       string
		configured bool
		body       []byte
		signature  string
		want       int
	}{
		{"disabled", false, invalid, signed(invalid), http.StatusNotFound},
		{"missing signature", true, invalid, "", http.StatusUnauthorized},
		{"wrong signature", true, invalid, "abcdef", http.StatusUnauthorized},
		{"signed malformed request", true, invalid, signed(invalid), http.StatusUnprocessableEntity},
		{"oversized", true, bytes.Repeat([]byte{' '}, wearables.PartnerBodyLimit+1), "", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &handler{}
			if tc.configured {
				h.partners.secrets = map[string]string{"garmin": secret}
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/garmin", bytes.NewReader(tc.body))
			req.Header.Set("X-HMAC-SHA256-Signature", tc.signature)
			w := httptest.NewRecorder()
			h.partnerWebhook("garmin").ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}
