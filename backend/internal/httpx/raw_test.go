package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/model"
)

type rawPayload struct {
	Note  string                     `json:"note"`
	Diag  json.RawMessage            `json:"diagnostics"`
	Ptr   *json.RawMessage           `json:"ptr"`
	List  []json.RawMessage          `json:"list"`
	ByKey map[string]json.RawMessage `json:"by_key"`
	Inner struct {
		Raw json.RawMessage `json:"raw"`
	} `json:"inner"`
}

// Every json.RawMessage, wherever it sits, comes out as encoding/json writes
// what it decodes: bad UTF-8 and lone surrogates as U+FFFD (keys too), valid
// text and numbers as they were; a number beyond float64 is a RawJSONError
// naming its field. Fields that hold none are left alone.
func TestNormalizeRaw(t *testing.T) {
	body := "{\"note\": \"ok\", \"diagnostics\": {\"a\": \"x\xffy\", \"k\xfe\": [\"\\ud800\", \"\\ud83d\\ude00\", 61.7, null]}," +
		" \"ptr\": \"\\udc00\", \"list\": [1, \"\xc3\x28\"], \"by_key\": {\"m\": {\"n\": \"\xed\xa0\x80\"}}, \"inner\": {\"raw\": true}}"
	var p rawPayload
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if err := normalizeRaw(&p); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]json.RawMessage{"diagnostics": p.Diag, "ptr": *p.Ptr, "list[1]": p.List[1],
		"by_key": p.ByKey["m"], "inner": p.Inner.Raw} {
		if !utf8.Valid(raw) || !json.Valid(raw) || strings.Contains(string(raw), `\ud`) {
			t.Errorf("%s: %q is not clean text", name, raw)
		}
	}
	var diag map[string]any
	if err := json.Unmarshal(p.Diag, &diag); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "x\uFFFDy", "k\uFFFD": []any{"\uFFFD", "😀", 61.7, nil}}
	if !reflect.DeepEqual(diag, want) || string(*p.Ptr) != `"`+"\uFFFD"+`"` || string(p.Inner.Raw) != "true" {
		t.Errorf("re-encoded: %#v %s %s", diag, *p.Ptr, p.Inner.Raw)
	}

	for field, body := range map[string]string{
		"diagnostics": `{"diagnostics": {"a": 1e1000000}}`,
		"list[]":      `{"list": [-1e400]}`,
		"by_key{}":    `{"by_key": {"m": [1e309]}}`,
		"inner.raw":   `{"inner": {"raw": 2e308}}`,
		"ptr":         `{"ptr": 9e999}`,
	} {
		var p rawPayload
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		var re *RawJSONError
		if err := normalizeRaw(&p); !errors.As(err, &re) || re.Field != field || !errors.Is(err, errNumberRange) {
			t.Errorf("%s: want a RawJSONError for %s, got %v", body, field, err)
		}
	}
	for _, c := range []string{`{"diagnostics": null}`, `{"diagnostics": 1e-400}`, `{"note": "x"}`} {
		var p rawPayload
		if err := json.Unmarshal([]byte(c), &p); err != nil {
			t.Fatal(err)
		}
		if err := normalizeRaw(&p); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
}

// Which request models hold raw JSON: the feedback's diagnostics; never the
// sync, whose page must not be refused for a value it carries (D118).
func TestRequestModelsWithRawJSON(t *testing.T) {
	for typ, want := range map[reflect.Type]bool{
		reflect.TypeFor[model.FeedbackPost]():      true,
		reflect.TypeFor[model.SyncRequest]():       false,
		reflect.TypeFor[model.Session]():           false,
		reflect.TypeFor[model.Plan]():              false,
		reflect.TypeFor[model.NotificationPatch](): false,
	} {
		if got := holdsRaw(reflect.PointerTo(typ), map[reflect.Type]bool{}); got != want {
			t.Errorf("%v holds raw JSON: %v, want %v", typ, got, want)
		}
	}
}

// Through ReadJSON: a raw field with bad UTF-8 or a lone surrogate is stored
// repaired (204), one with a number out of range is a 422 naming the field.
func TestReadJSONReencodesRawFields(t *testing.T) {
	var got rawPayload
	mux := http.NewServeMux()
	mux.HandleFunc("POST /x", func(w http.ResponseWriter, r *http.Request) {
		got = rawPayload{}
		if err := ReadJSON(w, r, &got, 1024); err != nil {
			WriteDecodeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := chain(WithRoutes(mux))
	for body, want := range map[string]int{
		"{\"diagnostics\": {\"note\": \"woke\xff\"}}": 204,
		`{"diagnostics": {"note": "\ud800"}}`:         204,
		`{"diagnostics": {"hrv": 1e1000000}}`:         422,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("%q: %d %s, want %d", body, rec.Code, rec.Body, want)
			continue
		}
		if want == 204 && !strings.Contains(string(got.Diag), "\uFFFD") {
			t.Errorf("%q: stored %q", body, got.Diag)
		}
		if want == 422 {
			if b := decodeBody(t, rec); b["error"] != "diagnostics: a number is out of range" || b["request_id"] == "" {
				t.Errorf("%q: body %v", body, b)
			}
		}
	}
}
