package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// docs/API.md is the contract with the app. Every ```json block in it must
// either
//
//   - be tied to a type with an HTML comment on the line before the fence
//     (invisible when rendered):
//
//     <!-- type: model.SyncResponse -->        one value of that type
//     <!-- type: []model.Workout -->           a list of them
//     <!-- type: model.A, model.B*3 -->        several values in the block, in order (*3: three of B)
//     <!-- type: model.Session shape -->       "shape" for a block that is written
//     for people, not parsers (a "…" for the rest, a type name where a value
//     would be, placeholder ids): only its field names are checked
//
//   - or be listed in docExampleAllowList with the reason it cannot be checked.
//
// A tied block is checked against the Go type: a field the type lacks fails the
// test, and so does a value of the wrong type (strict blocks, decoded with
// DisallowUnknownFields). Failures name the line of docs/API.md. A block that
// is neither tied nor allow-listed fails too: a new example must say what it is.

const apiDocPath = "../../../docs/API.md"

// docExampleAllowList names blocks that have no Go type. The key is a substring
// of the block's first non-blank line (stable when lines move). Every entry
// says why.
// missingType, when set, is the documented type the code lacks: the entry fails
// as soon as contractTypes has it, so the block gets annotated instead.
var docExampleAllowList = []struct{ firstLine, reason, missingType string }{
	{`"error": "human-readable message"`, "error body: written by httpx, no model type", ""},
	{`"error": "strings must not contain the NUL character`, "error body: written by httpx, no model type", ""},
	{`"status": "deleted", "auth_user_deleted"`, "DELETE /v1/me response: an inline map in the handler, no model type", ""},
	{`"code": "c1b2`, "POST /v1/me/apple-authorization body: decoded into a handler-local struct, no model type", ""},
	{`"aps": { "alert"`, "APNs payload sent to Apple, not an API type", ""},
	{`"sleep_hr_minutes": 170`, "TODO(drift, api-contract-owner): documented type ScoreCoverage (DailyScore.coverage) does not exist in the code, see docDrift", "ScoreCoverage"},
}

// docDrift lists fields that docs/API.md documents and the Go type does not
// have (yet). It is NOT a fix: the api-contract-owner decides whether the code
// or the doc moves, and the additive-only rule applies. An entry that no longer
// matches anything fails the test, so it disappears with the drift.
var docDrift = []struct{ typ, field, todo string }{
	{"UserSource", "samples_7d", "TODO(drift, api-contract-owner): API.md documents User.sources[].samples_7d (since 2026-10-31); UserSource has no such field"},
	{"DailyScore", "coverage", "TODO(drift, api-contract-owner): API.md documents DailyScore.coverage (ScoreCoverage, since 2026-10-31); DailyScore has no such field"},
}

type docBlock struct {
	line       int // line of the opening fence, 1-based
	annotation string
	body       string
	bodyLine   int // line of the first body line
}

var annotationRe = regexp.MustCompile(`^<!--\s*type:\s*(.+?)\s*-->$`)

func readDocBlocks(t *testing.T) []docBlock {
	t.Helper()
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	var out []docBlock
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```json") {
			continue
		}
		b := docBlock{line: i + 1, bodyLine: i + 2}
		// the annotation is the nearest non-blank line above the fence
		for j := i - 1; j >= 0; j-- {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if m := annotationRe.FindStringSubmatch(strings.TrimSpace(lines[j])); m != nil {
				b.annotation = m[1]
			}
			break
		}
		var body []string
		for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
			body = append(body, lines[i])
		}
		b.body = strings.Join(body, "\n")
		out = append(out, b)
	}
	return out
}

func firstLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			return s
		}
	}
	return ""
}

func trunc(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// lineOfKey is the line of the first `"key"` in the block, else the fence line.
func (b docBlock) lineOfKey(key string) int {
	if i := strings.Index(b.body, strconv.Quote(key)); i >= 0 {
		return b.bodyLine + strings.Count(b.body[:i], "\n")
	}
	return b.line
}

var (
	strLitRe   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	elideCmaRe = regexp.MustCompile(`,\s*…`)
	elideCmbRe = regexp.MustCompile(`…\s*,`)
	typeNameRe = regexp.MustCompile(`\b[A-Z][A-Za-z]+\??`)
	stampKeyRe = regexp.MustCompile(`("(?:[a-z_]*(?:_at|start|end|wake|bed)|t)"\s*:\s*)"…"`)
	elidedStr  = regexp.MustCompile(`^"[0-9a-fA-F-]*…[0-9a-fA-F]*"$`) // "a7f2…", "…"
)

// Stand-ins for elided strings, so the value types are checked as well.
const (
	placeholderUUID  = "00000000-0000-4000-8000-000000000000"
	placeholderStamp = "2026-10-19T00:00:00Z"
)

// shapeToJSON turns the doc's notation into parseable JSON: "…" elisions go,
// and a bare type name (`SessionSummary`, `SleepDetail?`) becomes null. Strings
// are masked first so text inside them is untouched.
func shapeToJSON(s string) string {
	s = stampKeyRe.ReplaceAllString(s, `$1"`+placeholderStamp+`"`)
	var strs []string
	masked := strLitRe.ReplaceAllStringFunc(s, func(m string) string {
		if elidedStr.MatchString(m) {
			m = `"` + placeholderUUID + `"`
		}
		strs = append(strs, m)
		return `"@` + strconv.Itoa(len(strs)-1) + `@"`
	})
	masked = elideCmaRe.ReplaceAllString(masked, "")
	masked = elideCmbRe.ReplaceAllString(masked, "")
	masked = strings.ReplaceAll(masked, "…", "")
	masked = typeNameRe.ReplaceAllString(masked, "null")
	for i, m := range strs {
		masked = strings.Replace(masked, `"@`+strconv.Itoa(i)+`@"`, m, 1)
	}
	return masked
}

// jsonFields maps the JSON names of a struct to their Go types, as
// encoding/json sees them (embedded structs flattened, json:"-" skipped).
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key := strings.Split(tag, ",")[0]
		if f.Anonymous && key == "" && f.Type.Kind() == reflect.Struct {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if key == "" {
			key = f.Name
		}
		out[key] = f.Type
	}
	return out
}

type unknownField struct{ typ, field string }

// walkDoc compares a decoded example with the Go type. A field the type does
// not have is returned; one listed in docDrift is deleted from the example
// (so the strict decode that follows can still check everything else) and
// counted in seenDrift.
func walkDoc(v any, t reflect.Type, seenDrift map[int]bool) []unknownField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == optQuietType {
		t = reflect.TypeOf(QuietHours{})
	}
	var out []unknownField
	switch x := v.(type) {
	case map[string]any:
		switch {
		case t.Kind() == reflect.Struct && t.PkgPath() == reflect.TypeOf(Sample{}).PkgPath():
			fields := jsonFields(t)
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val := x[k]
				ft, ok := fields[k]
				if !ok {
					drifted := false
					for i, d := range docDrift {
						if d.typ == t.Name() && d.field == k {
							seenDrift[i], drifted = true, true
						}
					}
					if drifted {
						delete(x, k)
					} else {
						out = append(out, unknownField{t.Name(), k})
					}
					continue
				}
				out = append(out, walkDoc(val, ft, seenDrift)...)
			}
		case t.Kind() == reflect.Map:
			for _, val := range x {
				out = append(out, walkDoc(val, t.Elem(), seenDrift)...)
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for _, e := range x {
				out = append(out, walkDoc(e, t.Elem(), seenDrift)...)
			}
		}
	}
	return out
}

// expandRefs splits "model.A, model.B*3" into A, B, B, B.
func expandRefs(spec string) []string {
	var out []string
	for _, ref := range strings.Split(spec, ",") {
		ref = strings.TrimSpace(ref)
		n := 1
		if i := strings.LastIndex(ref, "*"); i >= 0 {
			n, _ = strconv.Atoi(ref[i+1:])
			ref = ref[:i]
		}
		for ; n > 0; n-- {
			out = append(out, ref)
		}
	}
	return out
}

func parseTypeRef(ref string) (reflect.Type, error) {
	slice := strings.HasPrefix(ref, "[]")
	name := strings.TrimPrefix(strings.TrimPrefix(ref, "[]"), "model.")
	typ, ok := contractTypes[name]
	if !ok {
		return nil, fmt.Errorf("unknown type %q (an annotation names an exported struct of this package: model.<Name>)", ref)
	}
	if slice {
		return reflect.SliceOf(typ), nil
	}
	return typ, nil
}

// checkTied checks one annotated block and returns its failures (each starting
// with the docs/API.md line) and the type names it covered.
func checkTied(b docBlock, seenDrift map[int]bool) (errs, covered []string) {
	spec := b.annotation
	isShape := strings.HasSuffix(spec, " shape")
	spec = strings.TrimSuffix(spec, " shape")
	body := stripLineComments(b.body)
	if isShape {
		body = shapeToJSON(body)
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	fail := func(line int, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("docs/API.md:%d: ", line)+fmt.Sprintf(format, args...))
	}
	for _, ref := range expandRefs(spec) {
		typ, err := parseTypeRef(ref)
		if err != nil {
			fail(b.line, "%v", err)
			return
		}
		var generic any
		if err := dec.Decode(&generic); err != nil {
			fail(b.line, "example is not valid JSON for %s (%d value(s) read before): %v", ref, len(covered), err)
			return
		}
		covered = append(covered, strings.TrimPrefix(ref, "[]"))
		unknown := walkDoc(generic, typ, seenDrift)
		for _, u := range unknown {
			fail(b.lineOfKey(u.field), "field %q of the example is not in model.%s (the doc is ahead of the code, or the field was removed: contract drift, the api-contract-owner decides; allow-list it in docDrift with a TODO until then)", u.field, u.typ)
		}
		if len(unknown) > 0 {
			continue // the strict decode below would only repeat it
		}
		clean, _ := json.Marshal(generic) // known-drift fields already removed
		sd := json.NewDecoder(bytes.NewReader(clean))
		sd.DisallowUnknownFields()
		if err := sd.Decode(reflect.New(typ).Interface()); err != nil {
			line := b.line
			var ute *json.UnmarshalTypeError
			if errors.As(err, &ute) {
				parts := strings.Split(ute.Field, ".")
				line = b.lineOfKey(parts[len(parts)-1])
			}
			fail(line, "example does not decode into %s: %v", ref, err)
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		fail(b.line, "the block holds more JSON values than its annotation names (%s)", b.annotation)
	}
	return
}

// stripLineComments blanks whole-line // comments (jsonc blocks), keeping the
// line count so reported lines stay right.
func stripLineComments(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func TestAPIDocExamples(t *testing.T) {
	blocks := readDocBlocks(t)
	if len(blocks) < 30 {
		t.Fatalf("found only %d json blocks in %s; the extractor is broken", len(blocks), apiDocPath)
	}
	usedAllow := make([]bool, len(docExampleAllowList))
	seenDrift := map[int]bool{}
	strict, shape := 0, 0
	types := map[string]bool{}

	for _, b := range blocks {
		if b.annotation == "" {
			allowed := false
			for i, a := range docExampleAllowList {
				if strings.Contains(firstLine(b.body), a.firstLine) {
					usedAllow[i], allowed = true, true
				}
			}
			if !allowed {
				t.Errorf("docs/API.md:%d: json block starting %q is not tied to a type: put `<!-- type: model.X -->` on the line before the fence, or allow-list it in docExampleAllowList with a reason", b.line, trunc(firstLine(b.body)))
			}
			continue
		}

		errs, refs := checkTied(b, seenDrift)
		for _, e := range errs {
			t.Error(e)
		}
		for _, r := range refs {
			types[r] = true
		}
		if len(errs) == 0 {
			if strings.HasSuffix(b.annotation, " shape") {
				shape++
			} else {
				strict++
			}
		}
	}
	for _, a := range docExampleAllowList {
		if _, ok := contractTypes[a.missingType]; a.missingType != "" && ok {
			t.Errorf("allow-list entry %q waits for model.%s, which exists now: annotate the block with `<!-- type: model.%s -->` and remove the entry", a.firstLine, a.missingType, a.missingType)
		}
	}
	for i, u := range usedAllow {
		if !u {
			t.Errorf("docExampleAllowList[%d] (%q) matches no json block any more: remove it", i, docExampleAllowList[i].firstLine)
		}
	}
	for i, d := range docDrift {
		if !seenDrift[i] {
			t.Errorf("docDrift[%d] (%s.%s) no longer occurs in docs/API.md or the code has the field now: remove it", i, d.typ, d.field)
		}
	}
	t.Logf("%d json blocks: %d decoded strictly, %d field-checked (shape), %d allow-listed; %d distinct types; %d known drifts",
		len(blocks), strict, shape, len(docExampleAllowList), len(types), len(docDrift))
}

// TestShapeToJSON pins the notation rules of "shape" blocks.
func TestShapeToJSON(t *testing.T) {
	cases := map[string]string{
		`{ "a": [ {"x":1}, … ] }`:                         `{ "a": [ {"x":1} ] }`,
		`{ "details": { … }, "b": 2 }`:                    `{ "details": {  }, "b": 2 }`,
		`{ "s": [ SessionSummary, … ], "d": DayStrain? }`: `{ "s": [ null ], "d": null }`,
		`{ "t": "keep Type … text" }`:                     `{ "t": "keep Type … text" }`,
	}
	for in, want := range cases {
		if got := shapeToJSON(in); got != want {
			t.Errorf("shapeToJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDocCheckerCatchesMistakes is the mutation test of the checker itself.
func TestDocCheckerCatchesMistakes(t *testing.T) {
	cases := []struct {
		name, annotation, body, want string
	}{
		{"wrong value type in a shape block", "model.DailyScore shape", `{ "day": "2026-10-18", "recovery": "82", "details": { … } }`, "recovery"},
		{"unknown field in a shape block", "model.DailyScore shape", `{ "day": "2026-10-18", "recoveryy": 82 }`, "recoveryy"},
		{"wrong value type, strict", "model.Consent", `{ "policy_version": 7 }`, "does not decode"},
		{"unknown field, strict", "model.Consent", `{ "policy_version": "x", "nope": 1 }`, "nope"},
		{"unknown nested field", "model.NotificationPrefs", `{ "categories": {}, "quiet_hours": { "from": "22:00", "to": "07:00", "x": 1 } }`, `"x"`},
		{"extra value", "model.Consent", `{ "policy_version": "x" } { "policy_version": "y" }`, "more JSON values"},
		{"unknown type", "model.Nope", `{}`, "unknown type"},
	}
	for _, c := range cases {
		errs, _ := checkTied(docBlock{line: 1, bodyLine: 2, annotation: c.annotation, body: c.body}, map[int]bool{})
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%s: want a failure mentioning %q, got %v", c.name, c.want, errs)
		}
	}
	// and a comment line in a block is not a second value
	errs, _ := checkTied(docBlock{line: 1, bodyLine: 2, annotation: "model.Consent", body: "// note\n{ \"policy_version\": \"x\" }"}, map[int]bool{})
	if len(errs) != 0 {
		t.Errorf("comment line should be ignored: %v", errs)
	}
}
