package amqpbus

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
)

// A dead letter replayed with scripts/dlq.sh reaches its handler as a redelivery, so
// scoring finishes a re-score its failed attempts left half-done (D112). Only the
// boolean true under exactly HeaderReplayed counts: any other value there is logged and
// ignored (a first delivery), and another spelling of the name is just another header.
// A replay counts once in the redeliveries, also when it is a retry as well.
func TestReplayMarker(t *testing.T) {
	const queue = "replay-unit"
	for _, c := range []struct {
		name        string
		headers     amqp.Table
		redelivered bool
		warned      bool
	}{
		{"no headers", nil, false, false},
		{"other headers", amqp.Table{"trace": "kept"}, false, false},
		{"the marker", amqp.Table{HeaderReplayed: true}, true, false},
		{"the marker on a retry", amqp.Table{HeaderReplayed: true, HeaderAttempt: int64(1)}, true, false},
		{"false", amqp.Table{HeaderReplayed: false}, false, true},
		{"a string", amqp.Table{HeaderReplayed: "yes-please"}, false, true},
		{"the string true", amqp.Table{HeaderReplayed: "true"}, false, true},
		{"a number", amqp.Table{HeaderReplayed: int64(1)}, false, true},
		{"no value", amqp.Table{HeaderReplayed: nil}, false, true},
		{"a table", amqp.Table{HeaderReplayed: amqp.Table{"v": true}}, false, true},
		{"another case", amqp.Table{"X-Ryft-Replayed": true}, false, false},
		{"another name", amqp.Table{"x-ryft-replay": true}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			before := testutil.ToFloat64(metrics.Redeliveries.WithLabelValues(queue))
			var got, called bool
			d := amqp.Delivery{Headers: c.headers, Type: events.KeyIngested}
			(&Bus{}).handle(context.Background(), log, queue, d, func(ctx context.Context, _ amqp.Delivery, _ *string) error {
				called, got = true, events.Redelivered(ctx)
				return nil
			})
			if !called {
				t.Fatal("the handler did not run")
			}
			if got != c.redelivered {
				t.Errorf("Redelivered = %v, want %v", got, c.redelivered)
			}
			counted, want := testutil.ToFloat64(metrics.Redeliveries.WithLabelValues(queue))-before, 0.0
			if c.redelivered {
				want = 1
			}
			if counted != want {
				t.Errorf("redeliveries counted %v, want %v", counted, want)
			}
			warned := strings.Contains(logs.String(), `"level":"WARN"`) && strings.Contains(logs.String(), HeaderReplayed)
			if warned != c.warned {
				t.Errorf("warned %v, want %v: %s", warned, c.warned, logs.String())
			}
			if strings.Contains(logs.String(), "yes-please") {
				t.Errorf("the marker's value is logged: %s", logs.String())
			}
		})
	}
}

// End to end against a broker: an event fails all its attempts and is dead-lettered;
// after the fix scripts/dlq.sh replays it. The replayed letter is a redelivery to its
// handler, with all its attempts again (HeaderAttempt is gone), without the dead
// letter's headers, and its retry tier's copy keeps the marker. Needs bash, python3 and
// curl (the script's own dependencies) besides the broker.
func TestReplayedLetterIsARedelivery(t *testing.T) {
	b := newBroker(t)
	for _, tool := range []string{"bash", "python3", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("scripts/dlq.sh needs %s", tool)
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "dlq.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	bus := b.bus("test")

	type seen struct {
		redelivered bool
		attempt     int
		headers     amqp.Table
	}
	var (
		mu    sync.Mutex
		got   []seen
		fixed bool // the cause is fixed: the replayed letter fails once more (a blip), then succeeds
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bus.consume(ctx, quiet, events.ConsumerScoring, func(ctx context.Context, d amqp.Delivery, _ *string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, seen{events.Redelivered(ctx), attemptOf(d), d.Headers})
			if !fixed || len(got) == MaxAttempts+1 {
				return errors.New("broken")
			}
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitFor := func(n int) {
		t.Helper()
		k := 0
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			mu.Lock()
			k = len(got)
			mu.Unlock()
			if k >= n {
				return
			}
		}
		t.Fatalf("handled %d times, want %d", k, n)
	}

	if err := bus.PublishIngested(context.Background(), ingested("2026-10-21"), "replay-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(MaxAttempts)
	if n := b.depth(DLQ(events.ConsumerScoring), 1); n != 1 {
		t.Fatalf("dead letters: %d, want 1", n)
	}
	mu.Lock()
	fixed = true
	mu.Unlock()

	m := *b.mgmt
	password, _ := m.User.Password()
	user := m.User.Username()
	m.User = nil
	cmd := exec.Command("bash", script, "replay", DLQ(events.ConsumerScoring), "all")
	cmd.Env = append(os.Environ(), "RABBITMQ_MGMT_URL="+strings.TrimRight(m.String(), "/"), "RABBITMQ_VHOST="+b.vhost,
		"RABBITMQ_USER="+user, "RABBITMQ_PASSWORD="+password)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "replayed 1, put back 0") {
		t.Fatalf("dlq.sh replay: %v\n%s", err, out)
	}

	waitFor(MaxAttempts + 2)
	if n := b.depth(events.ConsumerScoring, 0); n != 0 {
		t.Fatalf("handled in the end: queue %d", n)
	}
	if n := b.depth(DLQ(events.ConsumerScoring), 0); n != 0 {
		t.Fatalf("not dead-lettered again: %d", n)
	}
	time.Sleep(time.Duration(float64(RetryTiers[0].Delay) * scale * 4))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != MaxAttempts+2 {
		t.Fatalf("handled %d times, want %d", len(got), MaxAttempts+2)
	}
	for i, s := range got[:MaxAttempts] {
		if s.redelivered != (i > 0) || s.attempt != i || s.headers[HeaderReplayed] != nil {
			t.Errorf("before the replay, delivery %d: %+v", i, s)
		}
	}
	replay, retry := got[MaxAttempts], got[MaxAttempts+1]
	if !replay.redelivered || replay.attempt != 0 || replay.headers[HeaderReplayed] != true {
		t.Errorf("the replay is a redelivery with all its attempts: redelivered %v, attempt %d, marker %#v",
			replay.redelivered, replay.attempt, replay.headers[HeaderReplayed])
	}
	for k := range replay.headers {
		if k != HeaderReplayed && (strings.HasPrefix(k, "Ryft-") || strings.HasPrefix(k, "x-death") ||
			strings.HasPrefix(k, "x-first-death-") || strings.HasPrefix(k, "x-last-death-") || k == HeaderAttempt) {
			t.Errorf("the replay carries the dead letter's header %s", k)
		}
	}
	if !retry.redelivered || retry.attempt != 1 || retry.headers[HeaderReplayed] != true {
		t.Errorf("the replay's retry keeps the marker: redelivered %v, attempt %d, marker %#v",
			retry.redelivered, retry.attempt, retry.headers[HeaderReplayed])
	}
}
