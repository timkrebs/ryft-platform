package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/timkrebs/ryft/internal/httpx"
)

// docs/API.md says an import that runs out of time answers 503 "request timed out" with
// Retry-After (Conventions → Timeouts, Train import → Retries), the same words as every other
// route's timeout (httpx.Fail). importUnavailable writes its own, so this pins that it says the
// same as Fail, and that API.md quotes it.
func TestImportTimeoutSaysWhatFailSays(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := func(h func(w http.ResponseWriter, r *http.Request)) (int, string, string) {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, "/v1/train/import", nil).WithContext(context.Background()))
		var b struct{ Error string }
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return w.Code, b.Error, w.Header().Get("Retry-After")
	}
	fs, fm, fr := body(func(w http.ResponseWriter, r *http.Request) { httpx.Fail(w, r, context.DeadlineExceeded) })
	is, im, ir := body(func(w http.ResponseWriter, r *http.Request) {
		importUnavailable(w, r, errors.New("deadline"), "timeout")
	})
	if is != http.StatusServiceUnavailable || im != fm || ir == "" || fs != is || fr == "" {
		t.Errorf("import timeout = %d %q Retry-After %q; httpx.Fail = %d %q Retry-After %q: API.md promises the same 503 text on both", is, im, ir, fs, fm, fr)
	}
	doc, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `503 "`+im+`"`) {
		t.Errorf("docs/API.md → Train import → Retries no longer says: 503 %q", im)
	}
}
