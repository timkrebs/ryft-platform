package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestGuardedTurnsAPanicIntoAnError(t *testing.T) {
	var nilMap map[string]*int
	err := Guarded(quiet, KeyRefresh, func() error { return errors.New(strings.Repeat("x", 1) + fmtNil(*nilMap["x"])) })
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("a panic must become an error: %v", err)
	}
	if err := Guarded(quiet, KeyRefresh, func() error { return nil }); err != nil {
		t.Fatalf("no panic, no error: %v", err)
	}
}

func fmtNil(int) string { return "" }

type recorder struct {
	mu   sync.Mutex
	sent []Message
}

func (r *recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

// Every event decodes on its consumer's side to what was published, with a stable id
// (a consumer's idempotency key on RabbitMQ, the dedup id on NATS).
func TestEventsRoundTrip(t *testing.T) {
	r := &recorder{}
	p := Publisher{Sender: r}
	ctx := context.Background()
	user, session := uuid.New(), uuid.New()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.PublishIngested(ctx, model.IngestedEvent{UserID: user, Days: []string{"2026-10-21"}, Reason: "sync", RequestID: "r1"}, ""))
	must(p.PublishSessionCompleted(ctx, model.SessionCompletedEvent{UserID: user, SessionID: session, Day: "2026-10-20"}, "session:x"))
	must(p.PublishScored(ctx, model.ScoredEvent{UserID: user, Day: "2026-10-21"}))
	must(p.PublishSessionScored(ctx, model.SessionScoredEvent{UserID: user, SessionID: session, Day: "2026-10-20"}))
	must(p.PublishRefresh(ctx, model.RefreshEvent{UserID: user, Day: "2026-10-21"}))

	keys := []string{}
	for _, m := range r.sent {
		keys = append(keys, m.Key)
	}
	if strings.Join(keys, ",") != "ingested,session.completed,scored,session.scored,refresh" {
		t.Fatalf("keys %v", keys)
	}
	ing, err := DecodeIngested(r.sent[0].Key, r.sent[0].Body)
	if err != nil || ing.UserID != user || ing.Days[0] != "2026-10-21" || ing.Reason != "sync" || r.sent[0].ID != "" {
		t.Fatalf("ingested: %+v %v id %q", ing, err, r.sent[0].ID)
	}
	sess, err := DecodeIngested(r.sent[1].Key, r.sent[1].Body)
	if err != nil || sess.Reason != ReasonSessionCompleted || *sess.SessionID != session || sess.Days[0] != "2026-10-20" || r.sent[1].ID != "session:x" {
		t.Fatalf("session completed: %+v %v", sess, err)
	}
	for i, want := range []string{"scored", "session_scored", "refresh"} {
		m := r.sent[2+i]
		n, err := DecodeNotify(m.Key, m.Body)
		if err != nil || n.Reason() != want || m.Reason != want {
			t.Fatalf("%s: %+v %v", want, n, err)
		}
	}
	if r.sent[2].ID != "scored:"+user.String()+":2026-10-21" || r.sent[3].ID != "session-scored:"+session.String() {
		t.Fatalf("ids: %q %q", r.sent[2].ID, r.sent[3].ID)
	}
	if _, err := DecodeIngested(KeyScored, r.sent[2].Body); err == nil {
		t.Fatal("scoring must refuse notify's events")
	}
	if _, err := DecodeNotify(KeyScored, []byte(`{"user_id":"`+user.String()+`"}`)); err == nil {
		t.Fatal("a scored event without a day is bad")
	}
}

func TestOutcomes(t *testing.T) {
	if after, ok := Deferred(Defer(time.Millisecond)); !ok || after != time.Second {
		t.Fatalf("a deferral is at least a second: %v %v", after, ok)
	}
	if after, _ := Deferred(Defer(48 * time.Hour)); after != 24*time.Hour {
		t.Fatalf("and at most a day: %v", after)
	}
	if !IsPermanent(Permanent(errors.New("gone"))) || IsPermanent(errors.New("transient")) {
		t.Fatal("permanent errors are recognised")
	}
}

type fakeBus struct {
	Publisher
	name     string
	consumed chan string
	fail     error
	closed   int
}

func (f *fakeBus) ConsumeIngested(ctx context.Context, _ *slog.Logger, _ func(context.Context, model.IngestedEvent) error) error {
	f.consumed <- f.name
	if f.fail != nil {
		return f.fail
	}
	<-ctx.Done()
	return nil
}
func (f *fakeBus) ConsumeNotify(ctx context.Context, log *slog.Logger, _ func(context.Context, NotifyMessage) error) error {
	return f.ConsumeIngested(ctx, log, nil)
}
func (f *fakeBus) Ready(context.Context) error { return nil }
func (f *fakeBus) Close()                      { f.closed++ }

// The cutover's bus: publishing on one, consuming on all; one consumer failing stops
// the others; each bus closes once.
func TestCombine(t *testing.T) {
	sent := &recorder{}
	ch := make(chan string, 4)
	nats := &fakeBus{Publisher: Publisher{Sender: sent}, name: "nats", consumed: ch}
	amqp := &fakeBus{Publisher: Publisher{Sender: &recorder{}}, name: "amqp", consumed: ch}
	b := Combine(nats, nats, amqp)
	if err := b.PublishRefresh(context.Background(), model.RefreshEvent{Day: "2026-10-21"}); err != nil || len(sent.sent) != 1 {
		t.Fatalf("publishes go to the publishing bus: %v %d", err, len(sent.sent))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.ConsumeIngested(ctx, quiet, nil) }()
	got := map[string]bool{<-ch: true, <-ch: true}
	if !got["nats"] || !got["amqp"] {
		t.Fatalf("both consume: %v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	amqp.fail = errors.New("broken")
	if err := Combine(nats, nats, amqp).ConsumeNotify(context.Background(), quiet, nil); err == nil || err.Error() != "broken" {
		t.Fatalf("a failing consumer ends it: %v", err)
	}
	b.Close()
	if nats.closed != 1 || amqp.closed != 1 {
		t.Fatalf("each bus closes once: %d %d", nats.closed, amqp.closed)
	}
}
