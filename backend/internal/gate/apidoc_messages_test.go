package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
)

// docs/API.md → Errors shows the body of a refused upload next to the `code` table. The
// message is free text for clients ("branch on the code"), but the example must be what the
// service sends, with the policy version filled in, so the doc is not a source of made-up text.
func TestAPIDocConsentExampleMatchesCode(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`"error":\s*("(?:[^"\\]|\\.)*"),\s*"code":\s*"consent_required"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal(`docs/API.md → Errors no longer shows an {"error": …, "code": "consent_required"} example`)
	}
	var docMsg string
	if err := json.Unmarshal(m[1], &docMsg); err != nil {
		t.Fatal(err)
	}
	v := regexp.MustCompile(`^consent to the privacy policy (\d{4}-\d{2}-\d{2}) is required before uploading health data$`).FindStringSubmatch(docMsg)
	if v == nil {
		t.Fatalf("the documented message %q is not the gate's shape %q", docMsg, "consent to the privacy policy <version> is required before uploading health data")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/sync", nil)
	if Consent(w, r, config.Beta{Consent: config.ModeOn, PrivacyPolicyVersion: v[1]}, model.User{}, "api") {
		t.Fatal("a user without consent passed the gate")
	}
	var body struct{ Error, Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || body.Code != model.CodeConsentRequired || body.Error != docMsg {
		t.Errorf("the gate answered %d code %q %q; API.md shows 403 %q %q", w.Code, body.Code, body.Error, model.CodeConsentRequired, docMsg)
	}
}
