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

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
)

// A bad setting ends the process before anything connects, under the message it
// always had: the database is a closed port, so a check behind db.Open would be
// outlasted by the start-up retries. The child gets only this environment.
func TestMainRefusesBadSettingsBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesBadSettingsBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://scoring@127.0.0.1:1/ryft", "CONSENT_ENFORCE=maybe"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"beta config"`) ||
		!strings.Contains(string(out), `"service":"scoring"`) {
		t.Fatalf("want exit 1 with the beta config error, got %v:\n%s", err, out)
	}
}

// fakeBus is the broker as scoring uses it: only the consumer.
type fakeBus struct {
	events.Bus
	consume func(ctx context.Context) error
}

func (b fakeBus) ConsumeIngested(ctx context.Context, _ *slog.Logger, _ func(context.Context, model.IngestedEvent) error) error {
	return b.consume(ctx)
}

// build hands app one job (the timer's pass) and one consumer (the ingest queue), and
// the consumer's result is the bus's: an error it returns stops the service.
func TestBuildPartsAreTheTimerAndTheConsumer(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	fatal := errors.New("the broker is gone")
	parts, err := build(app.Deps{Log: log, Bus: fakeBus{consume: func(context.Context) error { return fatal }}}, config.Beta{Consent: config.ModeOn})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Jobs) != 1 || parts.Jobs[0].Name != "scoring pass" || len(parts.Consumers) != 1 || parts.Consumers[0].Name != "ingested events" {
		t.Fatalf("jobs %+v, consumers %+v", parts.Jobs, parts.Consumers)
	}
	if parts.Routes != nil || parts.Checks != nil {
		t.Fatal("scoring serves health only, and app adds the db and broker checks")
	}
	if err := parts.Consumers[0].Run(context.Background()); !errors.Is(err, fatal) {
		t.Fatalf("the consumer returns the bus's error, got %v", err)
	}
	if !strings.Contains(logs.String(), `"msg":"consuming ingest events"`) {
		t.Fatalf("runbooks grep for the consuming line: %s", logs.String())
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

// The pass over the active users stops between two users when the shutdown starts,
// instead of logging one cancelled query per user left.
func TestEachUserStopsWhenTheContextEnds(t *testing.T) {
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []int
	eachUser(ctx, users, func(_ context.Context, i int, id uuid.UUID) {
		if id != users[i] {
			t.Errorf("user %d is %v", i, id)
		}
		seen = append(seen, i)
		if i == 1 {
			cancel()
		}
	})
	if len(seen) != 2 {
		t.Fatalf("users %v, want 0 and 1 then stop", seen)
	}
	eachUser(ctx, users, func(context.Context, int, uuid.UUID) { t.Fatal("no user once the context has ended") })

	var all int
	eachUser(context.Background(), users, func(context.Context, int, uuid.UUID) { all++ })
	if all != len(users) {
		t.Fatalf("a live context visits every user, got %d", all)
	}
}
