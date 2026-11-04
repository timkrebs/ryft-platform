package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
)

// maxReason is the longest reason /readyz shows for a failed check.
const maxReason = 120

// Check is one dependency /readyz reports by name.
type Check struct {
	Name     string // db | auth | broker: a closed set, the key in the body
	Required bool   // false: reported, never makes /readyz 503 (the broker in api and ingest, D101)
	Fn       func(context.Context) error
}

// Stale is the error a Check returns for a dependency that is failing but still
// served from what it has cached (JWKS keys past their refresh). It shows as
// "stale 3m" and counts as ok.
func Stale(age time.Duration) error { return staleError{age} }

type staleError struct{ age time.Duration }

func (e staleError) Error() string {
	switch {
	case e.age < time.Minute:
		return fmt.Sprintf("stale %ds", int(e.age.Seconds()))
	case e.age < time.Hour:
		return fmt.Sprintf("stale %dm", int(e.age.Minutes()))
	}
	return fmt.Sprintf("stale %dh", int(e.age.Hours()))
}

// readiness is the /readyz body: status is ready, unready or draining; error
// names the first failing required check (or draining); checks has one string per
// check, "ok", "stale 3m" or "down: <reason>".
type readiness struct {
	Status    string            `json:"status"`
	Error     string            `json:"error,omitempty"`
	Checks    map[string]string `json:"checks"`
	RequestID string            `json:"request_id,omitempty"`
}

// readyz answers 200 when every required check passes and 503 otherwise, and 503
// while draining. The status code is what probes read; the body is for people.
func readyz(checks []Check, draining *atomic.Bool, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := readiness{Status: "ready", Checks: make(map[string]string, len(checks)), RequestID: w.Header().Get(httpx.RequestIDHeader)}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		for i, err := range runChecks(ctx, checks, timeout) {
			c := checks[i]
			var stale staleError
			switch {
			case err == nil:
				body.Checks[c.Name] = "ok"
			case errors.As(err, &stale):
				body.Checks[c.Name] = stale.Error()
			default:
				why := reason(err)
				body.Checks[c.Name] = "down: " + why
				if c.Required && body.Error == "" {
					body.Status, body.Error = "unready", c.Name+": "+why
				}
			}
		}
		if draining.Load() {
			body.Status, body.Error = "draining", "draining"
		}
		code := http.StatusOK
		if body.Status != "ready" {
			code = http.StatusServiceUnavailable
		}
		httpx.WriteJSON(w, code, body)
	}
}

// runChecks runs every check at once and returns their errors in order. A check
// still running when ctx ends reads "timed out after <timeout>"; its goroutine ends
// with it, as the check was given the same ctx.
func runChecks(ctx context.Context, checks []Check, timeout time.Duration) []error {
	type result struct {
		i   int
		err error
	}
	ch := make(chan result, len(checks))
	for i, c := range checks {
		go func() { ch <- result{i, safeCheck(ctx, c.Fn)} }()
	}
	errs := make([]error, len(checks))
	got := make([]bool, len(checks))
	for range checks {
		select {
		case res := <-ch:
			errs[res.i], got[res.i] = res.err, true
		case <-ctx.Done():
			for i := range checks {
				if !got[i] {
					errs[i] = fmt.Errorf("timed out after %s", timeout)
				}
			}
			return errs
		}
	}
	return errs
}

func safeCheck(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return fn(ctx)
}

// reason is err on one line, cut to maxReason characters.
func reason(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(s); len(r) > maxReason {
		s = string(r[:maxReason]) + "..."
	}
	return s
}
