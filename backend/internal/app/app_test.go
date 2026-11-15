package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/metrics"
)

// started is what the start records, in order: Load, the pool, the transport, /metrics,
// Build and last HTTP. Nothing else starts: no consumer, timer, relay or db watch (D219).
var started = []string{"load", "pool", "bus", "metrics", "build", "http"}

// stopped is the shutdown's order.
var stopped = []string{"http closed", "pool closed", "metrics closed"}

func TestStartOrder(t *testing.T) {
	g := newRig()
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
	s := g.spec()
	s.DrainDelay = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	g.rec.waitLen(t, len(started))
	cancel()
	if err := result(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := g.rec.after(len(started)); !slices.Equal(got, stopped) {
		t.Errorf("shutdown order\n got %v\nwant %v", got, stopped)
	}
	steps := []string{}
	for _, m := range g.logs.find("shutdown step") {
		steps = append(steps, m["step"].(string))
		if _, ok := m["dur_ms"].(float64); !ok || m["err"] != nil {
			t.Errorf("step line %v: want dur_ms and no err", m)
		}
	}
	if want := []string{"drain", "http", "pool"}; !slices.Equal(steps, want) {
		t.Errorf("steps %v, want %v", steps, want)
	}
	if m := g.logs.find("shutting down"); len(m) != 1 || m[0]["reason"] != "signal" || m[0]["err"] != nil || m[0]["task"] != nil {
		t.Errorf("shutting down: %v", m)
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(0) {
		t.Errorf("shut down: %v", m)
	}
	if got := g.exitCodes(); len(got) != 0 {
		t.Errorf("the watchdog fired on a clean shutdown: %v", got)
	}
}

// The signal ends Run's wait, not Build's context: what Build started (the transport's
// key refresher) lives through the drain delay and the http step, and ends before the
// pool closes.
func TestBuildContextOutlivesTheSignal(t *testing.T) {
	g := newRig()
	var mu sync.Mutex
	var buildCtx context.Context
	g.build = func(ctx context.Context) error { mu.Lock(); buildCtx = ctx; mu.Unlock(); return nil }
	s := g.spec()
	s.DrainDelay = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	cancel()
	eventually(t, func() bool { return len(g.logs.find("shutting down")) == 1 }, "the shutdown")
	mu.Lock()
	if buildCtx.Err() != nil {
		t.Errorf("Build's context ended at the signal: %v", buildCtx.Err())
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
			if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(1) {
				t.Errorf("shut down: %v, the process exits 1", m)
			}
		})
	}
}

func TestRunNeedsANameAndABuild(t *testing.T) {
	if err := Run(context.Background(), Spec{Name: "x"}); err == nil {
		t.Error("a Spec without Build ran")
	}
}

// A server that fails (its listener breaks) stops the service in order, as a failing
// task always did: logged under its tier, the cause in "shutting down", exit 1.
func TestServerFailureStopsInOrder(t *testing.T) {
	g := newRig()
	done := g.serve(t, context.Background(), g.spec()) // no signal: the failure ends the service
	g.rec.waitLen(t, len(started))
	close(g.httpFail)
	err := result(t, done)
	if !errors.Is(err, errListener) || !strings.Contains(err.Error(), "http: ") {
		t.Fatalf("Run = %v, want the listener's error under the task's name", err)
	}
	if got := g.rec.after(len(started)); !slices.Equal(got, stopped) {
		t.Errorf("shutdown order\n got %v\nwant %v", got, stopped)
	}
	if m := g.logs.find("serve"); len(m) != 1 || m[0]["task"] != "http" {
		t.Errorf("serve line: %v", m)
	}
	if m := g.logs.find("shutting down"); len(m) != 1 || m[0]["reason"] != "task" || m[0]["task"] != "http" || m[0]["err"] == nil {
		t.Errorf("shutting down: %v: reason is a closed set, task and err are their own keys", m)
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(1) {
		t.Errorf("shut down: %v", m)
	}
}

func TestHTTPOverrunClosesAndContinues(t *testing.T) {
	g := newRig()
	entered, release := make(chan struct{}), make(chan struct{})
	g.parts = Parts{
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
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	client := make(chan error, 1)
	go func() { // the request the shutdown cannot wait for
		c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
		resp, err := c.Get("http://" + g.addr("http") + "/v1/slow")
		if err == nil {
			resp.Body.Close()
		}
		client <- err
	}()
	<-entered
	cancel()
	err := result(t, done)
	// Close cut the connection: the client fails while the handler still runs.
	select {
	case cerr := <-client:
		if cerr == nil {
			t.Error("the request in flight finished without an error")
		}
	case <-time.After(2 * time.Second):
		t.Error("the request in flight is still open after the shutdown: the server was not closed")
	}
	free()
	if err == nil || !strings.Contains(err.Error(), "http:") {
		t.Fatalf("Run = %v, want the http step's error", err)
	}
	list := g.rec.list()
	for _, step := range []string{"pool closed", "metrics closed"} {
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

// A panic in a server's goroutine is recovered, counted and logged with its stack; the
// service shuts down in order and exits 2, as an unrecovered panic does.
func TestPanicIsRecovered(t *testing.T) {
	g := newRig()
	before := testutil.ToFloat64(metrics.GoroutinePanics)
	done := g.serve(t, context.Background(), g.spec())
	close(g.httpBoom)
	err := result(t, done) // no signal: the panic ends it
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("Run = %v, want the panic as an error", err)
	}
	if got := testutil.ToFloat64(metrics.GoroutinePanics) - before; got != 1 {
		t.Errorf("ryft_goroutine_panics_total rose by %v, want 1", got)
	}
	m := g.logs.find("panic")
	if len(m) != 1 || m[0]["task"] != "http" || m[0]["stack"] == "" {
		t.Errorf("panic line: %v", m)
	}
	if list := g.rec.list(); !slices.Contains(list, "pool closed") || !slices.Contains(list, "metrics closed") {
		t.Errorf("no ordered teardown after the panic: %v", list)
	}
	if e := g.logs.find("shut down"); len(e) != 1 || e[0]["exit"] != float64(2) {
		t.Errorf("shut down: %v: a panic exits 2, as an unrecovered one does", e)
	}
	if c := exitCode(err); c != 2 {
		t.Errorf("exitCode = %d, want 2", c)
	}
}

func TestDependencyDownAtStart(t *testing.T) {
	down := errors.New("connection refused")
	for _, tc := range []struct {
		name string
		set  func(g *rig)
		log  string
		want []string
	}{
		{"database", func(g *rig) { g.poolErr = down }, "db",
			[]string{"load", "pool"}},
		{"transport", func(g *rig) { g.busErr = down }, "events",
			[]string{"load", "pool", "bus", "pool closed"}},
		{"build", func(g *rig) { g.buildErr = down }, "build",
			[]string{"load", "pool", "bus", "metrics", "build", "pool closed", "metrics closed"}},
		{"http port", func(g *rig) { g.httpErr = down }, "serve",
			[]string{"load", "pool", "bus", "metrics", "build", "pool closed", "metrics closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			tc.set(g)
			err := run(context.Background(), g.spec(), g.e)
			if !errors.Is(err, down) {
				t.Fatalf("Run = %v, want the dependency's error", err)
			}
			if got := g.rec.list(); !slices.Equal(got, tc.want) {
				t.Errorf("records\n got %v\nwant %v", got, tc.want)
			}
			if m := g.logs.find(tc.log); len(m) != 1 || m[0]["err"] != down.Error() {
				t.Errorf("log %q = %v", tc.log, m)
			}
			if g.addr("http") != "" {
				t.Error("the HTTP port opened")
			}
			if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(1) {
				t.Errorf("shut down: %v, the process exits 1", m)
			}
			if m := g.logs.find("shutting down"); len(m) != 1 || m[0]["reason"] != "start" || m[0]["err"] != down.Error() {
				t.Errorf("shutting down: %v", m)
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
	if err := result(t, done); err != nil {
		t.Errorf("Run = %v, a signal during the wait is a clean stop", err)
	}
	if got := g.rec.list(); !slices.Equal(got, []string{"load", "pool"}) {
		t.Errorf("records %v", got)
	}
	if len(g.logs.find("db")) != 0 || len(g.logs.find("start interrupted by the signal")) != 1 {
		t.Errorf("the interrupted wait is logged as an error: %v", g.logs.lines())
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(0) {
		t.Errorf("shut down: %v", m)
	}
}

// Only a signal makes a cancelled wait a clean stop: the same error without one is
// a dependency failing.
func TestCanceledWithoutASignalIsAFailure(t *testing.T) {
	g := newRig()
	g.poolErr = context.Canceled
	err := run(context.Background(), g.spec(), g.e)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(1) || len(g.logs.find("db")) != 1 {
		t.Errorf("shut down: %v", m)
	}
}

func TestSignalStartsTheShutdown(t *testing.T) {
	g := newRig()
	code := make(chan int, 1)
	go func() { code <- runSignals(g.spec(), g.e, syscall.SIGUSR1) }()
	<-g.started
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
	if got := g.rec.after(len(started)); len(got) == 0 || got[len(got)-1] != "metrics closed" {
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
	tr := newTier(context.Background(), slog.New(slog.DiscardHandler), "serve", func(task string, _ error) { failed <- task })
	tr.run("late", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	tr.run("early", func(context.Context) error { return errors.New("x") })
	if got := <-failed; got != "early" {
		t.Fatalf("failed %q, want early", got)
	}
	if err := tr.stop(limit(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(failed) != 0 {
		t.Error("a task winding down is not a failure")
	}
}

func TestWithinGivesUp(t *testing.T) {
	release := make(chan struct{})
	err := within(limit(20*time.Millisecond), func() error { <-release; return nil })
	close(release)
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Errorf("within = %v", err)
	}
}

// The outbox app builds is the Spec's: none, the events' (relaying into the transport by
// key) or notify's wait box (relaying into the wait topic), always of the service's own
// publisher; Build gets it, and the tick sweeps it after the db ping.
func TestOutboxKinds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    OutboxKind
		outbox  string // the publisher and what it relays into, "" for none
		tickRan []string
	}{
		{"none", NoOutbox, "", []string{"db ping", "own"}},
		{"events", EventOutbox, "svc *eventbus.Transport", []string{"db ping", "outbox sweep", "own"}},
		{"wait box", WaitOutbox, "svc pubsub.fixedTopic", []string{"db ping", "outbox sweep", "own"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.outboxKind = tc.kind
			g.parts.Ticks = []eventbus.Tick{step(g.rec, "own", nil)}
			ctx, cancel := context.WithCancel(context.Background())
			done := g.serve(t, ctx, g.spec())
			if code := g.tick(t); code != http.StatusNoContent {
				t.Errorf("tick = %d", code)
			}
			cancel()
			if err := result(t, done); err != nil {
				t.Fatal(err)
			}
			g.mu.Lock()
			built := g.outboxes
			g.mu.Unlock()
			if want := []string{tc.outbox}; tc.outbox == "" && len(built) != 0 || tc.outbox != "" && !slices.Equal(built, want) {
				t.Errorf("outboxes built %v, want %q", built, tc.outbox)
			}
			if got := g.rec.after(len(started))[:len(tc.tickRan)]; !slices.Equal(got, tc.tickRan) {
				t.Errorf("the tick ran %v, want %v", got, tc.tickRan)
			}
		})
	}
}

// POST /internal/tick runs app's steps and then the service's, in order, all of them even
// when one fails, and answers 204 whatever they did; the tick is counted by its result.
func TestTickRunsTheStepsInOrder(t *testing.T) {
	boom := errors.New("pooler unreachable")
	for _, tc := range []struct {
		name    string
		pingErr error
		own     error
		result  string
	}{
		{"all ok", nil, nil, "ok"},
		{"the ping fails, the rest still runs", boom, nil, "error"},
		{"the service's step fails", nil, boom, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.pingErr = tc.pingErr
			g.parts.Ticks = []eventbus.Tick{step(g.rec, "scoring pass", tc.own), step(g.rec, "stale check", nil)}
			before := testutil.ToFloat64(metrics.Ticks.WithLabelValues("svc", tc.result))
			ctx, cancel := context.WithCancel(context.Background())
			done := g.serve(t, ctx, g.spec())
			if code := g.tick(t); code != http.StatusNoContent {
				t.Errorf("tick = %d, want 204 whatever the steps did", code)
			}
			cancel()
			if err := result(t, done); err != nil {
				t.Fatal(err)
			}
			want := []string{"db ping", "outbox sweep", "scoring pass", "stale check"}
			if got := g.rec.after(len(started))[:len(want)]; !slices.Equal(got, want) {
				t.Errorf("steps %v, want %v", got, want)
			}
			if got := testutil.ToFloat64(metrics.Ticks.WithLabelValues("svc", tc.result)) - before; got != 1 {
				t.Errorf("ryft_ticks_total{result=%q} rose by %v, want 1", tc.result, got)
			}
			if tc.result == "error" {
				if m := g.logs.find("tick: step failed"); len(m) != 1 || m[0]["err"] == nil {
					t.Errorf("the failed step is logged once with its error: %v", m)
				}
			}
		})
	}
}

// The steps share the Spec's budget: scoring's 240 s, 45 s by default.
func TestTickStepsGetTheBudget(t *testing.T) {
	for _, budget := range []time.Duration{0, 240 * time.Second} {
		g := newRig()
		var left time.Duration
		g.parts.Ticks = []eventbus.Tick{{Name: "scoring pass", Run: func(ctx context.Context) error {
			dl, _ := ctx.Deadline()
			left = time.Until(dl)
			return nil
		}}}
		s := g.spec()
		s.TickBudget = budget
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		g.tick(t)
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		want := orDefault(budget, 45*time.Second)
		if left <= want-5*time.Second || left > want {
			t.Errorf("TickBudget %v: the step had %v left, want about %v", budget, left, want)
		}
	}
}

// The push routes and the tick are mounted beside /v1, outside its middleware: a /v1 that
// refuses every request (its auth) does not reach them, and no Authorization is needed
// (the push verifier checks theirs; ALLOW_INSECURE_PUSH here).
func TestPushAndTickSitOutsideV1(t *testing.T) {
	g := newRig()
	g.parts = Parts{
		Push: []PushRoutes{pushRoute{rec: g.rec, path: "/push/ingested"}, pushRoute{rec: g.rec, path: "/push/session.completed"}},
		Routes: func(mux *http.ServeMux) {
			mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				g.rec.add("v1 auth")
				w.WriteHeader(http.StatusUnauthorized)
			}))
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, g.spec())
	for _, path := range []string{"/push/ingested", "/push/session.completed"} {
		if code, _, _ := g.do(t, http.MethodPost, path, "{}"); code != http.StatusNoContent {
			t.Errorf("POST %s = %d", path, code)
		}
	}
	if code := g.tick(t); code != http.StatusNoContent {
		t.Errorf("tick = %d", code)
	}
	if code, _, _ := g.do(t, http.MethodPost, "/push/scored", "{}"); code != http.StatusNotFound {
		t.Errorf("a push path the service does not consume = %d, want 404", code)
	}
	if code, _ := g.get(t, "/v1/me"); code != http.StatusUnauthorized {
		t.Errorf("/v1/me = %d, want the /v1 auth's 401", code)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	want := []string{"push /push/ingested", "push /push/session.completed", "db ping", "outbox sweep", "v1 auth"}
	if got := g.rec.after(len(started))[:len(want)]; !slices.Equal(got, want) {
		t.Errorf("only /v1/me went through the /v1 middleware\n got %v\nwant %v", got, want)
	}
}

// A stuck pool close costs its bound, is reported, and the shutdown goes on.
func TestStepOverrunsAreReportedAndSurvived(t *testing.T) {
	g := newRig()
	g.e.bounds.poolClose = 30 * time.Millisecond
	release := make(chan struct{})
	g.e.closePool = func(p *pgxpool.Pool) { g.rec.add("pool closed"); <-release; p.Close() }
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, g.spec())
	cancel()
	err := result(t, done)
	close(release)
	if err == nil || !strings.Contains(err.Error(), "pool: timed out after") {
		t.Fatalf("Run = %v, want the pool step's timeout", err)
	}
	if list := g.rec.list(); !slices.Contains(list, "metrics closed") {
		t.Errorf("the shutdown stopped at the overrun: %v", list)
	}
	if e := g.logs.find("shut down"); len(e) != 1 || e[0]["exit"] != float64(1) {
		t.Errorf("shut down: %v", e)
	}
}

func TestShutdownStaysInBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Spec
		want time.Duration // the table next to the constants
	}{
		{"an edge service", Spec{Edge: true}, 13 * time.Second},
		{"an edge service with the planned 5 s drain delay", Spec{Edge: true, DrainDelay: 5 * time.Second}, 18 * time.Second},
		{"scoring, notify", Spec{}, 4 * time.Second},
		// A tick in flight is cut by the http step, never waited for: its budget is no part of the shutdown's.
		{"scoring with its 240 s tick", Spec{TickBudget: 240 * time.Second}, 4 * time.Second},
		// What A2 sets: Cloud Run's 10 s between SIGTERM and SIGKILL. The push services fit as they are.
		{"scoring, notify in Cloud Run's 10 s", Spec{Grace: 10 * time.Second}, 4 * time.Second},
	} {
		got := tc.s.worstCase(defaultSeams().bounds) // what Run really uses
		if got != tc.want {
			t.Errorf("%s: worst case %v, the table says %v (update both)", tc.name, got, tc.want)
		}
		if got >= tc.s.watchdogAfter() {
			t.Errorf("%s: worst case %v reaches the watchdog at %v: no headroom", tc.name, got, tc.s.watchdogAfter())
		}
	}
}

// The watchdog fires four seconds before the kubelet's 30 s, pinned as literals.
func TestWatchdogTime(t *testing.T) {
	if got := (Spec{}).grace(); got != 30*time.Second {
		t.Errorf("default grace %v, the kubelet's is 30 s", got)
	}
	if got := (Spec{}).watchdogAfter(); got != 26*time.Second {
		t.Errorf("watchdog at %v, want 26 s", got)
	}
	if got := (Spec{Grace: 40 * time.Second}).watchdogAfter(); got != 36*time.Second {
		t.Errorf("watchdog at %v for a 40 s grace, want 36 s", got)
	}
	if got := (Spec{Grace: 3 * time.Second}).watchdogAfter(); got != 1500*time.Millisecond {
		t.Errorf("watchdog at %v for a grace shorter than its margin, want half of it", got)
	}
}

// stuckPool makes the pool close hang until the test frees the rig, with a bound long
// enough for the watchdog to come first.
func stuckPool(g *rig) {
	g.e.bounds.poolClose = 5 * time.Second
	g.e.closePool = func(p *pgxpool.Pool) { g.rec.add("pool closed"); <-g.release; p.Close() }
}

func TestWatchdogEndsAStuckShutdown(t *testing.T) {
	g := newRig()
	stuckPool(g)
	defer g.free()
	s := g.spec()
	s.Grace = watchdogMargin + 100*time.Millisecond // fires 100 ms in
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	cancel()
	eventually(t, func() bool { return len(g.exitCodes()) == 1 }, "the watchdog")
	if got := g.exitCodes(); got[0] != 1 {
		t.Errorf("watchdog exit %v, want 1", got)
	}
	m := g.logs.find("shutdown overdue")
	if len(m) != 1 || m[0]["step"] != "pool" || m[0]["elapsed_ms"] == nil {
		t.Errorf("shutdown overdue: %v", m)
	}
	g.free()
	if err := result(t, done); err != nil {
		t.Error(err)
	}
}

func TestBuildErrorsKeepTheirMessage(t *testing.T) {
	boom := errors.New("JWKS_URL: not a URL")
	for _, tc := range []struct {
		name string
		err  error
		msg  string
	}{
		{"ConfigError", ConfigError{Msg: "auth", Err: boom}, "auth"},
		{"any other error", boom, "build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.buildErr = tc.err
			if err := run(context.Background(), g.spec(), g.e); !errors.Is(err, boom) {
				t.Fatalf("Run = %v", err)
			}
			if m := g.logs.find(tc.msg); len(m) != 1 || m[0]["err"] != boom.Error() {
				t.Errorf("log %q = %v", tc.msg, m)
			}
			if tc.msg == "auth" && len(g.logs.find("build")) != 0 {
				t.Error("a second error line under build")
			}
		})
	}
}

// A tick step without a name is a service's bug: the start fails under "build".
func TestABadTickStepFailsTheStart(t *testing.T) {
	g := newRig()
	g.parts.Ticks = []eventbus.Tick{{Run: func(context.Context) error { return nil }}}
	if err := run(context.Background(), g.spec(), g.e); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("Run = %v", err)
	}
	if m := g.logs.find("build"); len(m) != 1 || g.addr("http") != "" {
		t.Errorf("build line %v, http port %q", m, g.addr("http"))
	}
}

// A signal while Build waits ends Build's context, so the start does not hold the
// process until the kubelet's SIGKILL.
func TestSignalDuringStartEndsBuild(t *testing.T) {
	g := newRig()
	g.build = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	done := g.run(ctx, g.spec())
	g.rec.waitLen(t, 5) // load, pool, bus, metrics, build
	cancel()
	begun := time.Now()
	err := result(t, done)
	if err != nil || time.Since(begun) > 2*time.Second {
		t.Errorf("Run = %v after %v, want a clean stop at once", err, time.Since(begun))
	}
	if m := g.logs.find("shut down"); len(m) != 1 || m[0]["exit"] != float64(0) || len(g.logs.find("build")) != 0 {
		t.Errorf("a signal during Build is a clean stop, not an error: %v", g.logs.lines())
	}
	if list := g.rec.list(); !slices.Contains(list, "pool closed") || !slices.Contains(list, "metrics closed") {
		t.Errorf("no teardown: %v", list)
	}
}

func TestLateTaskErrorIsLoggedNotFailed(t *testing.T) {
	logs := &syncBuf{}
	failed := make(chan string, 1)
	tr := newTier(context.Background(), slog.New(slog.NewJSONHandler(logs, nil)), "serve", func(task string, _ error) { failed <- task })
	tr.run("winding down", func(ctx context.Context) error { <-ctx.Done(); return errors.New("flush failed") })
	if err := tr.stop(limit(time.Second)); err != nil {
		t.Fatal(err)
	}
	m := logs.find("task ended with error during shutdown")
	if len(m) != 1 || m[0]["task"] != "winding down" || m[0]["err"] != "flush failed" || m[0]["level"] != "WARN" {
		t.Errorf("late error line: %v", m)
	}
	if len(failed) != 0 {
		t.Error("an error after stop is not a failure")
	}
}

func TestExitCodes(t *testing.T) {
	for err, want := range map[error]int{nil: 0, errors.New("x"): 1, fmt.Errorf("job: %w", &taskPanic{"boom"}): 2} {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
}

// Every bound is reached at once, with the bounds scaled down: the measured shutdown
// is the sum Spec.worstCase says, so no step waits longer than its bound says.
func TestEverythingStuckStaysInBudget(t *testing.T) {
	g := newRig()
	g.stuck = true
	defer g.free()
	const unit = 200 * time.Millisecond
	g.e.bounds = bounds{afterClose: unit, poolClose: unit}
	entered := make(chan struct{})
	g.parts = Parts{
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /v1/slow", func(http.ResponseWriter, *http.Request) { close(entered); <-g.release })
		},
	}
	s := g.spec()
	s.ShutdownTimeout = unit
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	go func() {
		c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
		if resp, err := c.Get("http://" + g.addr("http") + "/v1/slow"); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	begun := time.Now()
	cancel()
	err := result(t, done)
	took := time.Since(begun)
	g.free()
	// The bounds the shutdown waited are exactly the ones worstCase adds up (nothing
	// timing-dependent: a wrong bound is a wrong number here, and the type limit keeps a
	// bare duration out). Each step then reached its own bound, and none ran on for
	// seconds (the margin is for a loaded machine; a bypass of 4 s or more is caught).
	g.mu.Lock()
	waited := g.bounds
	g.mu.Unlock()
	wantBound := map[string]time.Duration{"http": unit, "http wait": unit, "pool": unit, "metrics": unit}
	for step, want := range wantBound {
		if got := waited[step]; len(got) != 1 || got[0] != want {
			t.Errorf("step %s waited %v, its bound is %v", step, got, want)
		}
	}
	if len(waited) != len(wantBound) {
		t.Errorf("the shutdown waited %v, want only %v", waited, wantBound)
	}
	reached := map[string]time.Duration{"http": 2 * unit, "pool": unit}
	for _, m := range g.logs.find("shutdown step") {
		step, _ := m["step"].(string)
		dur := time.Duration(m["dur_ms"].(float64)) * time.Millisecond
		if want, ok := reached[step]; ok && (dur < want-20*time.Millisecond || dur > want+3*time.Second) {
			t.Errorf("step %s took %v with everything stuck, its bound is %v", step, dur, want)
		}
		delete(reached, step)
	}
	for _, step := range []string{"http", "pool", "metrics"} {
		if err == nil || !strings.Contains(err.Error(), step+": ") {
			t.Errorf("no overrun reported for %s: %v", step, err)
		}
	}
	for step := range reached {
		t.Errorf("no step line for %s", step)
	}
	if want := s.worstCase(g.e.bounds); took < want-30*time.Millisecond || took > want+3*time.Second {
		t.Errorf("the shutdown took %v with everything stuck; the bounds add up to %v", took, want)
	}
}

func TestWatchdogStopsWithTheShutdown(t *testing.T) {
	g := newRig()
	s := g.spec()
	s.Grace = watchdogMargin + 150*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := g.exitCodes(); len(got) != 0 {
		t.Errorf("the watchdog fired after the shutdown was over: %v", got)
	}
}

// After a recovered panic the watchdog exits 2 too, as the shutdown would have.
func TestWatchdogExitsTwoAfterAPanic(t *testing.T) {
	g := newRig()
	stuckPool(g)
	defer g.free()
	s := g.spec()
	s.Grace = watchdogMargin + 100*time.Millisecond
	done := g.serve(t, context.Background(), s)
	close(g.httpBoom)
	eventually(t, func() bool { return len(g.exitCodes()) == 1 }, "the watchdog")
	if got := g.exitCodes(); got[0] != 2 {
		t.Errorf("watchdog exit %v, want 2", got)
	}
	g.free()
	if err := result(t, done); err == nil {
		t.Error("the panic is not in Run's result")
	}
}

// The server's write timeout covers the tick: scoring's 240 s tick is answered. The
// other services, and every read limit, keep httpx's.
func TestServerTimeoutsCoverTheTick(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, tc := range []struct {
		s    Spec
		want time.Duration
	}{
		{Spec{}, 60 * time.Second},
		{Spec{Edge: true}, 60 * time.Second},
		{Spec{TickBudget: 240 * time.Second}, 255 * time.Second},
	} {
		srv := server(tc.s, log, ":0", http.NotFoundHandler())
		if srv.WriteTimeout != tc.want {
			t.Errorf("TickBudget %v: write timeout %v, want %v", tc.s.TickBudget, srv.WriteTimeout, tc.want)
		}
		if srv.ReadTimeout != 60*time.Second || srv.ReadHeaderTimeout != 10*time.Second || srv.MaxHeaderBytes != 64<<10 {
			t.Errorf("the read limits are httpx's: %v %v %v", srv.ReadTimeout, srv.ReadHeaderTimeout, srv.MaxHeaderBytes)
		}
	}
}

// Why the write timeout above matters: past WriteTimeout net/http lets the handler run on
// (its context does not end) and cuts the answer, so the caller sees no status and a
// push or a tick is delivered again. Pinned here so the reason does not rot.
func TestWriteTimeoutCutsALateAnswer(t *testing.T) {
	ranOn := make(chan bool, 1)
	srv := &http.Server{WriteTimeout: 100 * time.Millisecond, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		time.Sleep(300 * time.Millisecond)
		ranOn <- r.Context().Err() == nil
		w.WriteHeader(http.StatusNoContent)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := c.Post("http://"+ln.Addr().String()+"/", "application/json", strings.NewReader("{}"))
	if err == nil {
		resp.Body.Close()
		t.Errorf("an answer written after WriteTimeout arrived: %d", resp.StatusCode)
	}
	if !<-ranOn {
		t.Error("the handler's context ended at WriteTimeout: the timeout then cuts the work, not only the answer")
	}
	_ = srv.Close()
	<-served
}
