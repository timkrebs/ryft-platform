package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
)

// The shutdown's budget. Every step is bounded by what is written here, the bounds
// live in one place (bounds), the bound of a step includes the waits after it, and a
// test (TestShutdownStaysInBudget) fails when the worst case reaches the watchdog's
// time, Spec.Grace (the kubelet's 30 s; A2 sets Cloud Run's 10 s) less watchdogMargin.
//
//	step     bound                                              Edge             push and tick (scoring, notify)
//	drain    Spec.DrainDelay (A7: 5 s)                          0 (5 s)          0
//	http     Shutdown (10 s; push, tick and health 1 s) + afterClose   11 s      2 s
//	pool     poolClose                                          1 s              1 s
//	metrics  afterClose                                         1 s              1 s
//	worst case                                                  13 s (18 s)      4 s
//
// Nothing else runs in the background to stop (D219): the consumers are push routes,
// the timers and the relay are tick steps and inline flushes, the db watch is the
// tick's ping, and Pub/Sub's REST publish holds no connection to close (D218). So a
// push or a tick still in flight when the http step's Shutdown ends is cut there and
// not waited for: Pub/Sub delivers the push again (D100), and the tick's steps see
// their context end (scoring's pass stops between users) and the next tick comes in
// 15 minutes. The tick's budget is no part of the shutdown's.
//
// A step that overruns is logged, recorded (exit 1) and the next one runs; the
// watchdog (26 s) ends the process whatever step is stuck.
const (
	defaultEdgeShutdown   = 10 * time.Second // HTTP Shutdown of a service with a public API: today's budget
	defaultHealthShutdown = time.Second      // of one that serves its pushes, its tick and health only
	defaultGrace          = 30 * time.Second
	defaultTickBudget     = 45 * time.Second // api, ingest and notify; scoring sets 240 s (D219)
	defaultReadyTimeout   = 2 * time.Second  // how long /readyz waits for its checks
	watchdogMargin        = 4 * time.Second  // the watchdog fires this long before Grace
	// tickAnswer is how long a tick may run past its budget before it has answered: the
	// final step's 10 s (the metrics push, A2) and 5 s to spare. The server's write
	// timeout is at least the budget plus this (see server).
	tickAnswer = 15 * time.Second
)

// bounds are the shutdown steps' own bounds, all in one place; the tests shorten them.
type bounds struct {
	afterClose time.Duration // waiting for Serve to return after Shutdown or Close; the /metrics stop
	poolClose  time.Duration
}

func defaultBounds() bounds {
	return bounds{afterClose: time.Second, poolClose: time.Second}
}

// OutboxKind is the outbox app builds for a service, hands to Build as Deps.Outbox and
// sweeps on every tick (D219).
type OutboxKind int

const (
	// NoOutbox is for a service that publishes nothing.
	NoOutbox OutboxKind = iota
	// EventOutbox relays the service's events by key into their topics (scoring, D101).
	EventOutbox
	// WaitOutbox is notify's wait box: its deferrals (outbox rows of publisher notify with
	// a not-before time) relay into ryft-notify-wait and come back on /push/wait (D219).
	WaitOutbox
)

// Spec describes one service. The zero durations take the defaults.
type Spec struct {
	Name   string     // api | ingest | scoring | notify: logger, config.Load, transport, tick label, outbox publisher
	Edge   bool       // Instrument, Recover, SecurityHeaders (api, ingest); else Recover only and a 1 s HTTP shutdown
	Outbox OutboxKind // which outbox app builds and sweeps (scoring EventOutbox, notify WaitOutbox)

	// Load reads the service's own settings before anything connects (D78): an error
	// ends the process at once, not behind the start-up retries. Wrap it in
	// ConfigError to choose the message it is logged under.
	Load func(log *slog.Logger) error
	// Build makes what the service runs on the opened pool and transport. Its ctx lasts
	// until the shutdown's http step is over (it ends before the pool and /metrics
	// close), not until the signal; a signal while Build runs ends it, so Build must
	// return when ctx does (one that does not ends at the kubelet's SIGKILL). An error is
	// logged under ConfigError's message when it is one, else as "build".
	Build func(ctx context.Context, d Deps) (Parts, error)

	DrainDelay      time.Duration // keep serving this long after the shutdown starts, for the edge to drop the pod (default none)
	ShutdownTimeout time.Duration // HTTP Shutdown (default 10 s for Edge, 1 s otherwise)
	TickBudget      time.Duration // POST /internal/tick's steps together (default 45 s; scoring 240 s, D219)
	Grace           time.Duration // the pod's terminationGracePeriodSeconds (default 30 s): the watchdog and the budget test use it
}

func (s Spec) shutdownTimeout() time.Duration {
	switch {
	case s.ShutdownTimeout > 0:
		return s.ShutdownTimeout
	case s.Edge:
		return defaultEdgeShutdown
	}
	return defaultHealthShutdown
}

func (s Spec) grace() time.Duration { return orDefault(s.Grace, defaultGrace) }

func (s Spec) tickBudget() time.Duration { return orDefault(s.TickBudget, defaultTickBudget) }

// watchdogAfter is when the watchdog fires: Grace less its margin (half of Grace
// for a Grace too short to have one).
func (s Spec) watchdogAfter() time.Duration {
	if at := s.grace() - watchdogMargin; at > 0 {
		return at
	}
	return s.grace() / 2
}

// worstCase is the sum of the shutdown's step bounds for this Spec and these bounds.
func (s Spec) worstCase(b bounds) time.Duration {
	return s.DrainDelay + s.shutdownTimeout() + b.afterClose + b.poolClose + b.afterClose
}

// Deps is what Build gets: the opened dependencies.
type Deps struct {
	Log    *slog.Logger
	Config config.Config
	Pool   *pgxpool.Pool
	// Bus is the service's Pub/Sub (D218): it publishes, and it builds the consumers'
	// push routes (Bus.Push) and notify's wait sender. app builds the tick with it.
	Bus    *eventbus.Transport
	Outbox *outbox.Outbox // per Spec.Outbox; nil for NoOutbox
}

// Parts is what a service runs: Build returns it.
type Parts struct {
	// Routes mounts /v1/...; nil for none. It must not register "/", /healthz, /readyz,
	// the tick or a push path: app mounts those.
	Routes func(mux *http.ServeMux)
	Checks []Check // besides the db, which app adds
	// Push are the consumers' push routes (Bus.Push: POST /push/<key>), mounted outside
	// any /v1 middleware: the push token is their auth (D218).
	Push []PushRoutes
	// Ticks are the service's own steps of POST /internal/tick, in order, after app's
	// db ping and outbox sweep (D219). A step must return when its ctx ends (the tick's
	// budget) and logs nothing it would not log as a timer: its error is the tick's to
	// log, and the next step runs.
	Ticks []eventbus.Tick
}

// PushRoutes is a consumer's push handling as Bus.Push builds it.
type PushRoutes interface {
	Register(mux *http.ServeMux)
}

// ConfigError is what Spec.Load and Spec.Build return to say which message their
// error is logged under ("beta config", "auth"); any other error is logged as
// "config" (Load) or "build" (Build).
type ConfigError struct {
	Msg string
	Err error
}

// Error is the message and the cause.
func (e ConfigError) Error() string { return e.Msg + ": " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e ConfigError) Unwrap() error { return e.Err }

// Run starts s and blocks until ctx ends or a server fails; then it shuts down in
// order. It logs why it failed and returns the error: a start-up error, the failed
// task's error, or the steps of a shutdown that overran. A signal during the start
// (the database wait, Build) is a clean stop: nil. Main turns the result into the exit
// code: 0, 1, or 2 when a task panicked (as an unrecovered panic exits).
func Run(ctx context.Context, s Spec) error { return run(ctx, s, defaultSeams()) }

// seams are the dependencies Run opens, so the tests can stand in for them.
type seams struct {
	logger       func(service string) *slog.Logger
	loadConfig   func(service string) (config.Config, error)
	openPool     func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error)
	ping         func(ctx context.Context, pool *pgxpool.Pool) error                 // /readyz's db check
	dbPing       func(ctx context.Context, pool *pgxpool.Pool, service string) error // the tick's first step (ryft_db_up)
	sweep        func(ctx context.Context, ob *outbox.Outbox) error                  // the tick's outbox step
	closePool    func(pool *pgxpool.Pool)
	openBus      func(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (*eventbus.Transport, error)
	newOutbox    func(pool *pgxpool.Pool, publisher string, to events.Sender, log *slog.Logger) *outbox.Outbox
	listen       func(ctx context.Context, addr string) (net.Listener, error)
	exit         func(code int)                     // the watchdog's way out
	launched     func(task string)                  // a test hook: every task as it starts
	startDone    func()                             // a test hook: the start is over and its watcher has ended
	serverExit   func(server string)                // a test hook: a server's Serve has returned
	bounded      func(step string, d time.Duration) // a test hook: every bound a step waits
	readyTimeout time.Duration
	bounds       bounds
}

func defaultSeams() seams {
	return seams{
		logger:     httpx.Logger,
		loadConfig: config.Load,
		openPool: func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
			return db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
		},
		ping: func(ctx context.Context, pool *pgxpool.Pool) error { return pool.Ping(ctx) },
		dbPing: func(ctx context.Context, pool *pgxpool.Pool, service string) error {
			return db.Ping(ctx, pool, service)
		},
		sweep:     func(ctx context.Context, ob *outbox.Outbox) error { return ob.Sweep(ctx) },
		closePool: (*pgxpool.Pool).Close,
		openBus:   eventbus.Open,
		newOutbox: outbox.New,
		listen: func(ctx context.Context, addr string) (net.Listener, error) {
			return (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		},
		exit:         os.Exit,
		readyTimeout: defaultReadyTimeout,
		bounds:       defaultBounds(),
	}
}

// service is one Run: what it has opened so far and the tiers it has started.
type service struct {
	s   Spec
	e   seams
	log *slog.Logger
	cfg config.Config

	life       context.Context // ends after the http step (or a signal during start): Build and the transport run on it
	endLife    context.CancelFunc
	cancelWait context.CancelFunc // wakes Run: the first failing task calls it
	draining   atomic.Bool
	current    atomic.Value // the shutdown step in progress, for the watchdog

	mu        sync.Mutex
	cause     error  // the first failure: a start error or a task's, wrapped with its name
	causeKind string // "start" or "task"; "" for a signal
	causeTask string
	causeErr  error // cause without the task's name
	panicErr  error // the first recovered panic
	failed    []error

	pool    *pgxpool.Pool
	bus     *eventbus.Transport
	metrics *http.Server
	web     *http.Server
	serving bool

	serve, scrape *tier // the listener; /metrics
}

func run(ctx context.Context, s Spec, e seams) error {
	if s.Name == "" || s.Build == nil {
		return errors.New("app: a Spec needs a Name and a Build")
	}
	r := &service{s: s, e: e, log: e.logger(s.Name)}
	r.life, r.endLife = context.WithCancel(context.WithoutCancel(ctx))
	var waitCtx context.Context
	waitCtx, r.cancelWait = context.WithCancel(ctx)
	defer r.cancelWait()

	// A signal while the start waits (the database, Build) ends the life context, so
	// Build is interrupted; once the start is over the life context is the shutdown's
	// again.
	started, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			r.endLife()
		case <-started:
		}
	}()
	err := r.start(ctx)
	close(started)
	<-watched
	if r.e.startDone != nil {
		r.e.startDone()
	}

	switch {
	case err != nil && r.interrupted(ctx, err):
		// a signal during the wait: stopping is what was asked, not a failure
	case err != nil:
		r.setCause("start", "", err)
	default:
		<-waitCtx.Done()
	}
	steps := r.shutdown()
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.cause, r.panicOutside(), steps)
}

// panicOutside is the recovered panic when it is not already the cause, so the
// exit code can tell it.
func (r *service) panicOutside() error {
	if r.panicErr != nil && !errors.Is(r.cause, r.panicErr) {
		return r.panicErr
	}
	return nil
}

// setCause records the first failure and wakes Run.
func (r *service) setCause(kind, task string, err error) {
	r.mu.Lock()
	var tp *taskPanic
	if errors.As(err, &tp) && r.panicErr == nil {
		r.panicErr = err
	}
	if r.cause == nil {
		r.cause, r.causeKind, r.causeTask, r.causeErr = err, kind, task, err
		if task != "" {
			r.cause = fmt.Errorf("%s: %w", task, err)
		}
	}
	r.mu.Unlock()
	r.cancelWait()
}

// stop is what a failing task calls.
func (r *service) stop(task string, err error) { r.setCause("task", task, err) }

func (r *service) tier(msg string) *tier {
	t := newTier(r.life, r.log, msg, r.stop)
	t.launch = r.e.launched
	return t
}

// interrupted is whether err is only the signal ending a wait of the start.
func (r *service) interrupted(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && ctx.Err() != nil
}

// logSetup logs an error of the start under its ConfigError message, or def; a
// signal that ended a wait is logged at info.
func (r *service) logSetup(ctx context.Context, def string, err error) {
	if r.interrupted(ctx, err) {
		r.log.Info("start interrupted by the signal", "step", def)
		return
	}
	msg, cause := def, err
	var ce ConfigError
	if errors.As(err, &ce) {
		msg, cause = ce.Msg, ce.Err
	}
	r.log.Error(msg, "err", cause)
}

// start opens everything in order: settings, pool, the transport and the outbox,
// /metrics, Build and last the HTTP listener (the health, tick and push routes), so
// the start-up probe covers the waits above.
func (r *service) start(ctx context.Context) (err error) {
	if r.cfg, err = r.e.loadConfig(r.s.Name); err != nil {
		r.logSetup(ctx, "config", err)
		return err
	}
	if r.s.Load != nil {
		if err = r.s.Load(r.log); err != nil {
			r.logSetup(ctx, "config", err)
			return err
		}
	}
	// db.Open retries until the role can log in and the migrate job has applied the
	// schema; a signal ends the wait.
	if r.pool, err = r.e.openPool(ctx, r.cfg, r.log); err != nil {
		r.pool = nil
		r.logSetup(ctx, "db", err)
		return err
	}
	// Opening the transport dials nothing (D218); its push verifier's key refresher
	// lives on the service's life, which ends after the http step.
	if r.bus, err = r.e.openBus(r.life, r.cfg, r.s.Name, r.log); err != nil {
		r.bus = nil
		r.logSetup(ctx, "events", err)
		return err
	}
	ob, err := r.openOutbox()
	if err != nil {
		r.logSetup(ctx, "events", err)
		return err
	}
	r.startMetrics(ctx)

	deps := Deps{Log: r.log, Config: r.cfg, Pool: r.pool, Bus: r.bus, Outbox: ob}
	parts, err := r.s.Build(r.life, deps)
	if err != nil {
		r.logSetup(ctx, "build", err)
		return err
	}
	return r.startHTTP(ctx, parts, ob)
}

// openOutbox is Spec.Outbox's outbox: the events' relays into the transport by key,
// notify's wait box into the wait topic.
func (r *service) openOutbox() (*outbox.Outbox, error) {
	switch r.s.Outbox {
	case EventOutbox:
		return r.e.newOutbox(r.pool, r.s.Name, r.bus, r.log), nil
	case WaitOutbox:
		wait, err := r.bus.WaitSender()
		if err != nil {
			return nil, err
		}
		return r.e.newOutbox(r.pool, r.s.Name, wait, r.log), nil
	}
	return nil, nil
}

// ticks are the tick's steps: the db ping (ryft_db_up), the outbox's sweep (what the
// inline flushes left, and notify's deferrals that came due), then the service's own
// (D219).
func (r *service) ticks(ob *outbox.Outbox, own []eventbus.Tick) []eventbus.Tick {
	steps := []eventbus.Tick{{Name: "db ping", Run: func(ctx context.Context) error { return r.e.dbPing(ctx, r.pool, r.s.Name) }}}
	if ob != nil {
		steps = append(steps, eventbus.Tick{Name: "outbox sweep", Run: func(ctx context.Context) error { return r.e.sweep(ctx, ob) }})
	}
	return append(steps, own...)
}

func (r *service) startHTTP(ctx context.Context, parts Parts, ob *outbox.Outbox) error {
	checks := append([]Check{{Name: "db", Required: true, Fn: func(ctx context.Context) error { return r.e.ping(ctx, r.pool) }}}, parts.Checks...)
	// No final step yet: A2 hands the tick the metrics push here.
	tick, err := r.bus.Tick(r.s.tickBudget(), nil, r.ticks(ob, parts.Ticks)...)
	if err != nil {
		r.logSetup(ctx, "build", err)
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", readyz(r.log, checks, &r.draining, r.e.readyTimeout))
	// The tick and the pushes sit outside any /v1 middleware: the push token is their
	// auth, checked by the transport's verifier on every one (D218).
	mux.Handle(http.MethodPost+" "+eventbus.PathTick, tick)
	for _, p := range parts.Push {
		p.Register(mux)
	}
	if parts.Routes != nil {
		parts.Routes(mux)
	}
	mux.HandleFunc("/", httpx.NotFound)
	handler := httpx.Recover(mux)
	if r.s.Edge {
		handler = httpx.Instrument(r.log, httpx.Recover(httpx.SecurityHeaders(mux)))
	}

	addr := ":" + r.cfg.Port
	ln, err := r.e.listen(ctx, addr)
	if err != nil {
		r.logSetup(ctx, "serve", err)
		return err
	}
	r.web = server(r.s, r.log, addr, handler)
	r.serve = r.tier("serve")
	r.serve.run("http", func(context.Context) error {
		err := r.web.Serve(ln)
		if r.e.serverExit != nil {
			r.e.serverExit("http")
		}
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	r.log.Info("listening", "addr", addr)
	r.serving = true
	return nil
}

// server is httpx.Server with a write timeout no shorter than the tick's budget plus
// tickAnswer: net/http lets a handler run past WriteTimeout but cuts the answer it
// writes after it, so scoring's 240 s tick would do its work and Pub/Sub would see no
// answer and deliver it again (D219). ReadTimeout stays: the envelope is read at once,
// and net/http does not end a handler's context at it. The other services keep 60 s.
func server(s Spec, log *slog.Logger, addr string, h http.Handler) *http.Server {
	srv := httpx.Server(log, addr, h)
	srv.WriteTimeout = max(srv.WriteTimeout, s.tickBudget()+tickAnswer)
	return srv
}

// startMetrics serves /metrics on its own port (METRICS_PORT; A2 replaces it with the
// OTLP push). As before, a port that cannot be bound is logged and the service runs
// without it.
func (r *service) startMetrics(ctx context.Context) {
	addr := ":" + r.cfg.MetricsPort
	ln, err := r.e.listen(ctx, addr)
	if err != nil {
		r.log.Error("metrics server", "err", err)
		return
	}
	r.metrics = httpx.MetricsServer(r.log, addr)
	r.scrape = r.tier("metrics")
	r.scrape.run("metrics", func(context.Context) error {
		err := r.metrics.Serve(ln)
		if r.e.serverExit != nil {
			r.e.serverExit("metrics")
		}
		if !errors.Is(err, http.ErrServerClosed) {
			r.log.Error("metrics server", "err", err)
		}
		return nil
	})
	r.log.Info("metrics listening", "addr", addr)
}

// shutdown undoes the start in reverse and returns the steps that failed or overran
// their bound. It never stops early: a stuck step is logged and the next one runs,
// and the watchdog ends the process if the whole of it takes too long.
func (r *service) shutdown() error {
	began := time.Now()
	r.current.Store("begin")
	wd := time.AfterFunc(r.s.watchdogAfter(), func() {
		r.log.Error("shutdown overdue", "step", r.current.Load(), "elapsed_ms", time.Since(began).Milliseconds())
		r.mu.Lock()
		code := 1
		if r.panicErr != nil {
			code = 2
		}
		r.mu.Unlock()
		r.e.exit(code)
	})
	defer wd.Stop()

	r.mu.Lock()
	reason, task, cause := r.causeKind, r.causeTask, r.causeErr
	r.mu.Unlock()
	attrs := []any{"reason", orString(reason, "signal")}
	if task != "" {
		attrs = append(attrs, "task", task)
	}
	if cause != nil {
		attrs = append(attrs, "err", oneLine(cause))
	}
	r.log.Info("shutting down", attrs...)
	r.draining.Store(true)
	if r.serving && r.s.DrainDelay > 0 {
		r.step("drain", func() error { time.Sleep(r.s.DrainDelay); return nil })
	}
	if r.web != nil {
		r.step("http", r.stopHTTP)
	}
	r.endLife()
	if r.pool != nil {
		r.step("pool", func() error {
			return within(r.bound("pool", r.e.bounds.poolClose), func() error { r.e.closePool(r.pool); return nil })
		})
	}
	if r.metrics != nil {
		r.current.Store("metrics")
		stopped := time.Now()
		_ = r.metrics.Close()
		if err := r.scrape.stop(r.bound("metrics", r.e.bounds.afterClose)); err != nil {
			r.fail("metrics", err)
			r.log.Warn("shutdown step", "step", "metrics", "dur_ms", time.Since(stopped).Milliseconds(), "err", err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	exit := 0
	switch {
	case r.panicErr != nil:
		exit = 2 // as an unrecovered panic exits
	case len(r.failed) > 0 || r.cause != nil:
		exit = 1
	}
	r.log.Info("shut down", "dur_ms", time.Since(began).Milliseconds(), "exit", exit)
	return errors.Join(r.failed...)
}

// stopHTTP closes the listener and waits for the requests in flight; the budget is
// the Spec's, on a context the signal does not reach, plus the wait for Serve to
// return. On overtime it cuts the connections that are left (a push or a tick in
// flight: see the budget above) and lets the shutdown go on.
func (r *service) stopHTTP() error {
	r.serve.stopping() // Serve returning is the plan now
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.bound("http", r.s.shutdownTimeout())))
	defer cancel()
	err := r.web.Shutdown(ctx)
	if err != nil {
		_ = r.web.Close()
	}
	if werr := r.serve.wait(r.bound("http wait", r.e.bounds.afterClose)); err == nil {
		err = werr
	}
	return err
}

// step runs one shutdown step and logs it with its duration; a failure is recorded
// for Run's result, and the shutdown goes on.
func (r *service) step(name string, fn func() error) {
	r.current.Store(name)
	start := time.Now()
	err := fn()
	attrs := []any{"step", name, "dur_ms", time.Since(start).Milliseconds()}
	if err != nil {
		r.fail(name, err)
		r.log.Warn("shutdown step", append(attrs, "err", err)...)
		return
	}
	r.log.Info("shutdown step", attrs...)
}

// bound is the time a step may wait; the tests see every one.
func (r *service) bound(step string, d time.Duration) limit {
	if r.e.bounded != nil {
		r.e.bounded(step, d)
	}
	return limit(d)
}

// fail records a shutdown step that failed or overran its bound.
func (r *service) fail(step string, err error) {
	r.mu.Lock()
	r.failed = append(r.failed, fmt.Errorf("%s: %w", step, err))
	r.mu.Unlock()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func orString(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
