package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/metrics"
)

// started is what the start records, in order: Load, the pool, the db watch, the
// broker, /metrics, Build, the relay, the timers, the consumers and last HTTP.
var started = []string{"load", "pool", "db watch", "bus", "metrics", "build", "outbox relay", "job", "consumer", "http"}

// stopped is the shutdown's order (the relay and the db watch stop together).
var stopped = []string{"job stopped", "consumer stopped", "http closed", "relay stopped", "bus closed", "pool closed", "metrics closed"}

func fullParts(g *rig) Parts {
	return Parts{
		Jobs:      []Task{{Name: "job", Run: until(g.rec, "job stopped")}},
		Consumers: []Task{{Name: "consumer", Run: until(g.rec, "consumer stopped")}},
	}
}

func TestStartOrder(t *testing.T) {
	g := newRig()
	g.parts = fullParts(g)
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, g.spec())
	g.rec.waitLen(t, len(started))
	if got := g.rec.list(); !slices.Equal(got, started) {
		t.Errorf("start order\n got %v\nwant %v", got, started)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestShutdownOrder(t *testing.T) {
	g := newRig()
	g.parts = fullParts(g)
	s := g.spec()
	s.DrainDelay = 30 * time.Millisecond // the job's goroutine records before the next step starts
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	g.rec.waitLen(t, len(started))
	cancel()
	if err := result(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := g.rec.shutdown(len(started)); !slices.Equal(got, stopped) {
		t.Errorf("shutdown order\n got %v\nwant %v", got, stopped)
	}
	steps := []string{}
	for _, m := range g.logs.find("shutdown step") {
		steps = append(steps, m["step"].(string))
		if _, ok := m["dur_ms"].(float64); !ok || m["err"] != nil {
			t.Errorf("step line %v: want dur_ms and no err", m)
		}
	}
	if want := []string{"drain", "consumers", "http", "jobs", "relay", "broker", "pool"}; !slices.Equal(steps, want) {
		t.Errorf("steps %v, want %v", steps, want)
	}
	if m := g.logs.find("shutting down"); len(m) != 1 || m[0]["reason"] != "signal" {
		t.Errorf("shutting down: %v", m)
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(0) {
		t.Errorf("shut down: %v", m)
	}
}

// The signal ends Run's wait, not the tasks' contexts: a delivery in hand keeps its
// context until its own step (A4 gives it the drain on top).
func TestTaskContextsOutliveTheSignal(t *testing.T) {
	g := newRig()
	var mu sync.Mutex
	var buildCtx, consumerCtx context.Context
	g.build = func(ctx context.Context) { mu.Lock(); buildCtx = ctx; mu.Unlock() }
	g.parts.Consumers = []Task{{Name: "consumer", Run: func(ctx context.Context) error {
		mu.Lock()
		consumerCtx = ctx
		mu.Unlock()
		<-ctx.Done()
		return nil
	}}}
	s := g.spec()
	s.DrainDelay = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	g.rec.waitLen(t, 9)
	cancel()
	eventually(t, func() bool { return len(g.logs.find("shutting down")) == 1 }, "the shutdown")
	mu.Lock()
	if buildCtx.Err() != nil || consumerCtx.Err() != nil {
		t.Errorf("a context ended at the signal: build %v, consumer %v", buildCtx.Err(), consumerCtx.Err())
	}
	mu.Unlock()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if buildCtx.Err() == nil {
		t.Error("Build's context outlived the shutdown")
	}
}

func TestLoadErrorConnectsNothing(t *testing.T) {
	boom := errors.New("CONSENT_MODE must be off or on")
	for _, tc := range []struct {
		name    string
		load    error
		cfg     bool
		wantMsg string
	}{
		{"ConfigError names its message", ConfigError{Msg: "beta config", Err: boom}, false, "beta config"},
		{"other errors are config", boom, false, "config"},
		{"the common settings", nil, true, "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.loadErr = tc.load
			if tc.cfg {
				g.e.loadConfig = func(string) (config.Config, error) { return config.Config{}, boom }
			}
			err := run(context.Background(), g.spec(), g.e)
			if !errors.Is(err, boom) {
				t.Fatalf("Run = %v, want the load error", err)
			}
			if got := g.rec.list(); tc.cfg && len(got) != 0 || !tc.cfg && !slices.Equal(got, []string{"load"}) {
				t.Errorf("connected before the settings were valid: %v", got)
			}
			m := g.logs.find(tc.wantMsg)
			if len(m) != 1 || m[0]["err"] != boom.Error() {
				t.Errorf("log %q = %v", tc.wantMsg, m)
			}
			if len(g.logs.find("shut down")) != 1 {
				t.Error("no shut down line")
			}
		})
	}
}

func TestRunNeedsANameAndABuild(t *testing.T) {
	if err := Run(context.Background(), Spec{Name: "x"}); err == nil {
		t.Error("a Spec without Build ran")
	}
}

// consumerThatFails returns err after the test closes trigger, or ends with ctx.
func consumerThatFails(r *rec, trigger <-chan struct{}, err error) Task {
	return Task{Name: "consumer", Run: func(ctx context.Context) error {
		select {
		case <-trigger:
			r.add("consumer failed")
			return err
		case <-ctx.Done():
			r.add("consumer stopped")
			return nil
		}
	}}
}

func TestTaskErrorStopsInOrder(t *testing.T) {
	boom := errors.New("broker gone for good")
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"an error", boom, "broker gone for good"},
		{"a return before the shutdown", nil, "returned before the shutdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			trigger := make(chan struct{})
			g.parts = Parts{
				Jobs:      []Task{{Name: "job", Run: until(g.rec, "job stopped")}},
				Consumers: []Task{consumerThatFails(g.rec, trigger, tc.err)},
			}
			s := g.spec()
			s.DrainDelay = 30 * time.Millisecond
			done := g.serve(t, context.Background(), s) // no signal: the task ends the service
			g.rec.waitLen(t, len(started))
			close(trigger)
			err := result(t, done)
			if err == nil || !strings.Contains(err.Error(), tc.want) || tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("Run = %v, want %q", err, tc.want)
			}
			want := append([]string{"consumer failed"}, stopped...)
			want = slices.DeleteFunc(want, func(s string) bool { return s == "consumer stopped" })
			if got := g.rec.shutdown(len(started)); !slices.Equal(got, want) {
				t.Errorf("shutdown order\n got %v\nwant %v", got, want)
			}
			if m := g.logs.find("consume"); len(m) != 1 || m[0]["task"] != "consumer" {
				t.Errorf("consume line: %v", m)
			}
			if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(1) {
				t.Errorf("shut down: %v", m)
			}
		})
	}
}

func TestHTTPOverrunClosesAndContinues(t *testing.T) {
	g := newRig()
	entered, release := make(chan struct{}), make(chan struct{})
	g.parts = Parts{
		Consumers: []Task{{Name: "consumer", Run: until(g.rec, "consumer stopped")}},
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /v1/slow", func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
			})
		},
	}
	s := g.spec()
	s.ShutdownTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the request the shutdown cannot wait for
		defer wg.Done()
		c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
		if resp, err := c.Get("http://" + g.addr("http") + "/v1/slow"); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	err := result(t, done)
	close(release)
	wg.Wait()
	if err == nil || !strings.Contains(err.Error(), "http:") {
		t.Fatalf("Run = %v, want the http step's error", err)
	}
	list := g.rec.list()
	for _, step := range []string{"relay stopped", "bus closed", "pool closed", "metrics closed"} {
		if !slices.Contains(list, step) {
			t.Errorf("%q did not run after the overrun: %v", step, list)
		}
	}
	m := g.logs.find("shutdown step")
	var httpStep map[string]any
	for _, l := range m {
		if l["step"] == "http" {
			httpStep = l
		}
	}
	if httpStep == nil || httpStep["err"] == nil {
		t.Errorf("the http step is logged without its error: %v", m)
	}
	if e := g.logs.find("shut down"); len(e) != 1 || e[0]["exit"] != float64(1) {
		t.Errorf("shut down: %v", e)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	boomTask := func(name string) Task {
		return Task{Name: name, Run: func(context.Context) error { panic("kaboom " + name) }}
	}
	for _, tc := range []struct {
		name  string
		parts func(g *rig) Parts
	}{
		{"a timer", func(g *rig) Parts { return Parts{Jobs: []Task{boomTask("timer")}} }},
		{"a consumer", func(g *rig) Parts { return Parts{Consumers: []Task{boomTask("timer")}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.parts = tc.parts(g)
			before := testutil.ToFloat64(metrics.GoroutinePanics)
			err := result(t, g.run(context.Background(), g.spec())) // no signal: the panic ends it
			if err == nil || !strings.Contains(err.Error(), "kaboom") {
				t.Fatalf("Run = %v, want the panic as an error", err)
			}
			if got := testutil.ToFloat64(metrics.GoroutinePanics) - before; got != 1 {
				t.Errorf("ryft_goroutine_panics_total rose by %v, want 1", got)
			}
			m := g.logs.find("panic")
			if len(m) != 1 || m[0]["task"] != "timer" || m[0]["stack"] == "" {
				t.Errorf("panic line: %v", m)
			}
			if list := g.rec.list(); !slices.Contains(list, "pool closed") || !slices.Contains(list, "bus closed") {
				t.Errorf("no ordered teardown after the panic: %v", list)
			}
			if e := g.logs.find("shut down"); len(e) != 1 || e[0]["exit"] != float64(1) {
				t.Errorf("shut down: %v", e)
			}
		})
	}
}

func TestDependencyDownAtStart(t *testing.T) {
	down := errors.New("connection refused")
	for _, tc := range []struct {
		name string
		set  func(g *rig)
		log  string
		want []string // the records; the relay and the db watch count as one
	}{
		{"database", func(g *rig) { g.poolErr = down }, "db",
			[]string{"load", "pool"}},
		{"broker", func(g *rig) { g.busErr = down }, "events",
			[]string{"load", "pool", "db watch", "bus", "relay stopped", "pool closed"}},
		{"build", func(g *rig) { g.buildErr = down }, "build",
			[]string{"load", "pool", "db watch", "bus", "metrics", "build", "relay stopped", "bus closed", "pool closed", "metrics closed"}},
		{"http port", func(g *rig) {
			g.httpErr = down
			g.parts.Consumers = []Task{{Name: "consumer", Run: until(g.rec, "consumer stopped")}}
		}, "serve",
			[]string{"load", "pool", "db watch", "bus", "metrics", "build", "outbox relay", "consumer",
				"consumer stopped", "relay stopped", "bus closed", "pool closed", "metrics closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			tc.set(g)
			err := run(context.Background(), g.spec(), g.e)
			if !errors.Is(err, down) {
				t.Fatalf("Run = %v, want the dependency's error", err)
			}
			if got := g.rec.shutdown(0); !slices.Equal(got, tc.want) {
				t.Errorf("records\n got %v\nwant %v", got, tc.want)
			}
			if m := g.logs.find(tc.log); len(m) != 1 || m[0]["err"] != down.Error() {
				t.Errorf("log %q = %v", tc.log, m)
			}
			if g.addr("http") != "" {
				t.Error("the HTTP port opened")
			}
		})
	}
}

// The pool wait (db.Open retries for up to 10 minutes) holds the start: no port is
// open, so the start-up probe is what ends a pod that never gets a database.
func TestDatabaseWaitOpensNoPort(t *testing.T) {
	g := newRig()
	g.poolBlocks = true
	ctx, cancel := context.WithCancel(context.Background())
	done := g.run(ctx, g.spec())
	g.rec.waitLen(t, 2)
	time.Sleep(50 * time.Millisecond)
	if g.addr("http") != "" || g.addr("metrics") != "" {
		t.Error("a port opened during the database wait")
	}
	cancel()
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the cancelled wait", err)
	}
	if got := g.rec.list(); !slices.Equal(got, []string{"load", "pool"}) {
		t.Errorf("records %v", got)
	}
}

func TestSignalStartsTheShutdown(t *testing.T) {
	g := newRig()
	g.parts = fullParts(g)
	code := make(chan int, 1)
	go func() { code <- runSignals(g.spec(), g.e, syscall.SIGUSR1) }()
	eventually(t, func() bool { return g.addr("http") != "" }, "the listener")
	g.rec.waitLen(t, len(started))
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("exit code %d after a signal, want 0", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the signal did not stop the service")
	}
	if got := g.rec.shutdown(len(started)); len(got) == 0 || got[len(got)-1] != "metrics closed" {
		t.Errorf("shutdown did not run to its end: %v", got)
	}
	if m := g.logs.find("shutting down"); len(m) != 1 || m[0]["reason"] != "signal" {
		t.Errorf("shutting down: %v", m)
	}
}

func TestFailureExitsOne(t *testing.T) {
	g := newRig()
	g.buildErr = errors.New("no")
	if c := runSignals(g.spec(), g.e, syscall.SIGUSR1); c != 1 {
		t.Errorf("exit code %d, want 1", c)
	}
}

func TestTierDropsErrorsAfterStop(t *testing.T) {
	failed := make(chan string, 4)
	tr := newTier(context.Background(), slog.New(slog.DiscardHandler), "job", func(task string, _ error) { failed <- task })
	tr.run("late", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	tr.run("early", func(context.Context) error { return errors.New("x") })
	if got := <-failed; got != "early" {
		t.Fatalf("failed %q, want early", got)
	}
	if err := tr.stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if len(failed) != 0 {
		t.Error("a task winding down is not a failure")
	}
}

func TestWithinGivesUp(t *testing.T) {
	release := make(chan struct{})
	err := within(20*time.Millisecond, func() error { <-release; return nil })
	close(release)
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Errorf("within = %v", err)
	}
}
