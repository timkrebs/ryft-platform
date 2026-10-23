package logcheck

import (
	"math"
	"strings"
	"testing"
)

func TestKinds(t *testing.T) {
	long := strings.Repeat("a", 11)
	for _, tc := range []struct {
		name string
		kind Kind
		ok   []any
		bad  []any
	}{
		{"Day", Day,
			[]any{"2024-02-29", "2026-10-23"},
			[]any{"2026-02-29", "2026-04-31", "2026-1-5", "2026-10-23T07:38:47Z", "20261023", " 2026-10-23", 20261023.0, nil}},
		{"UUID", UUID,
			[]any{"0190f1c2-7d3a-7b1e-8c2d-3f4a5b6c7d8e"},
			[]any{"0190F1C2-7D3A-7B1E-8C2D-3F4A5B6C7D8E", "{0190f1c2-7d3a-7b1e-8c2d-3f4a5b6c7d8e}",
				"urn:uuid:0190f1c2-7d3a-7b1e-8c2d-3f4a5b6c7d8e", "0190f1c27d3a7b1e8c2d3f4a5b6c7d8e", "u-81", 81.0}},
		{"Time", Time,
			[]any{"2026-10-23T07:38:47.3Z", "2026-10-23T07:38:47+02:00", "2026-10-23T07:38:47.123456789-05:30"},
			[]any{" 2026-10-23T07:38:47Z", "2026-10-23 07:38:47Z", "47.3", 47.3, "2026-10-23T07:38:47"}},
		{"Count", Count,
			[]any{0.0, 1.0, 400.0, float64(math.MaxInt32)},
			[]any{-1.0, 0.5, 61.7, float64(math.MaxInt32) + 1, math.Pow(2, 31), math.Inf(1), math.NaN(), "3", true, nil}},
		{"CountMax", CountMax(20),
			[]any{0.0, 20.0},
			[]any{21.0, -1.0, 2.5}},
		{"IntBetween", IntBetween(100, 599),
			[]any{100.0, 599.0},
			[]any{99.0, 600.0, 47.3, "200"}},
		{"Bool", Bool, []any{true, false}, []any{1.0, "true", nil}},
		{"Enum", Enum("a", "b"), []any{"a"}, []any{"c", "a,b", "", 1.0}},
		{"EnumList", EnumList("a", "b"), []any{"a", "a,b"}, []any{"a,c", "", 1.0}},
		{"Text", Text(10), []any{"", "ok"}, []any{long, 1.0, true}},
	} {
		for _, v := range tc.ok {
			if err := tc.kind(v); err != nil {
				t.Errorf("%s(%v) rejected: %v", tc.name, v, err)
			}
		}
		for _, v := range tc.bad {
			if err := tc.kind(v); err == nil {
				t.Errorf("%s(%v) accepted", tc.name, v)
			}
		}
	}
}

// A too-long string is not reported as a wrong type.
func TestTextMessagesSayWhatIsWrong(t *testing.T) {
	if err := Text(3)("abcd"); err == nil || strings.Contains(err.Error(), "have string") {
		t.Errorf("%v", err)
	}
	if err := Text(3)(1.0); err == nil || !strings.Contains(err.Error(), "float64") {
		t.Errorf("%v", err)
	}
}

// 1e400 is no float64: the line is not accepted as JSON.
func TestCheckRejectsWhatIsNotJSON(t *testing.T) {
	std, lines := Keys{"msg": Text(20)}, Lines{"m": {"n": Count}}
	_, problems := Check(`{"msg":"m","n":1e400}`, std, lines, Forbidden{})
	if len(problems) == 0 {
		t.Error("accepted")
	}
}

func TestCheckFindsDuplicateKeysAndForbiddenValues(t *testing.T) {
	std, lines := Standard("svc"), Lines{"m": {"n": Count, "tags": Text(20)}}
	forbid := Forbidden{Texts: []string{"secret"}, Numbers: []float64{61.7}}
	const head = `{"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"svc","msg":"m"`
	id := `"0190f1c2-7d3a-7b1e-8c2d-3f4a5b6c7d8e"`

	Accepts(t, std, lines, forbid, head+`,"n":3,"user_id":`+id+`}`)
	for _, tc := range []struct{ name, line, key string }{
		// the call site's value, then the context's: Unmarshal keeps the last
		{"user_id twice, the first a score", head + `,"user_id":61.7,"n":3,"user_id":` + id + `}`, Key("user_id")},
		{"request_id twice", head + `,"request_id":` + id + `,"request_id":` + id + `}`, Key("request_id")},
		{"n twice", head + `,"n":3,"n":4}`, Key("n")},
		{"forbidden number as a count", head + `,"n":61.7}`, Key("n")},
		{"text in a field", head + `,"tags":"a secret"}`, `holds "secret"`},
		{"unlisted key", head + `,"score":81}`, Key("score")},
	} {
		Rejects(t, std, lines, forbid, tc.name, tc.line, tc.key)
	}
	// the duplicate is named even where the surviving value is fine
	if _, problems := Check(head+`,"n":3,"n":4}`, std, lines, forbid); len(problems) != 1 || !strings.Contains(problems[0], "twice") {
		t.Errorf("%v", problems)
	}
	// the forbidden scan sees what the decoded map no longer holds
	if _, problems := Check(head+`,"user_id":61.7,"user_id":`+id+`}`, std, lines, forbid); !strings.Contains(strings.Join(problems, "\n"), "value 61.7") {
		t.Errorf("the hidden value is not found: %v", problems)
	}
	// time is not scanned: 47.3 in the stamp is a time, not a heart rate
	Accepts(t, std, lines, Forbidden{Texts: []string{"47.3"}}, head+`,"n":3}`)
}
