package logx

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A type mismatch keeps the field and the type and drops the number it quoted;
// a body that is no object reads as one sentence; other errors pass unchanged.
func TestJSONErrorDropsTheValue(t *testing.T) {
	type day struct {
		Recovery int       `json:"recovery"`
		Zones    []float64 `json:"zone_minutes"`
	}
	// The field's spelling varies between Go releases (day.zone_minutes.0), so
	// the beginning, the field and the end are checked, not the whole sentence.
	for body, c := range map[string]struct{ start, field, end string }{
		`{"recovery": 61.7}`:                 {"json: cannot unmarshal number into ", "day.recovery", " of type int"},
		`{"recovery": 99999999999999999999}`: {"json: cannot unmarshal number into ", "day.recovery", " of type int"},
		`{"zone_minutes": [1e999]}`:          {"json: cannot unmarshal number into ", "day.zone_minutes", " of type float64"},
		`[61.7]`:                             {"json: cannot unmarshal array into ", "Go value", " of type logx.day"},
	} {
		var d day
		err := JSONError(json.Unmarshal([]byte(body), &d))
		if err == nil {
			t.Fatalf("%s: must not decode", body)
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, c.start) || !strings.Contains(msg, c.field) || !strings.HasSuffix(msg, c.end) ||
			strings.Contains(msg, "  ") || strings.Contains(msg, "61.7") || strings.Contains(msg, "999") {
			t.Errorf("%s: %q, want %q…%q…%q and no value", body, msg, c.start, c.field, c.end)
		}
	}
	syntax := json.Unmarshal([]byte(`{"recovery": 61.7`), &struct{}{})
	if got := JSONError(syntax); !errors.Is(got, syntax) || strings.Contains(got.Error(), "61.7") {
		t.Errorf("a syntax error passes as it is, and quotes no value: %v", got)
	}
	if JSONError(nil) != nil {
		t.Error("nil stays nil")
	}
}
