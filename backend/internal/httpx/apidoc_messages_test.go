package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// docs/API.md → Errors says "branch on the status code, not the text", so most messages are
// free text. A few it quotes verbatim, and a client or a support person greps for them:
//
//	"request timed out"   503, Conventions → Timeouts and Errors, Train import → Retries
//	"internal error"      500, Errors
//	the NUL text          422, Errors → NUL (also in the example block)
//	"bad json: …"         400, a raw 0x00 byte (the text after "bad json:" is the decoder's)
//	"diagnostics: a number is out of range"   422, Errors → NUL
//	the URL text          400, Errors → NUL
//	the database text     422, Errors → NUL (last paragraph)
//
// This test gets each message from the code, by making the code answer, and requires API.md to
// carry it character for character. It also requires that every other {"error": "…"} in
// API.md is a placeholder or a message pinned elsewhere (internal/gate), so a new quotation
// cannot slip in without a pin.

const apiDoc = "../../../docs/API.md"

// answer runs h on a request and returns the error body's message.
func answer(t *testing.T, h http.Handler, r *http.Request) (int, string, http.Header) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the error shape: %v", w.Body.String(), err)
	}
	return w.Code, body.Error, w.Header()
}

// jsonQuoted is s as it appears inside a JSON string in API.md ("\u0000" is written \\u0000).
func jsonQuoted(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPIDocQuotedMessagesMatchCode(t *testing.T) {
	raw, err := os.ReadFile(apiDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	failWith := func(err error, ctx context.Context) (int, string, http.Header) {
		r := httptest.NewRequest(http.MethodGet, "/v1/x", nil).WithContext(ctx)
		return answer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Fail(w, r, err) }), r)
	}

	// the five messages, each as the code answers it
	type msg struct {
		name   string
		status int
		text   string
	}
	var got []msg

	st, text, h := failWith(errors.New("boom"), context.Background())
	if st != 500 || h.Get("Retry-After") != "" {
		t.Fatalf("Fail(plain error) = %d, Retry-After %q; API.md says 500 (retry later, no Retry-After)", st, h.Get("Retry-After"))
	}
	got = append(got, msg{"internal error (Fail)", st, text})
	internal := text

	st, text, h = failWith(context.DeadlineExceeded, context.Background())
	if st != 503 || h.Get("Retry-After") == "" {
		t.Fatalf("Fail(deadline) = %d, Retry-After %q; API.md says 503 with Retry-After", st, h.Get("Retry-After"))
	}
	got = append(got, msg{"request timed out (Fail)", st, text})
	timedOut := text

	st, text, _ = failWith(sqlStateError("22021"), context.Background())
	if st != 422 {
		t.Fatalf("Fail(SQLSTATE 22021) = %d, want 422", st)
	}
	got = append(got, msg{"database text (Fail)", st, text})
	dbText := text

	// Recover's 500 is the same text as Fail's
	st, text, _ = answer(t, Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })),
		httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if st != 500 || text != internal {
		t.Errorf("Recover answered %d %q, want 500 %q (the same as Fail)", st, text, internal)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, map[string]string{}) })
	st, text, _ = answer(t, WithRoutes(mux), httptest.NewRequest(http.MethodGet, "/v1/x?q=%00", nil))
	if st != 400 {
		t.Fatalf("a %%00 in the query = %d, want 400", st)
	}
	got = append(got, msg{"URL text (WithRoutes)", st, text})

	decode := func(body string, v any) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
		if err := ReadJSON(w, r, v, MaxBodyDefault); err != nil {
			WriteDecodeError(w, err)
			var b struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
				t.Fatalf("decode error body %q: %v", w.Body.String(), err)
			}
			return w.Code, b.Error
		}
		t.Fatalf("ReadJSON accepted %q", body)
		return 0, ""
	}
	var s struct {
		A string `json:"a"`
	}
	st, text = decode(`{"a":"x\u0000y"}`, &s)
	if st != 422 {
		t.Fatalf("a \\u0000 in a string = %d, want 422", st)
	}
	got = append(got, msg{"NUL text (ReadJSON)", st, text})

	st, text = decode("{\"a\":\"x\x00y\"}", &s)
	if st != 400 || !strings.HasPrefix(text, "bad json: ") {
		t.Fatalf("a raw 0x00 byte = %d %q, want 400 \"bad json: …\"", st, text)
	}
	badJSON := text // the decoder's own wording after the prefix varies (API.md says so): only the prefix is pinned

	var f struct {
		Diagnostics json.RawMessage `json:"diagnostics"`
	}
	st, text = decode(`{"diagnostics":{"n":1e400}}`, &f)
	if st != 422 {
		t.Fatalf("a number beyond double precision = %d, want 422", st)
	}
	got = append(got, msg{"diagnostics number (ReadJSON)", st, text})

	// 1. each message is in API.md, character for character
	for _, m := range got {
		if !strings.Contains(doc, jsonQuoted(t, m.text)) {
			t.Errorf("docs/API.md does not quote the %s message %s, which the code answers with %d: fix the doc, or the code and the doc together",
				m.name, jsonQuoted(t, m.text), m.status)
		}
	}
	if !strings.Contains(doc, `"bad json: `) {
		t.Errorf("docs/API.md does not show the \"bad json: …\" body of a raw 0x00 byte")
	}
	// the two messages the doc also quotes outside a JSON example, and the DB text in a sentence
	for _, q := range []struct{ name, text string }{{"internal error", internal}, {"request timed out", timedOut}, {"database text", dbText}} {
		if !strings.Contains(doc, `"`+q.text+`"`) {
			t.Errorf("docs/API.md does not quote %s as \"…\" in a sentence", q.name)
		}
	}
	if !strings.Contains(doc, `{"error":"`+timedOut+`"}`) {
		t.Errorf("docs/API.md → Conventions → Timeouts does not show {\"error\":%q}", timedOut)
	}

	// 2. the other handlers that answer the same texts say it the same way. cmd/api's import
	// answers 503 "request timed out" itself (importUnavailable).
	src, err := os.ReadFile("../../cmd/api/train_import.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `msg = "`+timedOut+`"`) {
		t.Errorf("cmd/api/train_import.go no longer answers %q the way API.md → Retries says; keep it equal to httpx.Fail's", timedOut)
	}

	// 3. every {"error": "…"} in API.md is one of these, a placeholder, or pinned elsewhere
	known := map[string]bool{}
	for _, m := range got {
		known[m.text] = true
	}
	placeholders := []string{
		"human-readable message",         // the shape example
		"…",                              // elided, e.g. /readyz
		"consent to the privacy policy ", // the gate's, pinned by internal/gate's test
	}
	pairs := regexp.MustCompile(`"error":\s*("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(doc, -1)
	if len(pairs) < 6 {
		t.Fatalf("found %d \"error\": \"…\" quotations in API.md, expected at least 6: the scan is broken", len(pairs))
	}
	for _, p := range pairs {
		var s string
		if err := json.Unmarshal([]byte(p[1]), &s); err != nil {
			t.Errorf("API.md quotes %s, which is not a JSON string: %v", p[1], err)
			continue
		}
		ok := known[s] || (strings.HasPrefix(s, "bad json: ") && strings.HasPrefix(badJSON, "bad json: "))
		for _, ph := range placeholders {
			ok = ok || strings.HasPrefix(s, ph)
		}
		if !ok {
			t.Errorf("API.md quotes the error message %s and nothing pins it: add it to this test (and to the code's answer), or write it as a placeholder", p[1])
		}
	}
}
