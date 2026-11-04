package app

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// lifecycleLogKeys is every line the lifecycle logs besides the services' own, with
// the keys it may carry and the kind of each value (D114, D117). A task's name, a
// step and a reason are closed sets written in code; an error is free text, capped,
// and carries a cause (a connection, a path), never a request. Any other line, key
// or kind of value fails TestLifecycleLogsKeepToTheirKeys, so a new line has to be
// listed here after the rule has been checked against it.
var lifecycleLogKeys = func() logcheck.Lines {
	task, errText := logcheck.Text(80), logcheck.Text(1024)
	lines := logcheck.Lines{
		"listening":                             {"addr": logcheck.Pattern(regexp.MustCompile(`^:[0-9]{1,5}$`))},
		"metrics listening":                     {"addr": logcheck.Pattern(regexp.MustCompile(`^:[0-9]{1,5}$`))},
		"start interrupted by the signal":       {"step": logcheck.Enum("config", "db", "events", "build", "serve")},
		"metrics server":                        {"err": errText},
		"task ended with error during shutdown": {"task": task, "err": errText},
		"panic":                                 {"task": task, "check": logcheck.Enum("db", "auth", "broker"), "panic": errText, "stack": logcheck.Text(16384)},
		"shutting down":                         {"reason": logcheck.Enum("signal", "task", "start"), "task": task, "err": errText},
		"shutdown step": {"step": logcheck.Enum("drain", "consumers", "http", "jobs", "relay", "broker", "pool", "metrics"),
			"dur_ms": logcheck.Count, "err": errText},
		"shutdown overdue": {"step": logcheck.Enum("begin", "drain", "consumers", "http", "jobs", "relay", "broker", "pool", "metrics"),
			"elapsed_ms": logcheck.Count},
		"shut down": {"dur_ms": logcheck.Count, "exit": logcheck.CountMax(2)},
	}
	// A failed start logs its error under the area's message; a failed task under its tier's.
	for _, msg := range []string{"config", "beta config", "apns config", "apns", "db", "events", "build", "serve"} {
		lines[msg] = logcheck.Keys{"err": errText, "task": task}
	}
	for _, msg := range []string{"job", "consume", "serve", "background", "metrics"} {
		lines[msg] = logcheck.Keys{"task": task, "err": errText}
	}
	return lines
}()

// TestLifecycleLogsKeepToTheirKeys runs a service the ways it ends (a signal, a failing
// consumer, a panicking job, a stuck step, a failed start) as scoring and notify log
// them, and checks every line app wrote against lifecycleLogKeys.
func TestLifecycleLogsKeepToTheirKeys(t *testing.T) {
	boom := errors.New("connection refused")
	var all strings.Builder
	record := func(g *rig) { all.WriteString(g.logs.text() + "\n") }
	asScoring := func(g *rig) {
		g.e.logger = func(string) *slog.Logger { return logx.NewWriter(g.logs, "scoring", slog.LevelDebug) }
	}

	t.Run("a signal", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.parts = fullParts(g)
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, g.spec())
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		record(g)
	})
	t.Run("a consumer that fails", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.parts.Consumers = []Task{{Name: "ingested events", Run: func(context.Context) error { return boom }}}
		if err := result(t, g.run(context.Background(), g.spec())); !errors.Is(err, boom) {
			t.Fatalf("Run = %v", err)
		}
		record(g)
	})
	t.Run("a job that panics", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.parts.Jobs = []Task{{Name: "scoring pass", Run: func(context.Context) error { panic("kaboom") }}}
		if err := result(t, g.run(context.Background(), g.spec())); err == nil {
			t.Fatal("a panic is a failure")
		}
		record(g)
	})
	t.Run("a step that overruns", func(t *testing.T) {
		g := newRig()
		asScoring(g)
		g.e.bounds.consumerMargin = 20 * time.Millisecond
		release := make(chan struct{})
		g.parts.Consumers = []Task{{Name: "ingested events", Run: func(context.Context) error { <-release; return nil }}}
		s := g.spec()
		s.ConsumerDrain = 20 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
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
		release := make(chan struct{})
		g.parts.Consumers = []Task{{Name: "ingested events", Run: func(context.Context) error { <-release; return nil }}}
		s := g.spec()
		s.ConsumerDrain = 5 * time.Second
		s.Grace = watchdogMargin + 100*time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		cancel()
		eventually(t, func() bool { return len(g.exitCodes()) == 1 }, "the watchdog")
		close(release)
		_ = result(t, done)
		record(g)
	})
	for name, set := range map[string]func(g *rig){
		"the database":  func(g *rig) { g.poolErr = boom },
		"the broker":    func(g *rig) { g.busErr = boom },
		"build":         func(g *rig) { g.buildErr = boom },
		"the HTTP port": func(g *rig) { g.httpErr = boom },
		"the settings":  func(g *rig) { g.loadErr = ConfigError{Msg: "beta config", Err: boom} },
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
	// The lines the runbooks and the Insights queries are written for were all seen.
	for _, msg := range []string{"shutting down", "shutdown step", "shut down", "shutdown overdue", "panic", "consume", "listening", "metrics listening", "serve", "db", "events", "build"} {
		if len(lines[msg]) == 0 {
			t.Errorf("no %q line in any run", msg)
		}
	}
}

// TestLifecycleLogKindsRejectMutations is the check on the check: a line with a key it
// may not carry, a reason outside the closed set or an exit code that is not one fails.
func TestLifecycleLogKindsRejectMutations(t *testing.T) {
	std := logcheck.Standard("scoring")
	for _, line := range []string{
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutting down","reason":"because"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutting down","reason":"signal","recovery":61.7}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shut down","dur_ms":12,"exit":"0"}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"shutdown step","step":"cache","dur_ms":12}`,
		`{"time":"2026-11-04T10:00:00Z","level":"INFO","service":"scoring","msg":"a new line"}`,
	} {
		if _, problems := logcheck.Check(line, std, lifecycleLogKeys, logcheck.Forbidden{}); len(problems) == 0 {
			t.Errorf("accepted: %s", line)
		}
	}
}
