package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
)

// Half an APNs key ends the process before anything connects, under the message it
// always had (the database is a closed port: a check behind db.Open would be
// outlasted by the start-up retries). The child gets only this environment.
func TestMainRefusesHalfAnAPNsKeyBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesHalfAnAPNsKeyBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://notify@127.0.0.1:1/ryft", "APNS_KEY_ID=ABC1234567"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"apns config"`) ||
		!strings.Contains(string(out), `"service":"notify"`) {
		t.Fatalf("want exit 1 with the apns config error, got %v:\n%s", err, out)
	}
}

// fakeBus is the broker as notify uses it: only the consumer.
type fakeBus struct {
	events.Bus
	consume func(ctx context.Context) error
}

func (b fakeBus) ConsumeNotify(ctx context.Context, _ *slog.Logger, _ func(context.Context, events.NotifyMessage) error) error {
	return b.consume(ctx)
}

// Without an APNs key notify still builds: it warns that nothing is sent, hands app
// the stale check as a job and the scored events as the consumer, whose result is the
// bus's (an error it returns stops the service).
func TestBuildWithoutAKeyRunsAndCountsWhatItWouldSend(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	fatal := errors.New("the broker is gone")
	parts, err := build(app.Deps{Log: log, Bus: fakeBus{consume: func(context.Context) error { return fatal }}}, pushSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Jobs) != 1 || parts.Jobs[0].Name != "stale check" || len(parts.Consumers) != 1 || parts.Consumers[0].Name != "scored events" {
		t.Fatalf("jobs %+v, consumers %+v", parts.Jobs, parts.Consumers)
	}
	if parts.Routes != nil || parts.Checks != nil {
		t.Fatal("notify serves health only, and app adds the db and broker checks")
	}
	if err := parts.Consumers[0].Run(context.Background()); !errors.Is(err, fatal) {
		t.Fatalf("the consumer returns the bus's error, got %v", err)
	}
	for _, want := range []string{`"msg":"consuming scored events"`, "APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8 not set"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("runbooks grep for %q: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "apns configured") {
		t.Fatal("no key, no 'apns configured'")
	}
}

// A key that does not parse is a build error under the message "apns", as it was.
func TestBuildRefusesAKeyThatDoesNotParse(t *testing.T) {
	push := pushSettings{on: true, cfg: config.APNs{KeyID: "ABC1234567", TeamID: "DEF1234567", PrivateKey: "not a key", Topic: config.DefaultAPNsTopic}}
	_, err := build(app.Deps{Log: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))}, push)
	var ce app.ConfigError
	if !errors.As(err, &ce) || ce.Msg != "apns" {
		t.Fatalf("want a ConfigError under \"apns\", got %v", err)
	}
}

func TestEveryRunsThePassUntilTheContextEnds(t *testing.T) {
	var passes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- every(2*time.Millisecond, func(context.Context) { passes.Add(1) })(ctx)
	}()
	for deadline := time.Now().Add(5 * time.Second); passes.Load() < 3; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d passes", passes.Load())
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a job that ends with its context returns nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not end with its context")
	}
	after := passes.Load()
	time.Sleep(20 * time.Millisecond)
	if passes.Load() != after {
		t.Fatal("a pass ran after the context ended")
	}
}
