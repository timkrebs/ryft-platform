package logcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Forbidden is what a test's requests carried and no line may hold: texts as a
// substring of the raw line, numbers as any numeric value at any depth.
type Forbidden struct {
	Texts   []string
	Numbers []float64
}

var timeField = regexp.MustCompile(`"time":"[^"]*",?`)

// Check parses out (one JSON object per line) and returns the lines by
// message, without their time (07:38:47.3 is a time, not a heart rate), and
// every violation: a message not in lines, a key neither listed for it nor
// standard, a key twice on a line, a value of the wrong kind or shape, and
// anything in forbid. Every problem names its key as `key "name"`.
//
// A key twice matters: encoding/json keeps the last value, and logx appends
// request_id, user_id and the rest after a call site's own attributes, so a call
// site logging "user_id", 61.7 would be hidden by the context's. The forbidden
// scans run on the raw line, which still holds both.
func Check(out string, standard Keys, lines Lines, forbid Forbidden) (byMsg map[string][]map[string]any, problems []string) {
	byMsg = map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			problems = append(problems, fmt.Sprintf("not a JSON line: %q", line))
			continue
		}
		msg, _ := rec["msg"].(string)
		keys, known := lines[msg]
		if !known {
			problems = append(problems, fmt.Sprintf("a new log line %q: check it against the rule, then list its keys: %s", msg, line))
		}
		dups, nums := walk(line)
		for _, k := range dups {
			problems = append(problems, fmt.Sprintf("%q: key %q twice: %s", msg, k, line))
		}
		for _, f := range nums {
			if slices.Contains(forbid.Numbers, f) {
				problems = append(problems, fmt.Sprintf("%q: value %v of the request: %s", msg, f, line))
			}
		}
		rest := timeField.ReplaceAllString(line, "")
		for _, s := range forbid.Texts {
			if strings.Contains(rest, s) {
				problems = append(problems, fmt.Sprintf("%q: holds %q: %s", msg, s, line))
			}
		}
		for k, v := range rec {
			kind, ok := keys[k]
			if !ok {
				kind, ok = standard[k]
			}
			if !ok {
				problems = append(problems, fmt.Sprintf("%q: key %q may not be carried: %s", msg, k, line))
				continue
			}
			if err := kind(v); err != nil {
				problems = append(problems, fmt.Sprintf("%q: key %q: %v: %s", msg, k, err, line))
			}
		}
		delete(rec, "time")
		byMsg[msg] = append(byMsg[msg], rec)
	}
	return byMsg, problems
}

// walk returns the top-level keys of a JSON object line that occur more than
// once, and every number in it at any depth.
func walk(line string) (dups []string, nums []float64) {
	dec := json.NewDecoder(strings.NewReader(line))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return
		}
		k, _ := t.(string)
		if seen[k] && !slices.Contains(dups, k) {
			dups = append(dups, k)
		}
		seen[k] = true
		nums = value(dec, nums)
	}
	return
}

// value consumes one JSON value, collecting its numbers.
func value(dec *json.Decoder, nums []float64) []float64 {
	t, err := dec.Token()
	if err != nil && err != io.EOF {
		return nums
	}
	switch v := t.(type) {
	case float64:
		nums = append(nums, v)
	case json.Delim:
		for dec.More() {
			if v == '{' {
				_, _ = dec.Token() // the key
			}
			nums = value(dec, nums)
		}
		_, _ = dec.Token() // the closing delimiter
	}
	return nums
}

// Key is how a problem names a key; the other problems read "a new log line"
// (a message not listed), `holds "text"` and `value 61.7` (the forbidden ones).
func Key(name string) string { return fmt.Sprintf("key %q", name) }

// Rejects fails t unless Check reports a problem containing want for line. It
// is what the mutation tests use: a line rejected for another reason does not
// prove the key under test is pinned.
func Rejects(t *testing.T, standard Keys, lines Lines, forbid Forbidden, name, line, want string) {
	t.Helper()
	_, problems := Check(line, standard, lines, forbid)
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("%s: want a problem with %q, have %v for %s", name, want, problems, line)
}

// Accepts fails t if Check reports anything for line.
func Accepts(t *testing.T, standard Keys, lines Lines, forbid Forbidden, line string) {
	t.Helper()
	if _, problems := Check(line, standard, lines, forbid); len(problems) > 0 {
		t.Errorf("a real line is rejected: %s\n%v", line, problems)
	}
}
