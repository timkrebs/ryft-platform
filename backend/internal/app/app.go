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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// The shutdown's budget. Every step is bounded by what is written here, the bounds
// live in one place (bounds), the bound of a step includes the waits after it, and a
// test (TestShutdownStaysInBudget) fails when the worst case reaches the watchdog's
// time, Spec.Grace (Cloud Run's 10 s from SIGTERM to SIGKILL) less watchdogMargin (9 s).
//
//	step     bound                                                  Edge (api, ingest)  push and tick (scoring, notify)
//	drain    Spec.DrainDelay                                        0                   0
//	http     Shutdown (3 s; push, tick and health 2 s) + afterClose 4 s                 3 s
//	push     the metrics push (OTLP), once                          2 s                 2 s
//	pool     poolClose                                              1 s                 1 s
//	metrics  afterClose (the :9090 listener, off Cloud Run only)    1 s                 1 s
//	worst case                                                      8 s                 7 s
//
// Nothing else runs in the background to stop (D219): the consumers are push routes,
// the timers and the relay are tick steps and inline flushes, the db watch is the
// tick's ping, and Pub/Sub's REST publish holds no connection to close (D218). So a
// push or a tick still in flight when the http step's Shutdown ends is cut there and
// not waited for: its context ends (the handlers' base context, and httpx.Detach's), so
// its database calls return and give their connections back before the pool step
// (review-a1 finding 6); Pub/Sub delivers the push again (D100), and the next tick comes
// in 15 minutes. The tick's budget is no part of the shutdown's. The push services'
// Shutdown is 2 s, not 1 s: an idle HTTP/2 connection of Cloud Run's front end must have
// the time to see its GOAWAY.
//
// A step that overruns is logged, recorded (exit 1) and the next one runs; the
// watchdog (9 s) ends the process whatever step is stuck.
const (
	defaultEdgeShutdown   = 3 * time.Second  // HTTP Shutdown of a service with a public API (a sync in flight is cut, the app retries)
	defaultHealthShutdown = 2 * time.Second  // of one that serves its pushes, its tick and health only
	defaultGrace          = 10 * time.Second // Cloud Run's SIGTERM to SIGKILL (compose's docker stop waits as long)
	defaultTickBudget     = 45 * time.Second // api, ingest and notify; scoring sets 240 s (D219)
	defaultReadyTimeout   = 2 * time.Second  // how long a ready path waits for its checks
	watchdogMargin        = time.Second      // the watchdog fires this long before Grace
	// defaultStartWait is how long a request that arrives during the start (the pool, Build)
	// waits for it before it is answered 503 with Retry-After (gate). A cold start takes
	// about a second (design §3).
	defaultStartWait = 5 * time.Second
	// tickAnswer is how long a tick may run past its budget before it has answered: the
	// final step's 10 s (the metrics push) and 5 s to spare. The server's write timeout
	// is at least the budget plus this (see server).
	tickAnswer = 15 * time.Second
)

// errCut is the cause (context.Cause) of the handlers' contexts when the http step's
// Shutdown ran out and stopHTTP cut them. It wraps context.Canceled, so errors.Is(err,
// context.Canceled) holds for the cause as for Err. The tests tell this cut from Close's
// by it: Close also ends a request's context, with no cause of its own.
var errCut = fmt.Errorf("app: the http step's Shutdown ran out and cut the handlers: %w", context.Canceled)

// bounds are the shutdown steps' own bounds, all in one place; the tests shorten them.
type bounds struct {
	afterClose time.Duration // waiting for Serve to return after Shutdown or Close; the /metrics stop
	push       time.Duration // the metrics push at SIGTERM
	poolClose  time.Duration
}

func defaultBounds() bounds {
	return bounds{afterClose: time.Second, push: 2 * time.Second, poolClose: time.Second}
}

// OutboxKind is the outbox app builds for a service, hands to Build as Deps.Outbox and
// sweeps on every tick (D219).
type OutboxKind int

const (
	// NoOutbox is for a service that publishes nothing.
	NoOutbox OutboxKind = iota
	// EventOutbox relays the service's events by key into their topics (api, ingest,
	// scoring; D101).
	EventOutbox
	// WaitOutbox is notify's wait box: its deferrals (outbox rows of publisher notify with
	// a not-before time) relay into ryft-notify-wait and come back on /push/wait (D219).
	WaitOutbox
)

// Spec describes one service. The zero durations take the defaults.
type Spec struct {
	Name   string     // api | ingest | scoring | notify: logger, config.Load, transport, tick label, outbox publisher, metrics
	Edge   bool       // Instrument, Recover, SecurityHeaders (api, ingest); else Recover only and a 2 s HTTP shutdown
	Outbox OutboxKind // which outbox app builds (and, with Parts.Routes, sweeps)

	// Load reads the service's own settings before anything connects (D78): an error
	// ends the process at once, not behind the start-up retries. Wrap it in
	// ConfigError to choose the message it is logged under.
	Load func(log *slog.Logger) error
	// Build makes what the service runs on the opened pool and transport. Its ctx lasts
	// until the shutdown's http step is over (it ends before the pool closes), not until
	// the signal; a signal while Build runs ends it, so Build must return when ctx does
	// (one that does not ends at Cloud Run's SIGKILL). An error is logged under
	// ConfigError's message when it is one, else as "build".
	Build func(ctx context.Context, d Deps) (Parts, error)

	DrainDelay      time.Duration // keep serving this long after the shutdown starts (default none: Cloud Run stops routing first)
	ShutdownTimeout time.Duration // HTTP Shutdown (default 3 s for Edge, 2 s otherwise)
	TickBudget      time.Duration // POST /internal/tick's steps together (default 45 s; scoring 240 s, D219)
	Grace           time.Duration // SIGTERM to SIGKILL (default Cloud Run's 10 s): the watchdog and the budget test use it
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
	return s.DrainDelay + s.shutdownTimeout() + b.afterClose + b.push + b.poolClose + b.afterClose
}

// Deps is what Build gets: the opened dependencies.
type Deps struct {
	Log    *slog.Logger
	Config config.Config
	Pool   *pgxpool.Pool
	// Bus is the service's Pub/Sub (D218): it publishes, and it builds the consumers'
	// push routes (Bus.Push) and notify's wait sender.
	Bus    *eventbus.Transport
	Outbox *outbox.Outbox // per Spec.Outbox; nil for NoOutbox
	// Tick builds POST /internal/tick from exactly these steps, within Spec.TickBudget,
	// with the metrics push as its final step: for a service that mounts its own routes
	// (Parts.Handler). With Parts.Routes app builds the tick itself.
	Tick func(steps ...eventbus.Tick) (http.Handler, error)
}

// Parts is what a service runs: Build returns it.
type Parts struct {
	// Routes mounts /v1/...; nil for none. It must not register "/", the health paths,
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
	// Handler is, instead of the four above, the service's whole mux (api and ingest,
	// whose health answers are httpx.Health's, as API.md has them): its health paths, its
	// tick (Deps.Tick), its routes and its 404. app serves it behind the start gate and the
	// edge middleware and mounts nothing of its own.
	Handler http.Handler
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
	newPush      func(service string, log *slog.Logger) (func(context.Context) error, error) // the metrics push; nil func: none
	openPool     func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error)
	loginRefused func(err error) bool                                                // openPool's refused login on Cloud Run (db.LoginRefused): the start holds (holdLogin)
	ping         func(ctx context.Context, pool *pgxpool.Pool) error                 // the ready paths' db check
	dbPing       func(ctx context.Context, pool *pgxpool.Pool, service string) error // the tick's first step (ryft_db_up)
	sweep        func(ctx context.Context, ob *outbox.Outbox) error                  // the tick's outbox step
	closePool    func(pool *pgxpool.Pool)
	openBus      func(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (*eventbus.Transport, error)
	newOutbox    func(pool *pgxpool.Pool, publisher string, to events.Sender, log *slog.Logger) *outbox.Outbox
	listen       func(ctx context.Context, addr string) (net.Listener, error)
	cloudRun     func() bool                        // a Cloud Run service (K_SERVICE): no /metrics listener, generic ready answers
	exit         func(code int)                     // the watchdog's way out
	launched     func(task string)                  // a test hook: every task as it starts
	startDone    func()                             // a test hook: the start is over and its watcher has ended
	serverExit   func(server string)                // a test hook: a server's Serve has returned
	bounded      func(step string, d time.Duration) // a test hook: every bound a step waits
	readyTimeout time.Duration
	startWait    time.Duration
	retryEvery   time.Duration // the least time between two logins while the start holds (defaultLoginRetryEvery)
	bounds       bounds
}

func defaultSeams() seams {
	return seams{
		logger:     httpx.Logger,
		loadConfig: config.Load,
		newPush:    otlpPush,
		openPool: func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
			return db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
		},
		loginRefused: db.LoginRefused,
		ping:         func(ctx context.Context, pool *pgxpool.Pool) error { return pool.Ping(ctx) },
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
		cloudRun:     httpx.CloudRunService,
		exit:         os.Exit,
		readyTimeout: defaultReadyTimeout,
		startWait:    defaultStartWait,
		retryEvery:   defaultLoginRetryEvery,
		bounds:       defaultBounds(),
	}
}

// otlpPush is the service's metrics push (D224, design §6): OTLP_ENDPOINT, OTLP_USER and
// OTLP_TOKEN (config.LoadOTLP, whose refusal ends the start like any setting's), the
// process's registry as service. Without an endpoint there is no push (nil).
func otlpPush(service string, log *slog.Logger) (func(context.Context) error, error) {
	o, err := config.LoadOTLP()
	if err != nil {
		return nil, err
	}
	p, err := metrics.NewOTLP(metrics.OTLPOptions{Endpoint: o.Endpoint, User: o.User, Token: o.Token, Service: service,
		Gatherer: prometheus.DefaultGatherer, Log: log})
	if err != nil || p == nil {
		return nil, err
	}
	return p.Push, nil
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
	// handlers is every request's base context; cut ends it (cause errCut) when the http
	// step's Shutdown runs out, so the handlers still running stop their database calls
	// (finding 6).
	handlers context.Context
	cut      context.CancelFunc
	draining atomic.Bool
	current  atomic.Value // the shutdown step in progress, for the watchdog

	mu        sync.Mutex
	cause     error  // the first failure: a start error or a task's, wrapped with its name
	causeKind string // "start" or "task"; "" for a signal
	causeTask string
	causeErr  error // cause without the task's name
	panicErr  error // the first recovered panic
	failed    []error

	push    func(context.Context) error // the metrics push; nil: none
	pool    *pgxpool.Pool
	bus     *eventbus.Transport
	gate    *gate
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
	stop, end := context.WithCancelCause(context.Background())
	defer end(nil) // Run is over: a plain context.Canceled, unless the cut came first
	r.cut = func() { end(errCut) }
	r.handlers = httpx.WithStop(stop)
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
	if err != nil && r.gate != nil {
		r.gate.abort() // the requests waiting for the start get their 503 now
	}
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

// start opens everything in order: the settings (the service's own, the metrics push's),
// the transport, the HTTP listener behind the start gate and /metrics, then the pool, the
// outbox and Build, and last it opens the gate. The listener comes before the pool so that
// Cloud Run's startup probe (/health/live) passes while the database is down, and the
// ticks that arrive meanwhile report it (gate); nothing connects before the settings are
// valid (D78).
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
	if r.push, err = r.e.newPush(r.s.Name, r.log); err != nil {
		r.push = nil
		r.logSetup(ctx, "otlp config", err)
		return err
	}
	// Opening the transport makes one network call, the first fetch of Google's JWKS
	// (at most 10 s; a failure doesn't fail it, the pushes answer 503 until keys arrive),
	// so the listener may open up to 10 s late, within the startup probe's 20 s (D218,
	// D220). It comes first because its push verifier checks the ticks that arrive during
	// the start; its key refresher lives on the service's life, which ends after the http
	// step.
	if r.bus, err = r.e.openBus(r.life, r.cfg, r.s.Name, r.log); err != nil {
		r.bus = nil
		r.logSetup(ctx, "events", err)
		return err
	}
	if err = r.listen(ctx); err != nil {
		r.logSetup(ctx, "serve", err)
		return err
	}
	if !r.e.cloudRun() {
		r.startMetrics(ctx)
	}
	// db.Open retries until the role can log in and the migrate job has applied the
	// schema; a signal ends the wait. On Cloud Run a refused login ends it at once, and
	// the start holds instead of failing (holdLogin).
	r.pool, err = r.e.openPool(ctx, r.cfg, r.log)
	if err != nil && r.e.loginRefused != nil && r.e.loginRefused(err) {
		r.pool, err = r.holdLogin(ctx, err)
	}
	if err != nil {
		r.pool = nil
		r.logSetup(ctx, "db", err)
		return err
	}
	ob, err := r.openOutbox()
	if err != nil {
		r.logSetup(ctx, "events", err)
		return err
	}
	deps := Deps{Log: r.log, Config: r.cfg, Pool: r.pool, Bus: r.bus, Outbox: ob, Tick: r.tick}
	parts, err := r.s.Build(r.life, deps)
	if err == nil {
		var h http.Handler
		if h, err = r.handler(parts, ob); err == nil {
			r.gate.open(h)
			r.serving = true
			return nil
		}
	}
	r.logSetup(ctx, "build", err)
	return err
}

// holdLogin keeps a Cloud Run instance up after a refused login (db.LoginRefused: SQLSTATE
// class 28 or Supavisor's ECIRCUITBREAKER) instead of ending the start. An exit would make
// Cloud Run start another instance at once, whose login feeds the breaker again (a crash
// loop that keeps the whole role blocked, design §4), and no tick would ever report
// ryft_db_up 0. So the gate holds: every request but the health paths gets the 503 at
// once, a tick reports the database down, and a tick with a valid token that is due
// (gate.every since the last login) hands this loop one login retry, bounded by that
// tick (it is when the instance has CPU). It returns the pool once a retry passes, or the
// signal's error.
func (r *service) holdLogin(ctx context.Context, refused error) (*pgxpool.Pool, error) {
	r.log.Error("db", "err", refused)
	r.gate.hold(r.gate.now())
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case req := <-r.gate.retries:
			// Ended by the signal or the tick's end. The pool keeps open's context once it
			// is made, so a passed open does not cancel it (pgxpool uses it only to make
			// MinConns, 0 here).
			open, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(req.ctx, cancel)
			pool, err := r.e.openPool(open, r.cfg, r.log)
			if !stop() || err != nil {
				cancel()
			}
			req.result <- err == nil
			if err == nil {
				r.log.Info("db: the login passed on a retry")
				return pool, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.log.Error("db", "err", err)
		}
	}
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

// tick is POST /internal/tick from steps, within the Spec's budget, with the metrics push
// as its final step (D219, D224).
func (r *service) tick(steps ...eventbus.Tick) (http.Handler, error) {
	return r.bus.Tick(r.s.tickBudget(), r.push, steps...)
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

// handler is what the gate opens to: Parts.Handler as it is, or app's mux of the health
// paths, the tick, the push routes, Parts.Routes and the JSON 404.
func (r *service) handler(parts Parts, ob *outbox.Outbox) (http.Handler, error) {
	if parts.Handler != nil {
		if parts.Routes != nil || len(parts.Checks) > 0 || len(parts.Push) > 0 || len(parts.Ticks) > 0 {
			return nil, errors.New("app: Parts.Handler is the whole mux: Routes, Checks, Push and Ticks must be empty")
		}
		return parts.Handler, nil
	}
	checks := append([]Check{{Name: "db", Required: true, Fn: func(ctx context.Context) error { return r.e.ping(ctx, r.pool) }}}, parts.Checks...)
	tick, err := r.tick(r.ticks(ob, parts.Ticks)...)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+httpx.PathHealthz, httpx.Live)
	mux.HandleFunc("GET "+httpx.PathLive, httpx.Live)
	ready := readyz(r.log, checks, &r.draining, r.e.readyTimeout, r.e.cloudRun())
	mux.Handle("GET "+httpx.PathReadyz, ready)
	mux.Handle("GET "+httpx.PathReady, ready)
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
	return mux, nil
}

// listen opens the HTTP port and serves the start gate on it: the edge middleware (or
// Recover) around the gate, which hands the requests to the service's handler once the
// start is done.
func (r *service) listen(ctx context.Context) error {
	r.gate = newGate(r.s.Name, r.log, r.bus.Verifier(), r.push, r.e.cloudRun(), r.e.startWait)
	r.gate.every = orDefault(r.e.retryEvery, defaultLoginRetryEvery)
	handler := httpx.Recover(r.gate)
	if r.s.Edge {
		handler = httpx.Instrument(r.log, httpx.Recover(httpx.SecurityHeaders(r.gate)))
	}
	addr := ":" + r.cfg.Port
	ln, err := r.e.listen(ctx, addr)
	if err != nil {
		return err
	}
	r.web = server(r.s, r.log, addr, handler)
	r.web.BaseContext = func(net.Listener) context.Context { return r.handlers }
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

// startMetrics serves /metrics on its own port (METRICS_PORT), off Cloud Run only: there
// the push replaces it. As before, a port that cannot be bound is logged and the service
// runs without it.
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
	if r.push != nil {
		// After the http step, so the push carries its last requests' counts.
		r.step("push", func() error {
			d := r.bound("push", r.e.bounds.push)
			return within(d, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d))
				defer cancel()
				return r.push(ctx)
			})
		})
	}
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
// return. On overtime it ends the handlers' contexts (so a push or a tick in flight
// gives its database connection back before the pool step), cuts the connections that
// are left and lets the shutdown go on.
func (r *service) stopHTTP() error {
	r.serve.stopping() // Serve returning is the plan now
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.bound("http", r.s.shutdownTimeout())))
	defer cancel()
	err := r.web.Shutdown(ctx)
	if err != nil {
		r.cut()
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
