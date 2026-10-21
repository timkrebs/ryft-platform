package amqpbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
)

// Integration tests, opt-in: a RabbitMQ 4 with the management plugin and an
// administrator (CI's service container; locally e.g.
//
//	podman run -d --rm -p 55672:5672 -p 55673:15672 -e RABBITMQ_DEFAULT_USER=admin \
//	  -e RABBITMQ_DEFAULT_PASS=admin rabbitmq:4.3.6-management
//	RYFT_TEST_AMQP_URL=amqp://admin:admin@localhost:55672/ RYFT_TEST_AMQP_MGMT=http://admin:admin@localhost:55673 go test ./internal/events/amqpbus/
//
// Each test gets a fresh vhost with the real topology at a hundredth of its TTLs.

const scale = 0.01

var quiet = testLogger()

func testLogger() *slog.Logger {
	if os.Getenv("RYFT_TEST_VERBOSE") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type broker struct {
	t     *testing.T
	amqp  *url.URL
	mgmt  *url.URL
	vhost string
}

func newBroker(t *testing.T) *broker {
	t.Helper()
	a, m := os.Getenv("RYFT_TEST_AMQP_URL"), os.Getenv("RYFT_TEST_AMQP_MGMT")
	if a == "" || m == "" {
		t.Skip("set RYFT_TEST_AMQP_URL and RYFT_TEST_AMQP_MGMT to a RabbitMQ 4 with management (see the comment above)")
	}
	au, err := url.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	mu, err := url.Parse(m)
	if err != nil {
		t.Fatal(err)
	}
	b := &broker{t: t, amqp: au, mgmt: mu, vhost: fmt.Sprintf("ryfttest-%08x", rand.Uint32())}
	b.call(http.MethodPut, "/api/vhosts/"+b.vhost, map[string]any{"default_queue_type": "quorum"})
	b.call(http.MethodPut, "/api/permissions/"+b.vhost+"/"+au.User.Username(), map[string]string{"configure": ".*", "write": ".*", "read": ".*"})
	defs := VhostDefinitions(Spec(scale))
	for _, k := range []string{"exchanges", "queues", "bindings", "policies"} {
		for _, o := range defs[k].([]map[string]any) {
			o["vhost"] = b.vhost
		}
	}
	b.call(http.MethodPost, "/api/definitions/"+b.vhost, defs)
	t.Cleanup(func() { b.call(http.MethodDelete, "/api/vhosts/"+b.vhost, nil) })
	return b
}

// call is the management API; a non-2xx answer fails the test.
func (b *broker) call(method, path string, body any) []byte {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		r = bytes.NewReader(j)
	}
	u := *b.mgmt
	u.RawPath = path // path is escaped already (a connection's name has spaces)
	u.Path, _ = url.PathUnescape(path)
	req, _ := http.NewRequest(method, u.String(), r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		b.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, out)
	}
	return out
}

func (b *broker) url() string {
	u := *b.amqp
	u.Path = "/" + b.vhost
	return u.String()
}

func (b *broker) bus(name string) *Bus {
	b.t.Helper()
	bus, err := Open(context.Background(), Options{URL: b.url(), Name: name, ConfirmTimeout: 5 * time.Second}, quiet)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(bus.Close)
	return bus
}

// depth is a queue's message count, waiting until it reaches want (the management
// API's numbers lag a little).
func (b *broker) depth(queue string, want int) int {
	b.t.Helper()
	got := -1
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		var q struct {
			Messages int `json:"messages"`
		}
		_ = json.Unmarshal(b.call(http.MethodGet, "/api/queues/"+b.vhost+"/"+url.PathEscape(queue), nil), &q)
		if got = q.Messages; got == want {
			return got
		}
	}
	return got
}

// get takes one message off queue (as the administrator).
func (b *broker) get(queue string) (amqp.Delivery, bool) {
	b.t.Helper()
	conn, err := amqp.Dial(b.url())
	if err != nil {
		b.t.Fatal(err)
	}
	defer conn.Close()
	ch, _ := conn.Channel()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		d, ok, err := ch.Get(queue, true)
		if err != nil {
			b.t.Fatal(err)
		}
		if ok {
			return d, true
		}
	}
	return amqp.Delivery{}, false
}

func ingested(day string) model.IngestedEvent {
	return model.IngestedEvent{UserID: uuid.New(), Days: []string{day}, Reason: "sync", RequestID: "req-" + day}
}

// consume runs ConsumeIngested with fn until the test ends.
func consumeIngested(t *testing.T, bus *Bus, fn func(context.Context, model.IngestedEvent) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = bus.ConsumeIngested(ctx, quiet, fn) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestPublishAndConsume(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	got := make(chan model.IngestedEvent, 2)
	consumeIngested(t, bus, func(_ context.Context, ev model.IngestedEvent) error { got <- ev; return nil })
	ev := ingested("2026-10-21")
	if err := bus.PublishIngested(context.Background(), ev, ""); err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishSessionCompleted(context.Background(), model.SessionCompletedEvent{UserID: ev.UserID, SessionID: uuid.New(), Day: "2026-10-20"}, "s1"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sync", events.ReasonSessionCompleted} {
		select {
		case e := <-got:
			if e.Reason != want || e.UserID != ev.UserID {
				t.Fatalf("got %+v, want reason %s", e, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s event", want)
		}
	}
	if n := b.depth("scoring", 0); n != 0 {
		t.Fatalf("acknowledged, so the queue is empty: %d", n)
	}
}

// mandatory: an event no queue is bound for is an error at the publisher.
func TestUnroutable(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	err := bus.Send(context.Background(), events.Message{Key: "nobody.listens", Body: []byte(`{}`), Reason: "test"})
	if !errors.Is(err, ErrUnroutable) {
		t.Fatalf("want ErrUnroutable, got %v", err)
	}
	if err := bus.PublishIngested(context.Background(), ingested("2026-10-21"), ""); err != nil {
		t.Fatalf("the channel is still usable: %v", err)
	}
}

// A full queue refuses the publish (reject-publish): the confirm is a nack. A quorum
// queue checks its length as it applies a publish, so one more may get in first.
func TestConfirmRefused(t *testing.T) {
	b := newBroker(t)
	b.call(http.MethodPut, "/api/policies/"+b.vhost+"/work-scoring", map[string]any{"pattern": "^scoring$", "apply-to": "quorum_queues",
		"priority": 10, "definition": map[string]any{"max-length": 1, "overflow": "reject-publish"}})
	time.Sleep(500 * time.Millisecond)
	bus := b.bus("test")
	var refused error
	accepted := 0
	for i := 0; i < 5 && refused == nil; i++ {
		if err := bus.PublishIngested(context.Background(), ingested(fmt.Sprintf("2026-10-%02d", i+1)), ""); err != nil {
			refused = err
		} else {
			accepted++
		}
	}
	if refused == nil || !strings.Contains(refused.Error(), "nack") {
		t.Fatalf("publishes into a full queue must fail with a nack: %v", refused)
	}
	if accepted > 2 {
		t.Fatalf("%d publishes accepted into a queue of one", accepted)
	}
}

// A failing handler is retried through the tiers in order, spaced by their TTLs.
func TestRetryTiers(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	var mu sync.Mutex
	var at []time.Time
	done := make(chan struct{})
	consumeIngested(t, bus, func(context.Context, model.IngestedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		at = append(at, time.Now())
		if len(at) < 4 {
			return errors.New("not yet")
		}
		close(done)
		return nil
	})
	if err := bus.PublishIngested(context.Background(), ingested("2026-10-21"), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("handled %d times", len(at))
	}
	mu.Lock()
	defer mu.Unlock()
	for i, tier := range []Tier{RetryTiers[0], RetryTiers[1], RetryTiers[2]} {
		gap, want := at[i+1].Sub(at[i]), time.Duration(float64(tier.Delay)*scale)
		if gap < want*8/10 {
			t.Errorf("retry %d after %s, the tier %s (scaled) is %s", i+1, gap, tier.Name, want)
		}
	}
	if n := b.depth("scoring", 0); n != 0 {
		t.Fatalf("handled in the end: queue %d", n)
	}
}

// After MaxAttempts the message is dead-lettered with the reason in its headers.
func TestLastAttemptDeadLetters(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	var mu sync.Mutex
	n := 0
	consumeIngested(t, bus, func(context.Context, model.IngestedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Errorf("broken %d", n)
	})
	ev := ingested("2026-10-21")
	if err := bus.PublishIngested(context.Background(), ev, "dedup-1"); err != nil {
		t.Fatal(err)
	}
	d, ok := b.get(DLQ(events.ConsumerScoring))
	if !ok {
		t.Fatal("nothing in dlq.scoring")
	}
	mu.Lock()
	tries := n
	mu.Unlock()
	if tries != MaxAttempts {
		t.Fatalf("handled %d times, want %d", tries, MaxAttempts)
	}
	if d.Headers[events.HeaderSubject] != events.KeyIngested || d.Headers[events.HeaderConsumer] != "scoring" ||
		d.Headers[events.HeaderDeliveries] != fmt.Sprint(MaxAttempts) || d.Headers[events.HeaderError] != fmt.Sprintf("broken %d", MaxAttempts) ||
		d.MessageId != "dedup-1" || d.Type != events.KeyIngested {
		t.Fatalf("dead letter %v id %q type %q", d.Headers, d.MessageId, d.Type)
	}
	back, err := events.DecodeIngested(d.Type, d.Body)
	if err != nil || back.UserID != ev.UserID {
		t.Fatalf("the dead letter keeps the event: %+v %v", back, err)
	}
}

// Permanent errors are dropped, not dead-lettered (D97); a message that does not
// decode goes to the DLQ at once.
func TestPermanentAndBad(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	calls := make(chan struct{}, 4)
	consumeIngested(t, bus, func(context.Context, model.IngestedEvent) error {
		calls <- struct{}{}
		return events.Permanent(errors.New("the user is gone"))
	})
	if err := bus.PublishIngested(context.Background(), ingested("2026-10-21"), ""); err != nil {
		t.Fatal(err)
	}
	<-calls
	if err := bus.Send(context.Background(), events.Message{Key: events.KeyIngested, Body: []byte("not json"), Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	d, ok := b.get(DLQ(events.ConsumerScoring))
	if !ok || string(d.Body) != "not json" {
		t.Fatalf("the bad message is dead-lettered: %v %q", ok, d.Body)
	}
	if got := b.depth(DLQ(events.ConsumerScoring), 0); got != 0 {
		t.Fatalf("the permanent failure must not be dead-lettered too: %d left", got)
	}
	select {
	case <-calls:
		t.Fatal("a permanent failure is not retried")
	case <-time.After(time.Duration(float64(RetryTiers[1].Delay) * scale * 3)):
	}
}

// notify's deferral (quiet hours) parks the message in a wait tier; it is not a
// failure and does not count as an attempt.
func TestDefer(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("test")
	var mu sync.Mutex
	var at []time.Time
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = bus.ConsumeNotify(ctx, quiet, func(context.Context, events.NotifyMessage) error {
			mu.Lock()
			defer mu.Unlock()
			at = append(at, time.Now())
			if len(at) == 1 {
				return events.Defer(16 * time.Minute) // the 15m tier
			}
			close(done)
			return nil
		})
	}()
	if err := bus.PublishScored(ctx, model.ScoredEvent{UserID: uuid.New(), Day: "2026-10-21"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the deferred message did not come back")
	}
	mu.Lock()
	defer mu.Unlock()
	if gap, want := at[1].Sub(at[0]), time.Duration(float64(15*time.Minute)*scale); gap < want*8/10 {
		t.Fatalf("back after %s, the 15m tier (scaled) is %s", gap, want)
	}
}

func TestTiers(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Second: "notify.retry.5s", 40 * time.Second: "notify.retry.30s",
		2 * time.Minute: "notify.wait.1m", 6 * time.Minute: "notify.retry.5m", 3 * time.Hour: "notify.wait.1h"} {
		if got := RetryQueue(events.ConsumerNotify, waitTierFor(events.ConsumerNotify, d)); got != want {
			t.Errorf("defer %s → %s, want %s", d, got, want)
		}
	}
	for attempt, want := range map[int]string{1: "5s", 2: "30s", 3: "5m", 5: "5m"} {
		if got := tierFor(attempt).Name; got != want {
			t.Errorf("attempt %d → %s, want %s", attempt, got, want)
		}
	}
	if err := Spec(1).Validate(); err != nil {
		t.Fatal(err)
	}
}

// The broker drops the connection mid-stream (a node restart, a network cut): the bus
// reconnects, the consumer resubscribes, and nothing published afterwards is lost.
func TestReconnect(t *testing.T) {
	b := newBroker(t)
	bus := b.bus("reconnect-test")
	got := make(chan string, 8)
	consumeIngested(t, bus, func(_ context.Context, ev model.IngestedEvent) error { got <- ev.Days[0]; return nil })
	if err := bus.PublishIngested(context.Background(), ingested("before"), ""); err != nil {
		t.Fatal(err)
	}
	if d := <-got; d != "before" {
		t.Fatal(d)
	}
	var conns []struct {
		Name   string `json:"name"`
		Client struct {
			Name string `json:"connection_name"`
		} `json:"client_properties"`
	}
	bus.mu.Lock()
	before := bus.conn
	bus.mu.Unlock()
	closed := 0
	// The management API lists a connection once its stats are collected (seconds).
	for deadline := time.Now().Add(20 * time.Second); closed == 0 && time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		_ = json.Unmarshal(b.call(http.MethodGet, "/api/vhosts/"+b.vhost+"/connections", nil), &conns)
		for _, c := range conns {
			if c.Client.Name == "reconnect-test" {
				b.call(http.MethodDelete, "/api/connections/"+url.PathEscape(c.Name), nil)
				closed++
			}
		}
	}
	if closed == 0 {
		t.Fatal("found no connection to close")
	}
	// Reconnected: a new connection, and ready (the gap may be a few milliseconds).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		now := bus.conn
		bus.mu.Unlock()
		if now != nil && now != before && bus.Ready(context.Background()) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := bus.Ready(context.Background()); err != nil {
		t.Fatalf("not reconnected: %v", err)
	}
	perr := errors.New("not tried")
	for ; perr != nil && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		perr = bus.PublishIngested(context.Background(), ingested("after"), "")
	}
	if perr != nil {
		t.Fatalf("publishing after the reconnect: %v", perr)
	}
	select {
	case d := <-got:
		if d != "after" {
			t.Fatalf("got %s", d)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the consumer did not resubscribe")
	}
}

// A real broker restart mid-stream, opt-in (it restarts the broker under test):
//
//	RYFT_TEST_AMQP_RESTART="podman restart rmqtest" go test -run TestBrokerRestart ./internal/events/amqpbus/
//
// Messages published before the restart survive it (quorum queues on disk), the bus
// reconnects while the broker is down for seconds, and the consumer gets them all.
func TestBrokerRestart(t *testing.T) {
	cmd := os.Getenv("RYFT_TEST_AMQP_RESTART")
	if cmd == "" {
		t.Skip("set RYFT_TEST_AMQP_RESTART to a command that restarts the broker under test")
	}
	b := newBroker(t)
	pub := b.bus("restart-publisher")
	for i := range 20 {
		if err := pub.PublishIngested(context.Background(), ingested(fmt.Sprintf("before-%02d", i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", cmd, err, out)
	}
	sub := b.bus("restart-consumer") // Open retries until the broker is back
	var mu sync.Mutex
	seen := map[string]bool{}
	consumeIngested(t, sub, func(_ context.Context, ev model.IngestedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		seen[ev.Days[0]] = true
		return nil
	})
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := pub.PublishIngested(context.Background(), ingested("after"), ""); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the publisher did not reconnect: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n == 21 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("got %d of 21 messages after the restart", len(seen))
}
