package amqpbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
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

// A dead letter is filed under its event key only when its type matches and the queue's
// consumer is bound for that key; else under its routing key, which the publisher's topic
// permission limits (D99). So a forged type reaches neither the DLQ's Ryft-Subject (the
// key dlq.sh replays with) nor the DLQ metric's label.
func TestSubjectOf(t *testing.T) {
	for _, c := range []struct{ queue, exchange, typ, key, want string }{
		{events.ConsumerScoring, ExchangeEvents, events.KeyIngested, events.KeyIngested, events.KeyIngested},
		{events.ConsumerScoring, ExchangeRequeue, events.KeyIngested, events.ConsumerScoring, events.KeyIngested}, // a retry
		{events.ConsumerScoring, ExchangeEvents, events.KeySessionCompleted, events.KeySessionCompleted, events.KeySessionCompleted},
		{events.ConsumerScoring, ExchangeEvents, events.KeyScored, events.KeyIngested, events.KeyIngested},           // forged
		{events.ConsumerScoring, ExchangeEvents, events.KeySessionCompleted, events.KeyIngested, events.KeyIngested}, // forged, and bound
		{events.ConsumerScoring, ExchangeEvents, "anything", events.KeyIngested, events.KeyIngested},
		{events.ConsumerScoring, ExchangeEvents, "", events.KeyIngested, events.KeyIngested},
		{events.ConsumerNotify, ExchangeEvents, events.KeyRefresh, events.KeyRefresh, events.KeyRefresh},
		{events.ConsumerNotify, ExchangeRequeue, events.KeyIngested, events.ConsumerNotify, events.ConsumerNotify}, // not notify's
		{"elsewhere", ExchangeEvents, events.KeyIngested, "k", "k"},
	} {
		d := amqp.Delivery{Exchange: c.exchange, Type: c.typ, RoutingKey: c.key}
		if got := subjectOf(c.queue, d); got != c.want {
			t.Errorf("subjectOf(%s, %s, type %q, key %q) = %q, want %q", c.queue, c.exchange, c.typ, c.key, got, c.want)
		}
	}
	// Through handle: a delivery that does not decode is dead-lettered under that subject
	// (the publish fails here, no broker: counted as an error, under the same label).
	label := func(s string) float64 { return testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(s, "error")) }
	forged, routed := label("forged-type"), label(events.KeyIngested)
	d := amqp.Delivery{Type: "forged-type", RoutingKey: events.KeyIngested, Body: []byte("{}")} // not from ryft.events
	(&Bus{}).handle(context.Background(), quiet, events.ConsumerScoring, d, func(context.Context, amqp.Delivery, *string) error {
		return badEvent{errors.New("not an event for scoring")}
	})
	if label("forged-type") != forged || label(events.KeyIngested) != routed+1 {
		t.Errorf("the dead letter is counted under its type (%v) instead of its routing key (%v)",
			label("forged-type")-forged, label(events.KeyIngested)-routed)
	}
}

// From ryft.events a delivery is handled by its routing key, which the publisher's topic
// permission limits: a type that says otherwise (ingest, which may publish only `ingested`,
// calling its message session.completed) is a bad event, dead-lettered under the routing
// key without reaching the handler. A retry tier's copy (ryft.requeue, the queue's name as
// routing key) is handled by the type its first delivery was checked with.
func TestTypeMustMatchItsKey(t *testing.T) {
	for _, c := range []struct {
		exchange, typ, key string
		handled            bool
		as                 string
	}{
		{ExchangeEvents, events.KeyIngested, events.KeyIngested, true, events.KeyIngested},
		{ExchangeEvents, "", events.KeyIngested, true, events.KeyIngested},
		{ExchangeEvents, events.KeySessionCompleted, events.KeySessionCompleted, true, events.KeySessionCompleted},
		{ExchangeEvents, events.KeySessionCompleted, events.KeyIngested, false, events.KeyIngested},
		{ExchangeEvents, events.KeyScored, events.KeyIngested, false, events.KeyIngested},
		{ExchangeRequeue, events.KeySessionCompleted, events.ConsumerScoring, true, events.KeySessionCompleted},
		{ExchangeRequeue, "", events.ConsumerScoring, true, events.ConsumerScoring},
	} {
		d := amqp.Delivery{Exchange: c.exchange, Type: c.typ, RoutingKey: c.key, Body: []byte("{}")}
		if got := keyOf(d); got != c.as {
			t.Errorf("keyOf(%s, type %q, key %q) = %q, want %q", c.exchange, c.typ, c.key, got, c.as)
		}
		bad := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(c.as, "error"))
		called := false
		(&Bus{}).handle(context.Background(), quiet, events.ConsumerScoring, d, func(context.Context, amqp.Delivery, *string) error {
			called = true
			return nil
		})
		if called != c.handled {
			t.Errorf("%s, type %q, key %q: handled %v, want %v", c.exchange, c.typ, c.key, called, c.handled)
		}
		if got, want := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(c.as, "error"))-bad, map[bool]float64{true: 0, false: 1}[c.handled]; got != want {
			t.Errorf("%s, type %q, key %q: dead-lettered under %q %v times, want %v", c.exchange, c.typ, c.key, c.as, got, want)
		}
	}
}

// scripts/dlq.sh replays only the keys each dead-letter queue's consumer is bound for; its
// list is the topology's.
func TestDLQReplayKeysAreTheBindings(t *testing.T) {
	src, err := os.ReadFile(dlqScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*dlq\.([a-z]+)\) echo "([^"]*)" ;;$`).FindAllStringSubmatch(string(src), -1) {
		got[m[1]] = strings.Fields(m[2])
	}
	if len(got) != len(consumerKeys) {
		t.Fatalf("dlq.sh replay_keys names %v, the topology %v", got, consumerKeys)
	}
	for q, keys := range consumerKeys {
		if !slices.Equal(slices.Sorted(slices.Values(got[q])), slices.Sorted(slices.Values(keys))) {
			t.Errorf("dlq.sh replays %v from dlq.%s, its consumer is bound for %v", got[q], q, keys)
		}
	}
}

func dlqScriptPath(t *testing.T) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "dlq.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// dlqScript runs scripts/dlq.sh against b's vhost through the management API: never a
// cluster (no context, KUBECONFIG=/dev/null). Needs bash, python3 and curl, the script's
// own dependencies.
func dlqScript(t *testing.T, b *broker) func(args ...string) (string, error) {
	t.Helper()
	for _, tool := range []string{"bash", "python3", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("scripts/dlq.sh needs %s", tool)
		}
	}
	script := dlqScriptPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", os.DevNull)
	t.Setenv("KUBE_CONTEXT", "")
	m := *b.mgmt
	password, _ := m.User.Password()
	user := m.User.Username()
	m.User = nil
	env := append(os.Environ(), "RABBITMQ_MGMT_URL="+strings.TrimRight(m.String(), "/"), "RABBITMQ_VHOST="+b.vhost,
		"RABBITMQ_USER="+user, "RABBITMQ_PASSWORD="+password)
	return func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
}

// End to end against a broker: an event fails all its attempts and is dead-lettered.
// scripts/dlq.sh replays it: a redelivery to its handler, with all its attempts again
// (HeaderAttempt is gone) and without the dead letter's headers; every retry tier's copy
// keeps the marker, and when it fails them all it is back in the DLQ with the marker, which
// `dlq.sh list` shows. After the fix the next replay is handled.
func TestReplayedLetterIsARedelivery(t *testing.T) {
	b := newBroker(t)
	dlq := dlqScript(t, b)
	bus := b.bus("test")

	type seen struct {
		redelivered bool
		attempt     int
		headers     amqp.Table
	}
	var (
		mu    sync.Mutex
		got   []seen
		fixed bool
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bus.consume(ctx, quiet, events.ConsumerScoring, func(ctx context.Context, d amqp.Delivery, _ *string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, seen{events.Redelivered(ctx), attemptOf(d), d.Headers})
			if !fixed {
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
	// letters is `dlq.sh list`'s rows for dlq.scoring, once it holds want of them.
	letters := func(want int) []map[string]any {
		t.Helper()
		var rows []map[string]any
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			out, err := dlq("list")
			if err != nil {
				t.Fatalf("dlq.sh list: %v\n%s", err, out)
			}
			rows = nil
			in := false
			for _, l := range strings.Split(out, "\n") {
				switch {
				case strings.HasPrefix(l, "dlq.scoring: "):
					in = true
				case strings.HasPrefix(l, "dlq.notify: "):
					in = false
				case in && strings.HasPrefix(l, "{"):
					var r map[string]any
					if err := json.Unmarshal([]byte(l), &r); err != nil {
						t.Fatalf("dlq.sh list: %q: %v", l, err)
					}
					rows = append(rows, r)
				}
			}
			if len(rows) == want {
				return rows
			}
		}
		t.Fatalf("dlq.scoring holds %d letters, want %d", len(rows), want)
		return nil
	}
	replay := func() {
		t.Helper()
		out, err := dlq("replay", DLQ(events.ConsumerScoring), "all")
		if err != nil || !strings.Contains(out, "replayed 1, put back 0") {
			t.Fatalf("dlq.sh replay: %v\n%s", err, out)
		}
	}

	if err := bus.PublishIngested(context.Background(), ingested("2026-10-21"), "replay-1"); err != nil {
		t.Fatal(err)
	}
	waitFor(MaxAttempts)
	if r := letters(1)[0]; r["replayed_before"] != false || r["attempts"] != "6" || r["subject"] != events.KeyIngested {
		t.Fatalf("the first dead letter: %v", r)
	}
	replay() // the cause is not fixed yet: all six attempts fail again
	waitFor(2 * MaxAttempts)
	if r := letters(1)[0]; r["replayed_before"] != true || r["attempts"] != "6" || r["subject"] != events.KeyIngested {
		t.Fatalf("the replayed letter is back in the DLQ with the marker: %v", r)
	}
	mu.Lock()
	fixed = true
	mu.Unlock()
	replay()
	waitFor(2*MaxAttempts + 1)
	if n := b.depth(events.ConsumerScoring, 0); n != 0 {
		t.Fatalf("handled in the end: queue %d", n)
	}
	if n := b.depth(DLQ(events.ConsumerScoring), 0); n != 0 {
		t.Fatalf("not dead-lettered again: %d", n)
	}
	time.Sleep(time.Duration(float64(RetryTiers[0].Delay) * scale * 4))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2*MaxAttempts+1 {
		t.Fatalf("handled %d times, want %d", len(got), 2*MaxAttempts+1)
	}
	for i, s := range got[:MaxAttempts] {
		if s.redelivered != (i > 0) || s.attempt != i || s.headers[HeaderReplayed] != nil {
			t.Errorf("before the replay, delivery %d: %+v", i, s)
		}
	}
	for i, s := range got[MaxAttempts:] {
		if !s.redelivered || s.attempt != i%MaxAttempts || s.headers[HeaderReplayed] != true {
			t.Errorf("replayed delivery %d is a redelivery with the marker and attempt %d: redelivered %v, attempt %d, marker %#v",
				i, i%MaxAttempts, s.redelivered, s.attempt, s.headers[HeaderReplayed])
		}
	}
	for _, s := range []seen{got[MaxAttempts], got[2*MaxAttempts]} {
		for k := range s.headers {
			if k != HeaderReplayed && (strings.HasPrefix(k, "Ryft-") || strings.HasPrefix(k, "x-death") ||
				strings.HasPrefix(k, "x-first-death-") || strings.HasPrefix(k, "x-last-death-") || k == HeaderAttempt) {
				t.Errorf("a replay carries the dead letter's header %s", k)
			}
		}
	}
}

// A publisher may write only its own routing keys but sets the type freely. A message on
// `ingested` that says it is `scored` does not decode for scoring and is dead-lettered
// under its routing key; dlq.sh does not replay a letter whose type is not its key (as the
// management API's administrator, it would otherwise reach notify), it puts it back as it
// was.
func TestForgedTypeIsNotReplayed(t *testing.T) {
	b := newBroker(t)
	dlq := dlqScript(t, b)
	bus := b.bus("test")
	handled := make(chan struct{}, 4)
	consumeIngested(t, bus, func(context.Context, model.IngestedEvent) error { handled <- struct{}{}; return nil })
	before := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(events.KeyIngested, "ok"))
	forged := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(events.KeyScored, "ok"))
	body, _ := json.Marshal(model.ScoredEvent{UserID: uuid.New(), Day: "2026-10-21"})
	b.call(http.MethodPost, "/api/exchanges/"+b.vhost+"/"+ExchangeEvents+"/publish", map[string]any{
		"routing_key": events.KeyIngested, "payload": string(body), "payload_encoding": "string",
		"properties": map[string]any{"type": events.KeyScored, "message_id": "forged-1", "delivery_mode": 2, "content_type": "application/json"},
	})
	if n := b.depth(DLQ(events.ConsumerScoring), 1); n != 1 {
		t.Fatalf("dead letters: %d, want 1", n)
	}
	if got := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(events.KeyIngested, "ok")) - before; got != 1 {
		t.Errorf("counted under its routing key %v times, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.EventsDLQ.WithLabelValues(events.KeyScored, "ok")) - forged; got != 0 {
		t.Errorf("counted under the forged type %v times", got)
	}
	out, err := dlq("replay", "scoring")
	if err == nil || !strings.Contains(out, "replayed 0, put back 1") || !strings.Contains(out, `its type "scored" is not its key`) {
		t.Fatalf("dlq.sh replay of the forged letter: %v\n%s", err, out)
	}
	d, ok := b.get(DLQ(events.ConsumerScoring))
	if !ok {
		t.Fatal("the forged letter is not back in dlq.scoring")
	}
	if d.Headers[events.HeaderSubject] != events.KeyIngested || d.Type != events.KeyScored || d.MessageId != "forged-1" ||
		d.Headers[HeaderReplayed] != nil {
		t.Errorf("put back as it was: subject %v, type %q, id %q, marker %v", d.Headers[events.HeaderSubject], d.Type, d.MessageId, d.Headers[HeaderReplayed])
	}
	if n := b.depth(events.ConsumerNotify, 0); n != 0 {
		t.Errorf("notify got %d messages", n)
	}
	select {
	case <-handled:
		t.Error("scoring handled the forged message")
	default:
	}
}

// Against a broker: a message ingest may publish (`ingested`) that calls itself
// session.completed is not handled as a session; it is dead-lettered under `ingested`,
// with its type kept. A real session.completed, and its retry, are handled.
func TestTypeMustMatchItsKeyOnTheBroker(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	var mu sync.Mutex
	var got []model.IngestedEvent
	failed := false
	consumeIngested(t, bus, func(_ context.Context, ev model.IngestedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
		if ev.Reason == events.ReasonSessionCompleted && !failed {
			failed = true
			return errors.New("once") // its retry comes back through ryft.requeue
		}
		return nil
	})
	body, _ := json.Marshal(model.SessionCompletedEvent{UserID: uuid.New(), SessionID: uuid.New(), Day: "2026-10-21"})
	b.call(http.MethodPost, "/api/exchanges/"+b.vhost+"/"+ExchangeEvents+"/publish", map[string]any{
		"routing_key": events.KeyIngested, "payload": string(body), "payload_encoding": "string",
		"properties": map[string]any{"type": events.KeySessionCompleted, "message_id": "forged-session", "delivery_mode": 2},
	})
	d, ok := b.get(DLQ(events.ConsumerScoring))
	if !ok {
		t.Fatal("the forged session is not dead-lettered")
	}
	if d.Headers[events.HeaderSubject] != events.KeyIngested || d.Type != events.KeySessionCompleted || d.MessageId != "forged-session" {
		t.Errorf("dead letter: subject %v, type %q, id %q", d.Headers[events.HeaderSubject], d.Type, d.MessageId)
	}
	session := model.SessionCompletedEvent{UserID: uuid.New(), SessionID: uuid.New(), Day: "2026-10-20"}
	if err := bus.PublishSessionCompleted(context.Background(), session, "s1"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
	}
	time.Sleep(time.Duration(float64(RetryTiers[1].Delay) * scale * 2))
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want the real session and its retry, got %+v", got)
	}
	for _, ev := range got {
		if ev.Reason != events.ReasonSessionCompleted || ev.UserID != session.UserID || len(ev.Days) != 1 || ev.Days[0] != session.Day {
			t.Errorf("handled %+v, want the real session", ev)
		}
	}
}
