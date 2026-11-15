package app

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
)

// docs/API.md quotes the start gate's two answers (Endpoints → Health, Conventions → Errors
// → 503) character for character; internal/httpx's TestAPIDocQuotedMessagesMatchCode lists
// them as pinned here. Each is taken from the gate by making it answer.
func TestAPIDocQuotesTheStartGate(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	quiet := slog.New(slog.DiscardHandler)
	g := newGate("svc", quiet, auth.InsecurePush(quiet), nil, false, time.Millisecond)

	for _, c := range []struct {
		name, path, retryAfter string
	}{
		{"a ready path before the start is done", httpx.PathReady, ""},
		{"a request that waited for the start in vain", "/v1/me", "5"},
	} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Fatalf("%s: body %q is not the error shape: %v", c.name, w.Body.String(), err)
		}
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != c.retryAfter {
			t.Errorf("%s: %d, Retry-After %q; API.md says 503, Retry-After %q", c.name, w.Code, w.Header().Get("Retry-After"), c.retryAfter)
		}
		quoted, _ := json.Marshal(body.Error)
		if want := `{"error":` + string(quoted) + `}`; !strings.Contains(doc, want) {
			t.Errorf("%s: docs/API.md does not quote %s, which the gate answers: fix the doc, or the code and the doc together", c.name, want)
		}
	}
}
