// Package logcheck is the test-side half of the rule "no health values in
// logs" (D114, docs/OPERATIONS.md → Data protection (logs)). A service's log
// test lists, per message, the keys the line may carry and the kind of value
// each key holds; Check reports any other message, key or value shape.
//
// A kind is a type and, where cheap, a shape: a UUID, a YYYY-MM-DD day, a
// member of a closed enum, a non-negative integer, a boolean. A recovery score
// logged under a count key (a float, or a number out of the count's bound) or
// under a renamed key fails. What a kind cannot do: tell an integer health
// value from an integer count under the same count key; only a bound helps
// there, and only where one is known. Text (an error's cause) is the other
// gap: the tests' scans for the request's own values cover it.
//
// Production code must not import this package; it exists for tests.
package logcheck

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kind checks the value of one key as encoding/json decodes it (float64,
// string, bool).
type Kind func(v any) error

// UUID is a canonical UUID string (user and request ids, devices).
func UUID(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("want a UUID string, have %T", v)
	}
	if id, err := uuid.Parse(s); err != nil || id.String() != s {
		return fmt.Errorf("%q is not a canonical UUID", s)
	}
	return nil
}

// Day is a calendar day, YYYY-MM-DD.
func Day(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("want a YYYY-MM-DD string, have %T", v)
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return fmt.Errorf("%q is not a YYYY-MM-DD day", s)
	}
	return nil
}

// Time is the RFC 3339 timestamp slog writes.
func Time(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("want an RFC 3339 string, have %T", v)
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return fmt.Errorf("%q is not an RFC 3339 time", s)
	}
	return nil
}

// Bool is a flag.
func Bool(v any) error {
	if _, ok := v.(bool); !ok {
		return fmt.Errorf("want a boolean, have %T (%v)", v, v)
	}
	return nil
}

// Enum is one of a closed set of strings.
func Enum(members ...string) Kind {
	return func(v any) error {
		s, ok := v.(string)
		if !ok || !slices.Contains(members, s) {
			return fmt.Errorf("%v is not one of %v", v, members)
		}
		return nil
	}
}

// EnumList is a comma-joined list of members of a closed set (the gate header).
func EnumList(members ...string) Kind {
	one := Enum(members...)
	return func(v any) error {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("want a string, have %T", v)
		}
		for _, part := range strings.Split(s, ",") {
			if err := one(part); err != nil {
				return err
			}
		}
		return nil
	}
}

// Pattern is a string matching re.
func Pattern(re *regexp.Regexp) Kind {
	return func(v any) error {
		if s, ok := v.(string); !ok || !re.MatchString(s) {
			return fmt.Errorf("%v does not match %s", v, re)
		}
		return nil
	}
}

// Count is a non-negative integer.
func Count(v any) error { return CountMax(math.MaxInt32)(v) }

// CountMax is a Count of at most max, for a count whose bound is known.
func CountMax(max int) Kind {
	return func(v any) error {
		f, ok := v.(float64)
		if !ok {
			return fmt.Errorf("want a non-negative integer, have %T (%v)", v, v)
		}
		if f != math.Trunc(f) || f < 0 || f > float64(max) {
			return fmt.Errorf("%v is not an integer in 0..%d", f, max)
		}
		return nil
	}
}

// IntBetween is an integer in lo..hi (an HTTP status).
func IntBetween(lo, hi int) Kind {
	return func(v any) error {
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
			return fmt.Errorf("%v is not an integer in %d..%d", v, lo, hi)
		}
		return nil
	}
}

// Text is free text of at most max bytes: an error's cause. It is the one kind
// that cannot rule a health value out.
func Text(max int) Kind {
	return func(v any) error {
		if s, ok := v.(string); !ok || len(s) > max {
			return fmt.Errorf("want a string of at most %d bytes, have %T", max, v)
		}
		return nil
	}
}

// Keys maps a key to the kind of its value.
type Keys map[string]Kind

// Lines maps a message to the keys its line may carry.
type Lines map[string]Keys

var (
	clientRequest = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	routePattern  = regexp.MustCompile(`^(unmatched|([A-Z]+ )?/[A-Za-z0-9/_{}.$-]*)$`)
	pathPattern   = regexp.MustCompile(`^/[A-Za-z0-9/_.-]*$`)
)

// Standard returns the keys on every line: logx's and the request's. service
// is the one the log belongs to.
func Standard(service string) Keys {
	return Keys{
		"time": Time, "level": Enum("DEBUG", "INFO", "WARN", "ERROR"), "service": Enum(service),
		"msg":        Text(120),
		"request_id": UUID, "user_id": UUID,
		"client_request_id": Pattern(clientRequest),
		"route":             Pattern(routePattern),
	}
}

// HTTP returns the keys of httpx.Instrument's access line. codes are the error
// codes (and auth reasons) the test's requests can reach.
func HTTP(codes ...string) Keys {
	return Keys{
		"method": Enum("GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"),
		"path":   Pattern(pathPattern), "status": IntBetween(100, 599),
		"bytes": Count, "dur_ms": Count, "code": Enum(codes...), "gate": EnumList(codes...),
	}
}

// RequestFailed returns the keys of httpx.Fail's line.
func RequestFailed() Keys {
	return Keys{"status": IntBetween(500, 599), "err": Text(1024)}
}

// Check parses out (one JSON object per line) and returns the lines by
// message, without their time (07:38:47.3 is a time, not a heart rate), and
// every violation: a message not in lines, a key neither listed for it nor
// standard, a value of the wrong kind or shape. The caller scans the lines for
// the request's own values on top.
func Check(out string, standard Keys, lines Lines) (byMsg map[string][]map[string]any, problems []string) {
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
		for k, v := range rec {
			kind, ok := keys[k]
			if !ok {
				kind, ok = standard[k]
			}
			if !ok {
				problems = append(problems, fmt.Sprintf("%q carries %s, which it may not: %s", msg, k, line))
				continue
			}
			if err := kind(v); err != nil {
				problems = append(problems, fmt.Sprintf("%q: %s: %v: %s", msg, k, err, line))
			}
		}
		delete(rec, "time")
		byMsg[msg] = append(byMsg[msg], rec)
	}
	return byMsg, problems
}
