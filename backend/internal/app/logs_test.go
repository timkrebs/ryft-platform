package app

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// tickSteps is every tick step's name: app's two and the services' own (D219). A step's
// name is written in code, never derived from a request.
var tickSteps = logcheck.Enum("db ping", "outbox sweep",
	"scoring pass", "stale check", "account reconcile", "invite settings", "invite email purge", "app attest purge", "finally")

// lifecycleLogKeys is every line the lifecycle logs besides the services' own, with
// the keys it may carry and the kind of each value (D114, D117). A task's name, a
// step and a reason are closed sets written in code; an error is free text, capped,
// and carries a cause (a connection, a path), never a request. Any other line, key
// or kind of value fails TestLifecycleLogsKeepToTheirKeys, so a new line has to be
// listed here after the rule has been checked against it.
var lifecycleLogKeys = func() logcheck.Lines {
	task, errText := logcheck.Text(80), logcheck.Text(1024)
	took := logcheck.Pattern(regexp.MustCompile(`^[0-9.]+(ns|µs|ms|s|m[0-9.]+s|h[0-9m.]+s)$|^0s$`))
	messageID := logcheck.Pattern(regexp.MustCompile(`^[0-9]{1,32}$`)) // Pub/Sub's, decimal
	lines := logcheck.Lines{
		"listening":                             {"addr": logcheck.Pattern(regexp.MustCompile(`^:[0-9]{1,5}$`))},
		"metrics listening":                     {"addr": logcheck.Pattern(regexp.MustCompile(`^:[0-9]{1,5}$`))},
		"start interrupted by the signal":       {"step": logcheck.Enum("config", "otlp config", "db", "events", "build", "serve")},
		"metrics server":                        {"err": errText},
		"task ended with error during shutdown": {"task": task, "err": errText},
		"panic": {"task": task, "check": logcheck.Enum("db", "auth"), "step": tickSteps,
			"panic": errText, "stack": logcheck.Text(16384)},
		"shutting down":    {"reason": logcheck.Enum("signal", "task", "start"), "task": task, "err": errText},
		"shutdown step":    {"step": logcheck.Enum("drain", "http", "push", "pool", "metrics"), "dur_ms": logcheck.Count, "err": errText},
		"shutdown overdue": {"step": logcheck.Enum("begin", "drain", "http", "push", "pool", "metrics"), "elapsed_ms": logcheck.Count},
		"shut down":        {"dur_ms": logcheck.Count, "exit": logcheck.CountMax(2)},

		// The start gate and the ready paths on Cloud Run (httpx.CloudReady): the cause is
		// a check's error or the start's, never a request's.
		"not ready":                       {"err": logcheck.Text(203)},
		"tick before the start was done":  {"waited": took},
		"db: the login passed on a retry": {}, // the hold on a refused login is over (holdLogin)
		"push rejected":                   {"path": logcheck.Enum(eventbus.PathTick), "reason": logcheck.Text(40), "err": errText},
		"push token cannot be checked":    {"path": logcheck.Enum(eventbus.PathTick), "reason": logcheck.Text(40), "err": errText},

		// The transport (eventbus.Open, auth.InsecurePush) and the tick (pubsub.NewTick).
		"push and tick tokens are NOT checked (ALLOW_INSECURE_PUSH=true): local development only": {},
		"tick":              {"result": logcheck.Enum("ok", "error"), "message_id": messageID, "took": took},
		"tick: step failed": {"step": tickSteps, "err": errText, "took": took},
		"tick: the budget is spent, step skipped":              {"step": tickSteps, "budget": took},
		"tick: the previous one still runs, skipping this one": {"message_id": messageID},
		"tick: the final step failed":                          {"err": errText},
		"tick: not a push envelope":                            {"status": logcheck.Enum("400", "413", "415")},
		"tick: not a tick":                                     {"message_id": messageID, "key": logcheck.Text(70)},
	}
	// A failed start logs its error under the area's message; a failed server under its tier's (serve, metrics).
	for _, msg := range []string{"config", "otlp config", "beta config", "apns config", "apns", "db", "events", "build", "serve", "metrics"} {
		lines[msg] = logcheck.Keys{"err": errText, "task": task}
	}
	// "events" is also the transport's start line: what it publishes to and how it checks pushes.
	lines["events"]["transport"] = logcheck.Enum("pubsub")
	lines["events"]["project"] = logcheck.Pattern(regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`))
	lines["events"]["endpoint"] = logcheck.Pattern(regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$|^http://[a-z0-9.-]+:[0-9]{1,5} \(emulator\)$`))
	lines["events"]["push_auth"] = logcheck.Enum("google_oidc", "insecure")
	return lines
}()

// TestLifecycleLogsKeepToTheirKeys runs a service the ways it ticks and ends (a tick
// with a failing, a panicking and a skipped step, a signal, a failing server, a
// panicking one, a stuck step, a failed start) as scoring and notify log them, and
// checks every line app wrote against lifecycleLogKeys.
func TestLifecycleLogsKeepToTheirKeys(t *testing.T) {
	boom := errors.New("connection refused")
	var all strings.Builder
	record := func(g *rig) { all.WriteString(g.logs.text() + "\n") }
	asScoring := func(g *rig) {
		g.e.logger = func(string) *slog.Logger { return logx.NewWriter(g.logs, "scoring", slog.LevelDebug) }
	}

	t.Run("ticks and a signal", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.pingErr = boom
		g.parts.Ticks = []eventbus.Tick{
			{Name: "scoring pass", Run: func(context.Context) error { panic("kaboom") }},
			{Name: "stale check", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
			step(g.rec, "invite settings", nil), // the budget is spent: skipped
		}
		s := g.spec()
		s.TickBudget = 50 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		g.tick(t)
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		record(g)
	})
	t.Run("a server that fails", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		done := g.serve(t, context.Background(), g.spec())
		close(g.httpFail)
		if err := result(t, done); !errors.Is(err, errListener) {
			t.Fatalf("Run = %v", err)
		}
		record(g)
	})
	t.Run("a server that panics", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		done := g.serve(t, context.Background(), g.spec())
		close(g.httpBoom)
		if err := result(t, done); err == nil {
			t.Fatal("a panic is a failure")
		}
		record(g)
	})
	t.Run("a step that overruns", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.e.bounds.poolClose = 20 * time.Millisecond
		release := make(chan struct{})
		g.e.closePool = func(p *pgxpool.Pool) { <-release; p.Close() }
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, g.spec())
		cancel()
		err := result(t, done)
		close(release)
		if err == nil {
			t.Fatal("an overrun is a failure")
		}
		record(g)
	})
	t.Run("a watchdog", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		stuckPool(g)
		s := g.spec()
		s.Grace = watchdogMargin + 100*time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		cancel()
		eventually(t, func() bool { return len(g.exitCodes()) == 1 }, "the watchdog")
		g.free()
		_ = result(t, done)
		record(g)
	})
	t.Run("a tick and a ready probe before the start is done, on Cloud Run", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.cloud = true
		g.poolGate = make(chan struct{})
		g.e.startWait = 20 * time.Millisecond
		var dbDown atomic.Bool
		g.e.ping = func(context.Context, *pgxpool.Pool) error {
			if dbDown.Load() {
				return boom
			}
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := g.run(ctx, g.spec())
		eventually(t, func() bool { return g.addr("http") != "" }, "the listener")
		g.get(t, "/health/ready")
		g.tick(t)
		close(g.poolGate)
		<-g.started
		dbDown.Store(true)
		g.get(t, "/health/ready")
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		record(g)
	})
	t.Run("a refused login on Cloud Run, then a retry that passes", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.cloud = true
		g.refusals = 1
		g.e.retryEvery = time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := g.waitForPort(t, ctx, g.spec())
		eventually(t, func() bool { return len(g.logs.find("db")) == 1 }, "the refused login's line")
		g.get(t, "/health/ready")
		time.Sleep(5 * time.Millisecond)
		g.tick(t)
		<-g.started
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		record(g)
	})
	for name, set := range map[string]func(g *rig){
		"the database":            func(g *rig) { g.poolErr = boom },
		"the transport":           func(g *rig) { g.busErr = boom },
		"build":                   func(g *rig) { g.buildErr = boom },
		"the HTTP port":           func(g *rig) { g.httpErr = boom },
		"the settings":            func(g *rig) { g.loadErr = ConfigError{Msg: "beta config", Err: boom} },
		"the metrics push's":      func(g *rig) { g.otlpErr = boom },
		"the push's final answer": func(g *rig) { g.buildErr, g.pushErr = boom, boom },
	} {
		t.Run("a failed start: "+name, func(t *testing.T) {
			g := newRig()
			asScoring(g)
			set(g)
			if err := run(context.Background(), g.spec(), g.e); !errors.Is(err, boom) {
				t.Fatalf("Run = %v", err)
			}
			record(g)
		})
	}

	lines, problems := logcheck.Check(all.String(), logcheck.Standard("scoring"), lifecycleLogKeys, logcheck.Forbidden{})
	for _, p := range problems {
		t.Error(p)
	}
	// logcheck.Standard allows a user and a request on every line, but a lifecycle line
	// belongs to no user and no request: none may carry one.
	for msg, recs := range lines {
		for _, rec := range recs {
			for _, key := range []string{"user_id", "request_id", "client_request_id", "route"} {
				if _, ok := rec[key]; ok {
					t.Errorf("%q carries %q: %v", msg, key, rec)
				}
			}
		}
	}
	// The lines the runbooks and the Insights queries are written for were all seen.
	for _, msg := range []string{"shutting down", "shutdown step", "shut down", "shutdown overdue", "panic", "listening",
		"metrics listening", "serve", "db", "events", "build", "tick", "tick: step failed", "tick: the budget is spent, step skipped",
		"push and tick tokens are NOT checked (ALLOW_INSECURE_PUSH=true): local development only",
		"not ready", "tick before the start was done", "otlp config", "db: the login passed on a retry"} {
		if len(lines[msg]) == 0 {
			t.Errorf("no %q line in any run", msg)
		}
	}
}

// TestLifecycleLogKindsRejectMutations is the check on the check: a line with a key it
// may not carry, a reason or a step outside the closed set or an exit code that is not
// one fails.
func TestLifecycleLogKindsRejectMutations(t *testing.T) {
	std := logcheck.Standard("scoring")
	for _, line := range []string{
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutting down","reason":"because"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutting down","reason":"signal","recovery":61.7}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shut down","dur_ms":12,"exit":"0"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutdown step","step":"consumers","dur_ms":12}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"a new line"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"ERROR","service":"scoring","msg":"tick: step failed","step":"user 8f2c","err":"x","took":"1s"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"tick","result":"ok","message_id":"4242","took":"1s","recovery":61.7}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"tick","result":"ok","message_id":"me@example.com","took":"1s"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"events","transport":"pubsub","project":"ryft-local","endpoint":"https://x.example/?token=abc","push_auth":"insecure"}`,
	} {
		if _, problems := logcheck.Check(line, std, lifecycleLogKeys, logcheck.Forbidden{}); len(problems) == 0 {
			t.Errorf("accepted: %s", line)
		}
	}
}
