package pubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// tickBody is what Cloud Scheduler's tick looks like on a push subscription of ryft-tick.
func tickBody(attrs map[string]string) string {
	return pushBody("tick-api", []byte("tick"), attrs, nil)
}

func newTick(t *testing.T, o TickOptions, ticks ...Tick) http.Handler {
	t.Helper()
	if o.Verifier == nil {
		o.Verifier = fakeVerifier{}
	}
	if o.Log == nil {
		o.Log = quiet
	}
	if o.Budget == 0 {
		o.Budget = time.Minute
	}
	h, err := NewTick(o, ticks...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// steps records the order the tick ran its steps in.
type steps struct {
	mu  sync.Mutex
	ran []string
}

func (s *steps) step(name string, err error) Tick {
	return Tick{Name: name, Run: func(context.Context) error {
		s.mu.Lock()
		s.ran = append(s.ran, name)
		s.mu.Unlock()
		return err
	}}
}

func (s *steps) order() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.ran, " ")
}

func TestTickRunsTheStepsInOrder(t *testing.T) {
	const svc = "tick-order"
	var s steps
	w := httptest.NewRecorder()
	ok := rise(metrics.Ticks.WithLabelValues(svc, "ok"))
	var finally bool
	h := newTick(t, TickOptions{Service: svc, Finally: func(ctx context.Context) error {
		finally = true
		// The tick is counted, and not answered yet: the metrics push carries it.
		if ok() != 1 || s.order() != "ping sweep pass" || w.Code != http.StatusOK {
			t.Errorf("finally ran with ticks %v, steps %q, status %d", ok(), s.order(), w.Code)
		}
		if _, set := ctx.Deadline(); !set {
			t.Error("finally has no deadline")
		}
		return nil
	}}, s.step("ping", nil), s.step("sweep", nil), s.step("pass", nil))

	for _, attrs := range []map[string]string{nil, {AttrKey: KeyTick}} {
		w = httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, PathTick, strings.NewReader(tickBody(attrs)))
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || !finally {
			t.Fatalf("status %d, finally %v", w.Code, finally)
		}
		s.ran, finally = nil, false
		ok = rise(metrics.Ticks.WithLabelValues(svc, "ok"))
	}
}

// A failed step is logged, the next still runs, and the tick is answered 204 (a tick is
// not retried) and counted as error.
func TestTickStepFails(t *testing.T) {
	const svc = "tick-fails"
	var s steps
	failed := rise(metrics.Ticks.WithLabelValues(svc, "error"))
	ok := rise(metrics.Ticks.WithLabelValues(svc, "ok"))
	finallyRan := false
	h := newTick(t, TickOptions{Service: svc, Finally: func(context.Context) error { finallyRan = true; return errors.New("pushgateway: refused") }},
		s.step("ping", errors.New("db: unreachable")), s.step("sweep", nil))
	if w := serve(h, http.MethodPost, tickBody(nil), nil); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if s.order() != "ping sweep" || failed() != 1 || ok() != 0 || !finallyRan {
		t.Errorf("steps %q, error %v, ok %v, finally %v", s.order(), failed(), ok(), finallyRan)
	}
}

// A step that panics is recovered and counted as a panic of the tick route; the next
// step runs.
func TestTickStepPanics(t *testing.T) {
	const svc = "tick-panics"
	var s steps
	panics := rise(metrics.Panics.WithLabelValues(http.MethodPost + " " + PathTick))
	failed := rise(metrics.Ticks.WithLabelValues(svc, "error"))
	h := newTick(t, TickOptions{Service: svc},
		Tick{Name: "boom", Run: func(context.Context) error { panic("nil pointer") }}, s.step("after", nil))
	if w := serve(h, http.MethodPost, tickBody(nil), nil); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if s.order() != "after" || panics() != 1 || failed() != 1 {
		t.Errorf("steps %q, panics %v, error %v", s.order(), panics(), failed())
	}
}

// The budget bounds all steps together: a step sees its deadline, and a step that would
// begin after it is skipped.
func TestTickBudget(t *testing.T) {
	const svc = "tick-budget"
	const budget = 50 * time.Millisecond
	var s steps
	failed := rise(metrics.Ticks.WithLabelValues(svc, "error"))
	finallyErr := errors.New("unset")
	h := newTick(t, TickOptions{Service: svc, Budget: budget, Finally: func(ctx context.Context) error {
		finallyErr = ctx.Err() // its own budget: the steps' is spent
		return nil
	}},
		Tick{Name: "slow", Run: func(ctx context.Context) error {
			dl, ok := ctx.Deadline()
			if !ok || time.Until(dl) > budget {
				t.Errorf("deadline %v %v, want within %s", dl, ok, budget)
			}
			<-ctx.Done()
			return ctx.Err()
		}},
		s.step("skipped", nil))
	start := time.Now()
	if w := serve(h, http.MethodPost, tickBody(nil), nil); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
	if s.order() != "" || failed() != 1 || finallyErr != nil {
		t.Errorf("steps %q, error %v, finally's ctx %v", s.order(), failed(), finallyErr)
	}
}

// A request whose context is gone still counts its tick and runs Finally, on a context
// of its own.
func TestTickCanceledRequest(t *testing.T) {
	const svc = "tick-canceled"
	var s steps
	failed := rise(metrics.Ticks.WithLabelValues(svc, "error"))
	finallyErr := errors.New("unset")
	h := newTick(t, TickOptions{Service: svc, Finally: func(ctx context.Context) error { finallyErr = ctx.Err(); return nil }}, s.step("ping", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, PathTick, strings.NewReader(tickBody(nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || s.order() != "" || failed() != 1 || finallyErr != nil {
		t.Errorf("status %d, steps %q, error %v, finally's ctx %v", w.Code, s.order(), failed(), finallyErr)
	}
}

// The final step outlives the caller's cancel but not the server's hard stop (httpx.WithStop, the
// shutdown's http step running out): a push still running then lets go, so the shutdown's own push
// and the pool step keep their bounds.
func TestTickFinallyEndsAtTheHardStop(t *testing.T) {
	stop, hardStop := context.WithCancel(context.Background())
	defer hardStop()
	entered := make(chan struct{})
	var ended atomic.Value
	h := newTick(t, TickOptions{Service: "tick-hard-stop", Finally: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		ended.Store(ctx.Err())
		return ctx.Err()
	}})
	req, hangUp := context.WithCancel(httpx.WithStop(stop))
	defer hangUp()
	r := httptest.NewRequestWithContext(req, http.MethodPost, PathTick, strings.NewReader(tickBody(nil)))
	answered := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		answered <- w.Code
	}()
	<-entered
	hangUp() // the caller's cancel: the push goes on
	select {
	case code := <-answered:
		t.Fatalf("the tick answered %d when the caller hung up: the push was cut", code)
	case <-time.After(50 * time.Millisecond):
	}
	hardStop()
	select {
	case code := <-answered:
		if code != http.StatusNoContent {
			t.Errorf("status %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the final step outlived the server's hard stop (its budget is 10 s)")
	}
	if err, _ := ended.Load().(error); !errors.Is(err, context.Canceled) {
		t.Errorf("the final step's context ended with %v, want the hard stop's cancel", err)
	}
}

// A tick that arrives while one runs in this instance is answered at once and counted
// as overlap; the running one finishes, and the next runs again.
func TestTickOverlap(t *testing.T) {
	const svc = "tick-overlap"
	var runs atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	h := newTick(t, TickOptions{Service: svc}, Tick{Name: "slow", Run: func(context.Context) error {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}})
	overlap := rise(metrics.Ticks.WithLabelValues(svc, "overlap"))
	ok := rise(metrics.Ticks.WithLabelValues(svc, "ok"))

	done := make(chan int, 1)
	go func() { done <- serve(h, http.MethodPost, tickBody(nil), nil).Code }()
	<-started
	if w := serve(h, http.MethodPost, tickBody(nil), nil); w.Code != http.StatusNoContent {
		t.Errorf("the overlapping tick: %d", w.Code)
	}
	if overlap() != 1 || runs.Load() != 1 {
		t.Errorf("overlap %v, runs %d", overlap(), runs.Load())
	}
	close(release)
	if code := <-done; code != http.StatusNoContent {
		t.Errorf("the first tick: %d", code)
	}
	if w := serve(h, http.MethodPost, tickBody(nil), nil); w.Code != http.StatusNoContent || runs.Load() != 2 {
		t.Errorf("the next tick: %d, runs %d", w.Code, runs.Load())
	}
	if ok() != 2 || overlap() != 1 {
		t.Errorf("ok %v, overlap %v", ok(), overlap())
	}
}

// What is answered before any step runs; none of it is a tick, so none is counted.
func TestTickRefuses(t *testing.T) {
	const svc = "tick-refuses"
	cases := []struct {
		name     string
		method   string
		body     string
		verifier error
		want     int
	}{
		{name: "GET", method: http.MethodGet, body: tickBody(nil), want: 405},
		{name: "no token", body: tickBody(nil), verifier: auth.Unauthorized("missing_token", nil), want: 401},
		{name: "a stranger's token", body: tickBody(nil), verifier: auth.Unauthorized("subject", errors.New("not the push account")), want: 401},
		{name: "keys unavailable", body: tickBody(nil), verifier: auth.Unavailable("jwks_unavailable", nil), want: 503},
		{name: "another key", body: tickBody(map[string]string{AttrKey: "ingested"}), want: 400},
		{name: "an empty key", body: tickBody(map[string]string{AttrKey: ""}), want: 400},
		{name: "not JSON", body: "tick", want: 400},
		{name: "no messageId", body: `{"message":{"data":"e30=","attributes":{"ryft-key":"tick"}}}`, want: 400},
		{name: "messageId not decimal", body: `{"message":{"data":"e30=","messageId":"me@example.com"}}`, want: 400},
		{name: "too big", body: `{"message":{"data":"` + strings.Repeat("A", MaxPushBody) + `"}}`, want: 413},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s steps
			before := make([]func() float64, 0, 3)
			for _, result := range []string{"ok", "error", "overlap"} {
				before = append(before, rise(metrics.Ticks.WithLabelValues(svc, result)))
			}
			finally := false
			h := newTick(t, TickOptions{Service: svc, Verifier: fakeVerifier{c.verifier}, Finally: func(context.Context) error { finally = true; return nil }},
				s.step("ping", nil))
			method := c.method
			if method == "" {
				method = http.MethodPost
			}
			if w := serve(h, method, c.body, nil); w.Code != c.want {
				t.Fatalf("status %d, want %d", w.Code, c.want)
			}
			if s.order() != "" || finally {
				t.Errorf("steps %q ran, finally %v", s.order(), finally)
			}
			for _, counted := range before {
				if counted() != 0 {
					t.Error("counted as a tick")
				}
			}
		})
	}
}

// A ryft-key that is not "tick" is logged cut to maxLogKey bytes: the envelope is the
// pusher's.
func TestTickCutsTheKeyItLogs(t *testing.T) {
	var buf bytes.Buffer
	h := newTick(t, TickOptions{Service: "tick-cuts", Log: slog.New(slog.NewJSONHandler(&buf, nil))})
	if w := serve(h, http.MethodPost, tickBody(map[string]string{AttrKey: strings.Repeat("k", 5000)}), nil); w.Code != 400 {
		t.Fatalf("status %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["msg"] != "tick: not a tick" || got["key"] != strings.Repeat("k", maxLogKey) || got["message_id"] != "17012345678901234" {
		t.Fatalf("logged %v", got)
	}
}

func TestNewTickRefuses(t *testing.T) {
	run := func(context.Context) error { return nil }
	good := TickOptions{Service: "api", Verifier: fakeVerifier{}, Budget: time.Second, Log: quiet}
	broken := map[string]func(*TickOptions, *[]Tick){
		"no service":     func(o *TickOptions, _ *[]Tick) { o.Service = "" },
		"no logger":      func(o *TickOptions, _ *[]Tick) { o.Log = nil },
		"no verifier":    func(o *TickOptions, _ *[]Tick) { o.Verifier = nil },
		"no budget":      func(o *TickOptions, _ *[]Tick) { o.Budget = 0 },
		"a step no name": func(_ *TickOptions, ts *[]Tick) { *ts = append(*ts, Tick{Run: run}) },
		"a step no Run":  func(_ *TickOptions, ts *[]Tick) { *ts = append(*ts, Tick{Name: "ping"}) },
	}
	for name, br := range broken {
		o, ts := good, []Tick{{Name: "sweep", Run: run}}
		br(&o, &ts)
		if _, err := NewTick(o, ts...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ts := []Tick{{Name: "sweep", Run: run}}
	h, err := NewTick(good, ts...)
	if err != nil {
		t.Fatal(err)
	}
	// The handler keeps its own copy of the steps.
	ts[0] = Tick{Name: "changed", Run: func(context.Context) error { return errors.New("changed") }}
	if !slices.Equal(stepNames(h), []string{"sweep"}) {
		t.Errorf("steps %v", stepNames(h))
	}
}

func stepNames(h http.Handler) []string {
	var names []string
	for _, t := range h.(*tickHandler).ticks {
		names = append(names, t.Name)
	}
	return names
}
