package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func TestFeedbackValidation(t *testing.T) {
	str := func(s string) *string { return &s }
	ok := func() model.FeedbackPost {
		return model.FeedbackPost{Message: strings.Repeat("ä", 4000), Category: str("bug"), AppVersion: str(strings.Repeat("x", 64)),
			RequestIDs: make([]string, 20), Diagnostics: json.RawMessage(`{"a": 1}`)}
	}
	f := ok()
	for k := range f.RequestIDs {
		f.RequestIDs[k] = "r"
	}
	if c, msg := feedbackError(&f); msg != "" || c != "bug" {
		t.Fatalf("at the limits (4000 characters, not bytes): %q", msg)
	}
	if c, msg := feedbackError(&model.FeedbackPost{Message: "hi", Diagnostics: json.RawMessage(`null`)}); msg != "" || c != "other" {
		t.Fatalf("category defaults to other, diagnostics null is none: %s %q", c, msg)
	}
	for name, f := range map[string]model.FeedbackPost{
		"empty":         {Message: "  \n"},
		"long":          {Message: strings.Repeat("a", 4001)},
		"category":      {Message: "x", Category: str("rant")},
		"build":         {Message: "x", Build: str(strings.Repeat("1", 65))},
		"ids":           {Message: "x", RequestIDs: make([]string, 21)},
		"empty id":      {Message: "x", RequestIDs: []string{""}},
		"diag array":    {Message: "x", Diagnostics: json.RawMessage(`[1]`)},
		"diag too long": {Message: "x", Diagnostics: json.RawMessage(`{"a":"` + strings.Repeat("b", 32<<10) + `"}`)},
	} {
		if _, msg := feedbackError(&f); msg == "" {
			t.Errorf("%s must be 422", name)
		}
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":                     now.Add(-7 * 24 * time.Hour),
		"2026-10-18":           time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
		"2026-10-19T08:00:00Z": time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
	} {
		if got, err := parseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("yesterday must be refused")
	}
}
