package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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

// Defaults for the Spec's zero durations (D-P2b: HTTP keeps today's 10 s inside the
// 30 s grace period), and the bounds of the steps the Spec does not tune.
const (
	defaultShutdownTimeout = 10 * time.Second
	defaultConsumerDrain   = 15 * time.Second
	defaultReadyTimeout    = 2 * time.Second // how long /readyz waits for its checks
	stepWait               = 3 * time.Second // jobs, relay and watch
	brokerClose            = 2 * time.Second
	poolClose              = 3 * time.Second
)

// Spec describes one service. The zero durations take the defaults.
type Spec struct {
	Name           string // api | ingest | scoring | notify: logger, config.Load, broker connection, db.Watch label, outbox publisher
	Edge           bool   // Instrument, Recover, SecurityHeaders (api, ingest); else Recover only
	BrokerRequired bool   // the broker gates /readyz (scoring, notify); api and ingest report it only (D101)
	Outbox         bool   // app runs the outbox relay (api, ingest, scoring); notify publishes nothing

	// Load reads the service's own settings before anything connects (D78): an error
	// ends the process at once, not behind the start-up retries. Wrap it in
	// ConfigError to choose the message it is logged under.
	Load func(log *slog.Logger) error
	// Build makes what the service runs on the opened pool and broker. Its ctx lasts
	// until the shutdown's last step, not until the signal.
	Build func(ctx context.Context, d Deps) (Parts, error)

	DrainDelay      time.Duration // keep serving this long after the shutdown starts, for the edge to drop the pod (default none)
	ShutdownTimeout time.Duration // HTTP Shutdown (default 10 s)
	ConsumerDrain   time.Duration // for the delivery in hand (default 15 s)
}

// Deps is what Build gets: the opened dependencies.
type Deps struct {
	Log    *slog.Logger
	Config config.Config
	Pool   *pgxpool.Pool
	Bus    events.Bus
	Outbox *outbox.Outbox // nil unless Spec.Outbox
}

// Parts is what a service runs: Build returns it.
type Parts struct {
	Routes    func(mux *http.ServeMux) // /v1/...; nil for health only. It must not register "/", /healthz or /readyz
	Checks    []Check                  // besides the db and the broker, which app adds
	Consumers []Task                   // block until ctx ends: bus.ConsumeIngested(ctx, log, handle)
	Jobs      []Task                   // timers, the reconciler
}

// Task is a goroutine the service owns. Run returns when ctx ends; an error, or a
// panic, stops the whole service in order.
type Task struct {
	Name string // a closed set, written in code
	Run  func(ctx context.Context) error
}

// ConfigError is what Spec.Load returns to say which message its error is logged
// under ("beta config"); any other error is logged as "config".
type ConfigError struct {
	Msg string
	Err error
}

// Error is the message and the cause.
func (e ConfigError) Error() string { return e.Msg + ": " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e ConfigError) Unwrap() error { return e.Err }

// Run starts s and blocks until ctx ends or a part fails; then it shuts down in
// order. It logs why it failed and returns the error (Main exits 1): a start-up
// error, the failed task's error, or the steps of a shutdown that overran.
func Run(ctx context.Context, s Spec) error { return run(ctx, s, defaultSeams()) }

// seams are the dependencies Run opens, so the tests can stand in for them.
type seams struct {
	logger       func(service string) *slog.Logger
	loadConfig   func(service string) (config.Config, error)
	openPool     func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error)
	watch        func(ctx context.Context, pool *pgxpool.Pool, service string, log *slog.Logger)
	ping         func(ctx context.Context, pool *pgxpool.Pool) error
	closePool    func(pool *pgxpool.Pool)
	openBus      func(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (events.Bus, error)
	relay        func(ctx context.Context, ob *outbox.Outbox)
	listen       func(ctx context.Context, addr string) (net.Listener, error)
	readyTimeout time.Duration
	launched     func(task string) // a test hook: every task as it starts
}

func defaultSeams() seams {
	return seams{
		logger:     httpx.Logger,
		loadConfig: config.Load,
		openPool: func(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
			return db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
		},
		watch:     db.Watch,
		ping:      func(ctx context.Context, pool *pgxpool.Pool) error { return pool.Ping(ctx) },
		closePool: (*pgxpool.Pool).Close,
		openBus:   eventbus.Open,
		relay:     func(ctx context.Context, ob *outbox.Outbox) { ob.Run(ctx) },
		listen: func(ctx context.Context, addr string) (net.Listener, error) {
			return (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		},
		readyTimeout: defaultReadyTimeout,
	}
}

// service is one Run: what it has opened so far and the tiers it has started.
type service struct {
	s   Spec
	e   seams
	log *slog.Logger
	cfg config.Config

	life       context.Context // ends with the last shutdown step: Build and every task run on it
	endLife    context.CancelFunc
	cancelWait context.CancelFunc // wakes Run: the first failing task calls it
	draining   atomic.Bool

	mu     sync.Mutex
	cause  error // the first task that failed
	failed []error

	pool    *pgxpool.Pool
	bus     events.Bus
	metrics *http.Server
	web     *http.Server
	serving bool

	jobs, consumers, serve, infra, scrape *tier // timers; deliveries; the listener; relay and db watch; /metrics
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

	if err := r.start(ctx); err != nil {
		return errors.Join(err, r.shutdown("start failed"))
	}
	<-waitCtx.Done()
	reason := "signal"
	if err := r.failure(); err != nil {
		reason = err.Error()
	}
	steps := r.shutdown(reason)
	return errors.Join(r.failure(), steps)
}

func (r *service) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cause
}

// stop is what a failing task calls: it records the first cause and wakes Run.
func (r *service) stop(task string, err error) {
	r.mu.Lock()
	if r.cause == nil {
		r.cause = fmt.Errorf("%s: %w", task, err)
	}
	r.mu.Unlock()
	r.cancelWait()
}

func (r *service) tier(msg string) *tier {
	t := newTier(r.life, r.log, msg, r.stop)
	t.launch = r.e.launched
	return t
}

// start opens everything in order: settings, pool and its watch, broker, /metrics,
// Build, the relay, timers, consumers and last the HTTP listener, so the start-up
// probe covers the waits above.
func (r *service) start(ctx context.Context) (err error) {
	if r.cfg, err = r.e.loadConfig(r.s.Name); err != nil {
		r.log.Error("config", "err", err)
		return err
	}
	if r.s.Load != nil {
		if err = r.s.Load(r.log); err != nil {
			msg, cause := "config", err
			var ce ConfigError
			if errors.As(err, &ce) {
				msg, cause = ce.Msg, ce.Err
			}
			r.log.Error(msg, "err", cause)
			return err
		}
	}
	// db.Open retries until the role can log in and the migrate job has applied the
	// schema; a signal ends the wait.
	if r.pool, err = r.e.openPool(ctx, r.cfg, r.log); err != nil {
		r.pool = nil
		r.log.Error("db", "err", err)
		return err
	}
	r.infra = r.tier("background")
	r.infra.run("db watch", func(ctx context.Context) error {
		r.e.watch(ctx, r.pool, r.s.Name, r.log)
		return nil
	})
	if r.bus, err = r.e.openBus(ctx, r.cfg, r.s.Name, r.log); err != nil {
		r.bus = nil
		r.log.Error("events", "err", err)
		return err
	}
	r.startMetrics(ctx)

	deps := Deps{Log: r.log, Config: r.cfg, Pool: r.pool, Bus: r.bus}
	if r.s.Outbox {
		deps.Outbox = outbox.New(r.pool, r.s.Name, r.bus, r.log)
	}
	parts, err := r.s.Build(r.life, deps)
	if err != nil {
		r.log.Error("build", "err", err)
		return err
	}
	if deps.Outbox != nil {
		r.infra.run("outbox relay", func(ctx context.Context) error { r.e.relay(ctx, deps.Outbox); return nil })
	}
	r.jobs = r.tier("job")
	for _, t := range parts.Jobs {
		r.jobs.run(t.Name, t.Run)
	}
	r.consumers = r.tier("consume")
	for _, t := range parts.Consumers {
		r.consumers.run(t.Name, func(ctx context.Context) error {
			err := t.Run(ctx)
			if err == nil && ctx.Err() == nil {
				err = errors.New("returned before the shutdown")
			}
			return err
		})
	}
	return r.startHTTP(ctx, parts)
}

func (r *service) startHTTP(ctx context.Context, parts Parts) error {
	checks := append([]Check{{Name: "db", Required: true, Fn: func(ctx context.Context) error { return r.e.ping(ctx, r.pool) }}}, parts.Checks...)
	checks = append(checks, Check{Name: "broker", Required: r.s.BrokerRequired, Fn: r.bus.Ready})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", readyz(checks, &r.draining, r.e.readyTimeout))
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
		r.log.Error("serve", "err", err)
		return err
	}
	r.web = httpx.Server(r.log, addr, handler)
	r.serve = r.tier("serve")
	r.serve.run("http", func(context.Context) error {
		if err := r.web.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	r.log.Info("listening", "addr", addr)
	r.serving = true
	return nil
}

// startMetrics serves /metrics on its own port. As before, a port that cannot be
// bound is logged and the service runs without it.
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
		if err := r.metrics.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			r.log.Error("metrics server", "err", err)
		}
		return nil
	})
	r.log.Info("metrics listening", "addr", addr)
}

// shutdown undoes the start in reverse and returns the steps that failed or overran
// their bound. It never stops early: a stuck step is logged and the next one runs.
func (r *service) shutdown(reason string) error {
	began := time.Now()
	r.log.Info("shutting down", "reason", reason)
	r.draining.Store(true)
	r.jobs.stopping()
	if r.serving && r.s.DrainDelay > 0 {
		r.step("drain", func() error { time.Sleep(r.s.DrainDelay); return nil })
	}
	if r.consumers != nil {
		r.step("consumers", func() error { return r.consumers.stop(orDefault(r.s.ConsumerDrain, defaultConsumerDrain)) })
	}
	if r.web != nil {
		r.step("http", r.stopHTTP)
	}
	if r.jobs != nil {
		r.step("jobs", func() error { return r.jobs.wait(stepWait) })
	}
	if r.infra != nil {
		r.step("relay", func() error { return r.infra.stop(stepWait) })
	}
	if r.bus != nil {
		r.step("broker", func() error { return within(brokerClose, func() error { r.bus.Close(); return nil }) })
	}
	r.endLife()
	if r.pool != nil {
		r.step("pool", func() error { return within(poolClose, func() error { r.e.closePool(r.pool); return nil }) })
	}
	if r.metrics != nil {
		_ = r.metrics.Close()
		if err := r.scrape.stop(stepWait); err != nil {
			r.fail("metrics", err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	exit := 0
	if len(r.failed) > 0 || r.cause != nil {
		exit = 1
	}
	r.log.Info("shut down", "dur_ms", time.Since(began).Milliseconds(), "exit", exit)
	return errors.Join(r.failed...)
}

// stopHTTP closes the listener and waits for the requests in flight; the budget is
// the Spec's, on a context the signal does not reach. On overtime it cuts the
// connections that are left and lets the shutdown go on.
func (r *service) stopHTTP() error {
	r.serve.stopping() // Serve returning is the plan now
	ctx, cancel := context.WithTimeout(context.Background(), orDefault(r.s.ShutdownTimeout, defaultShutdownTimeout))
	defer cancel()
	err := r.web.Shutdown(ctx)
	if err != nil {
		_ = r.web.Close()
	}
	if werr := r.serve.wait(stepWait); err == nil {
		err = werr
	}
	return err
}

// step runs one shutdown step and logs it with its duration; a failure is recorded
// for Run's result, and the shutdown goes on.
func (r *service) step(name string, fn func() error) {
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
