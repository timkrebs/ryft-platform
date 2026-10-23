package httpx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

// holdsNUL is the oracle: whether any string of a decoded JSON value, key or
// value, contains U+0000.
func holdsNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case []any:
		for _, x := range v {
			if holdsNUL(x) {
				return true
			}
		}
	case map[string]any:
		for k, x := range v {
			if strings.ContainsRune(k, 0) || holdsNUL(x) {
				return true
			}
		}
	}
	return false
}

// The scanner finds "\u0000" exactly where encoding/json decodes a NUL: in
// values and keys at any depth, after an odd run of backslashes only, next to
// surrogates; fed whole or a byte at a time.
func TestNULScanner(t *testing.T) {
	for _, c := range []struct {
		json string
		nul  bool
	}{
		{`"\u0000"`, true},
		{`"a\u0000b"`, true},
		{`"\u00000"`, true},
		{`"\b\f\n\r\t\u0000"`, true},
		{`"€\u0000"`, true},
		{`["x", "\u0000"]`, true},
		{`{"\u0000": 1}`, true},
		{`{"a\u0000b": "v"}`, true},
		{`{"a": {"b": [{"c\u0000": null}]}}`, true},
		{`{"a": {"b": [{"c": "d\u0000"}]}}`, true},
		// Backslash runs: odd before "u0000" escapes it, even escapes themselves.
		{`"\\u0000"`, false},
		{`"\\\u0000"`, true},
		{`"\\\\u0000"`, false},
		{`"\\\\\u0000"`, true},
		{`"\\\\\\u0000"`, false},
		{`{"u0000": "\\u0000"}`, false},
		{`"\"\\u0000\""`, false},
		// Escaped quotes and solidus do not end a string or start an escape.
		{`"x\"\u0000"`, true},
		{`["a\"", "\u0000"]`, true},
		{`["a\\", "u0000"]`, false},
		{`"\/u0000"`, false},
		// Other code points, either case of hex digit.
		{`"\u0001"`, false},
		{`"\u0100"`, false},
		{`"\u1000"`, false},
		{`"\u000a"`, false},
		{`"\u000A"`, false},
		{`"\u00a0\u00A0"`, false},
		// Surrogates: a pair, a lone one (decoded as U+FFFD), a lone one before NUL.
		{`"\ud83d\ude00"`, false},
		{`"\uD83D\uDE00"`, false},
		{`"\ud800"`, false},
		{`"\ud83d\u0000"`, true},
		{`"\udc00\u0000"`, true},
		{`"\u0000\ud83d\ude00"`, true},
		// Outside strings.
		{`"u0000"`, false},
		{`[0, 0.0, "0000"]`, false},
		{`{}`, false},
		{`""`, false},
		{`null`, false},
	} {
		if !json.Valid([]byte(c.json)) {
			t.Fatalf("%s: not JSON", c.json)
		}
		var v any
		if err := json.Unmarshal([]byte(c.json), &v); err != nil {
			t.Fatal(err)
		}
		if holdsNUL(v) != c.nul {
			t.Fatalf("%s: the case is wrong, encoding/json says %v", c.json, holdsNUL(v))
		}
		for name, r := range map[string]func(io.Reader) io.Reader{
			"whole": func(r io.Reader) io.Reader { return r }, "byte by byte": iotest.OneByteReader,
		} {
			s := newNULScanner(r(strings.NewReader(c.json)))
			if _, err := io.Copy(io.Discard, s); err != nil {
				t.Fatal(err)
			}
			if got := s.nul >= 0; got != c.nul {
				t.Errorf("%s (%s): NUL %v, want %v", c.json, name, got, c.nul)
			}
			if c.nul && c.json[s.nul] != '\\' {
				t.Errorf("%s (%s): offset %d is not the escape's backslash", c.json, name, s.nul)
			}
		}
	}
}

type nulPayload struct {
	Note string `json:"note"`
	Sets []struct {
		Tags map[string]string `json:"tags"`
	} `json:"sets"`
	Extra json.RawMessage `json:"extra"`
}

// A NUL in any string of a well-formed body is a 422 in the error shape (no
// code), counted, and nothing else is: an escaped backslash before "u0000" is
// text, raw 0x00 bytes are not JSON (400), a decode error comes first, and what
// follows the value stays ignored.
func TestReadJSONRefusesNUL(t *testing.T) {
	var got nulPayload
	mux := http.NewServeMux()
	mux.HandleFunc("POST /x", func(w http.ResponseWriter, r *http.Request) {
		got = nulPayload{}
		if err := ReadJSON(w, r, &got, 256); err != nil {
			WriteDecodeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := chain(WithRoutes(mux))
	refused := metrics.TextRefused.WithLabelValues("POST /x", "body")
	before := testutil.ToFloat64(refused)
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"value", `{"note": "woke at 3\u0000"}`, 422},
		{"nested key", `{"sets": [{"tags": {"ok": "x"}}, {"tags": {"k\u0000": "v"}}]}`, 422},
		{"nested value", `{"sets": [{"tags": {"k": "\u0000"}}]}`, 422},
		{"raw message", `{"extra": {"diagnostics": ["\u0000"]}}`, 422},
		{"escaped backslash", `{"note": "C:\\u0000", "sets": [{"tags": {"\\u0000": "\\\\u0000"}}]}`, 204},
		{"raw 0x00 in a string", "{\"note\": \"woke\x00\"}", 400},
		{"raw 0x00 between tokens", "{\"note\":\x00\"woke\"}", 400},
		{"NUL and an unknown field", `{"note": "\u0000", "hrv": 61.7}`, 400},
		{"NUL and a type error", `{"note": "\u0000", "sets": 1}`, 400},
		{"NUL in an oversized body", `{"note": "\u0000` + strings.Repeat("x", 300) + `"}`, 413},
		{"NUL after the value", `{"note": "x"} "\u0000"`, 204},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body)))
		if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
			continue
		}
		if c.want != 422 {
			continue
		}
		body := decodeBody(t, rec)
		if body["error"] != `strings must not contain the NUL character (\u0000)` || body["request_id"] == "" || len(body) != 2 {
			t.Errorf("%s: body %v", c.name, body)
		}
	}
	if n := testutil.ToFloat64(refused) - before; n != 4 {
		t.Errorf("the four 422s count as body refusals: %v", n)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"note": "C:\\u0000", "sets": [{"tags": {"\\u0000": "\\\\u0000"}}]}`)))
	if rec.Code != 204 || got.Note != `C:\u0000` || got.Sets[0].Tags[`\u0000`] != `\\u0000` {
		t.Fatalf("the escaped backslash decodes to six characters: %d %+v", rec.Code, got)
	}
}

// An empty body is still io.EOF (POST /v1/sessions/{id}/complete takes none).
func TestReadJSONEmptyBodyIsEOF(t *testing.T) {
	var v nulPayload
	err := ReadJSON(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", http.NoBody), &v, 256)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

// withoutNUL is the stripper's oracle: a decoded JSON value with U+0000
// removed from every string, keys included.
func withoutNUL(v any) any {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(v, "\x00", "")
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = withoutNUL(x)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			out[strings.ReplaceAll(k, "\x00", "")] = withoutNUL(x)
		}
		return out
	}
	return v
}

// readAllIn reads r to the end in reads of size bytes.
func readAllIn(r io.Reader, size int) ([]byte, error) {
	var out []byte
	p := make([]byte, size)
	for {
		n, err := r.Read(p)
		out = append(out, p[:n]...)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// The stripper removes exactly the "\u0000" escapes the scanner finds, from
// values and keys, and passes every other byte as written: an escaped backslash
// before "u0000" stays, and so does text that is not JSON (the decoder refuses
// it). What decodes is what encoding/json decodes without its NULs, however the
// source and the caller split the reads.
func TestNULStripper(t *testing.T) {
	for _, c := range []struct {
		in, out string
		removed int
		valid   bool
	}{
		{`"\u0000"`, `""`, 1, true},
		{`"a\u0000b"`, `"ab"`, 1, true},
		{`"\u00000"`, `"0"`, 1, true},
		{`"\u0000\u0000\u0000"`, `""`, 3, true},
		{`{"k\u0000": "v\u0000", "n": [1, "\u0000x"]}`, `{"k": "v", "n": [1, "x"]}`, 3, true},
		{`"€\u0000€"`, `"€€"`, 1, true},
		{`"\\u0000"`, `"\\u0000"`, 0, true},
		{`"\\\u0000"`, `"\\"`, 1, true},
		{`"\\\\u0000"`, `"\\\\u0000"`, 0, true},
		{`["a\"", "\u0000"]`, `["a\"", ""]`, 1, true},
		{`"\/\u0000\n"`, `"\/\n"`, 1, true},
		{`"\u000A\u0001\uD83D\uDE00"`, `"\u000A\u0001\uD83D\uDE00"`, 0, true},
		{`{"u0000": 0}`, `{"u0000": 0}`, 0, true},
		// Not JSON, passed on as it came: an escape cut off, a bad digit, a NUL outside a string.
		{`"\u00`, `"\u00`, 0, false},
		{`"\u000"`, `"\u000"`, 0, false},
		{`{"a": 1, \u0000}`, `{"a": 1, \u0000}`, 0, false},
	} {
		var want any
		if c.valid {
			if err := json.Unmarshal([]byte(c.in), &want); err != nil {
				t.Fatal(err)
			}
			want = withoutNUL(want)
		}
		for name, read := range map[string]func(*nulStripper) ([]byte, error){
			"whole":               func(s *nulStripper) ([]byte, error) { return io.ReadAll(s) },
			"source byte by byte": func(s *nulStripper) ([]byte, error) { s.r = iotest.OneByteReader(s.r); return io.ReadAll(s) },
			"six-byte reads":      func(s *nulStripper) ([]byte, error) { return readAllIn(s, 6) },
		} {
			s := &nulStripper{r: strings.NewReader(c.in)}
			out, err := read(s)
			if err != nil || string(out) != c.out || s.removed != c.removed {
				t.Errorf("%s (%s): %q, %d removed, %v; want %q, %d", c.in, name, out, s.removed, err, c.out, c.removed)
				continue
			}
			var got any
			if err := json.Unmarshal(out, &got); (err == nil) != c.valid {
				t.Errorf("%s (%s): decodes %v, want valid %v", c.in, name, err, c.valid)
			} else if c.valid && !reflect.DeepEqual(got, want) {
				t.Errorf("%s (%s): decodes to %#v, want %#v", c.in, name, got, want)
			}
		}
	}
	// A NUL between the halves of a surrogate pair joins them: the pair, not two U+FFFD.
	out, _ := io.ReadAll(&nulStripper{r: strings.NewReader(`"\ud83d\u0000\ude00"`)})
	if string(out) != `"\ud83d\ude00"` {
		t.Errorf("surrogates around a NUL: %q", out)
	}
	if _, err := (&nulStripper{r: strings.NewReader(`"\u00`)}).Read(make([]byte, 3)); err != nil {
		t.Errorf("a short first read: %v", err)
	}
}

// POST /v1/sync's decode: every "\u0000" goes, keys included (a key then names
// its field, or is unknown: 400), the rest decodes and the request counts once;
// an escaped backslash stays; raw 0x00 is still a 400 and an oversized body a 413.
func TestReadJSONStripNUL(t *testing.T) {
	var got nulPayload
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) {
		got = nulPayload{}
		if err := ReadJSONStripNUL(w, r, &got, 256); err != nil {
			WriteDecodeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := chain(WithRoutes(mux))
	stripped := metrics.NULStripped.WithLabelValues("POST /sync")
	before := testutil.ToFloat64(stripped)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader(body)))
		return rec.Code
	}
	if code := post(`{"note\u0000": "Tims\u0000 Watch", "sets": [{"tags": {"k\u0000": "v\u0000"}}]}`); code != 204 ||
		got.Note != "Tims Watch" || got.Sets[0].Tags["k"] != "v" {
		t.Fatalf("stripped: %d %+v", code, got)
	}
	if code := post(`{"note": "C:\\u0000"}`); code != 204 || got.Note != `C:\u0000` {
		t.Fatalf("escaped backslash: %d %+v", code, got)
	}
	for body, want := range map[string]int{
		`{"hrv\u0000": 61.7}`:                                400,
		"{\"note\": \"Tims\x00\"}":                           400,
		`{"note": "\u0000` + strings.Repeat("x", 300) + `"}`: 413,
	} {
		if code := post(body); code != want {
			t.Errorf("%.30s: %d, want %d", body, code, want)
		}
	}
	if n := testutil.ToFloat64(stripped) - before; n != 1 {
		t.Errorf("one accepted body had NULs removed (refused ones do not count), counted %v", n)
	}
}

// A NUL or bytes that are not UTF-8 anywhere in the path or the query (keys
// too) are a 400 before any handler runs; text that only looks like it is fine.
func TestWithRoutesRefusesNonTextURLs(t *testing.T) {
	served := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		served++
		WriteJSON(w, 200, map[string]string{"id": r.PathValue("id"), "q": r.URL.Query().Get("q")})
	})
	h := chain(WithRoutes(mux))
	refused := metrics.TextRefused.WithLabelValues("GET /v1/things/{id}", "url")
	before := testutil.ToFloat64(refused)
	for target, want := range map[string]int{
		"/v1/things/a%00b":                 400,
		"/v1/things/a?q=x%00":              400,
		"/v1/things/a?%00=1":               400,
		"/v1/things/a?q=ok&q=%00":          400,
		"/v1/things/a?q=%FF":               400,
		"/v1/things/%C3%28":                400,
		"/v1/things/caf%C3%A9?q=%E2%82%AC": 200,
		"/v1/things/a%2500?q=%5Cu0000":     200,
		"/v1/things/a?q=%25FF":             200,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != want {
			t.Errorf("%s: %d %s, want %d", target, rec.Code, rec.Body, want)
			continue
		}
		if body := decodeBody(t, rec); want == 400 && (body["error"] != msgURLText || body["request_id"] == "") {
			t.Errorf("%s: body %v", target, body)
		}
	}
	if served != 3 {
		t.Errorf("the handler ran %d times, want 3", served)
	}
	if n := testutil.ToFloat64(refused) - before; n != 6 {
		t.Errorf("six url refusals, counted %v", n)
	}
}

// sqlStateError is what *pgconn.PgError looks like to httpx.
type sqlStateError string

func (e sqlStateError) Error() string    { return "ERROR: refused (SQLSTATE " + string(e) + ")" }
func (e sqlStateError) SQLState() string { return string(e) }

// Postgres refusing a NUL or non-UTF-8 text (22021) or "\u0000" in jsonb
// (22P05), however wrapped, is the client's 422, counted and not logged; other
// database errors stay a logged 500.
func TestFailAnswersRefusedText422(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	refused := metrics.TextRefused.WithLabelValues("unmatched", "database")
	before := testutil.ToFloat64(refused)
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("samples: batch item 0: %w", sqlStateError("22021")), 422},
		{fmt.Errorf("feedback: %w", sqlStateError("22P05")), 422},
		{fmt.Errorf("samples: %w", sqlStateError("23514")), 500},
		{errors.New("invalid byte sequence for encoding \"UTF8\": 0x00 (SQLSTATE 22021)"), 500},
	} {
		logs.Reset()
		rec := httptest.NewRecorder()
		chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Fail(w, r, c.err) })).
			ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
		body := decodeBody(t, rec)
		switch {
		case rec.Code != c.want:
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.want)
		case c.want == 422 && (body["error"] != msgDBText || logs.Len() != 0):
			t.Errorf("%v: body %v, logged %q", c.err, body, logs.String())
		case c.want == 500 && (body["error"] != "internal error" || !strings.Contains(logs.String(), `"msg":"request failed"`)):
			t.Errorf("%v: body %v, logged %q", c.err, body, logs.String())
		}
	}
	if n := testutil.ToFloat64(refused) - before; n != 2 {
		t.Errorf("two database refusals, counted %v", n)
	}
}

// A NUL in a header never reaches a handler: net/http answers the request with
// its own plain-text 400 (API.md → Errors). A header with bytes that are not
// UTF-8 does arrive, but no header reaches Postgres as text (the device id is
// parsed as a UUID).
func TestServerRefusesNULInHeaders(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})))
	t.Cleanup(srv.Close)
	for header, want := range map[string]int{
		"X-RYFT-Device-ID: a\x00b": 400,
		"Idempotency-Key: k\x00":   400,
		"X-RYFT-Device-ID: a\xffb": 204,
	} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: ryft\r\n"+header+"\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode != want {
			t.Errorf("%q: %d, want %d", header, resp.StatusCode, want)
		}
	}
	if n := served.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1", n)
	}
}
