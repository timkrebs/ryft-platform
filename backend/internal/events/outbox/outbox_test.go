package outbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// pool is a fresh, migrated database (RYFT_TEST_POSTGRES_URL, opt-in like the store tests).
func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server")
	}
	ctx := context.Background()
	root, err := db.Connect(ctx, admin, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	name := fmt.Sprintf("ryft_outboxtest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	p, err := db.Connect(ctx, u.String(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	if err := db.Migrate(ctx, p, quiet); err != nil {
		t.Fatal(err)
	}
	return p
}

// sink records what the relay sends and can fail on purpose.
type sink struct {
	mu   sync.Mutex
	sent []events.Message
	fail func(events.Message) error
}

func (s *sink) Send(_ context.Context, m events.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(m); err != nil {
			return err
		}
	}
	s.sent = append(s.sent, m)
	return nil
}

func (s *sink) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.sent))
	for i, m := range s.sent {
		out[i] = m.ID
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, o *Outbox) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); o.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func msg(i int) events.Message {
	m, _ := events.Ingested(model.IngestedEvent{UserID: uuid.New(), Days: []string{"2026-10-21"}, Reason: "sync"}, fmt.Sprintf("id-%02d", i))
	return m
}

func pending(t *testing.T, p *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Sent, confirmed, marked: and a kick wakes the relay without waiting for its poll.
func TestRelay(t *testing.T) {
	p := pool(t)
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	o.Poll = time.Hour // only kicks
	run(t, o)
	start := time.Now()
	for i := range 3 {
		if err := o.Send(context.Background(), msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three sent", func() bool { return len(s.ids()) == 3 })
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a kick must not wait for the poll: %s", time.Since(start))
	}
	if got := s.ids(); got[0] != "id-00" || got[2] != "id-02" {
		t.Fatalf("oldest first: %v", got)
	}
	waitFor(t, "marked", func() bool { return pending(t, p) == 0 })
	var user string
	_ = p.QueryRow(context.Background(), `SELECT user_id::text FROM outbox LIMIT 1`).Scan(&user)
	if _, err := uuid.Parse(user); err != nil {
		t.Fatalf("the row carries the event's user (for account deletion): %q", user)
	}
	if m := s.sent[0]; m.Key != events.KeyIngested || m.Reason != "sync" {
		t.Fatalf("relayed as written: %+v", m)
	}
	ev, err := events.DecodeIngested(s.sent[0].Key, s.sent[0].Body)
	if err != nil || ev.Days[0] != "2026-10-21" {
		t.Fatalf("the payload survives jsonb: %+v %v", ev, err)
	}
}

// A broker that refuses: the row stays, counts the attempt, and goes once it accepts,
// still in order.
func TestRetryInOrder(t *testing.T) {
	p := pool(t)
	var mu sync.Mutex
	down := true
	s := &sink{fail: func(events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		if down {
			return errors.New("broker down")
		}
		return nil
	}}
	o := New(p, "api", s, quiet)
	o.Poll = 20 * time.Millisecond
	for i := range 3 {
		if err := Insert(context.Background(), p, "api", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	run(t, o)
	waitFor(t, "an attempt recorded", func() bool {
		var a int
		_ = p.QueryRow(context.Background(), `SELECT attempts FROM outbox WHERE message_id = 'id-00'`).Scan(&a)
		return a >= 1
	})
	if len(s.ids()) != 0 || pending(t, p) != 3 {
		t.Fatalf("nothing marked while the broker is down: sent %v", s.ids())
	}
	mu.Lock()
	down = false
	mu.Unlock()
	waitFor(t, "all sent", func() bool { return len(s.ids()) == 3 })
	if got := s.ids(); got[0] != "id-00" || got[1] != "id-01" || got[2] != "id-02" {
		t.Fatalf("order kept across the outage: %v", got)
	}
}

// Two replicas drain one publisher's rows: each row is sent once.
func TestReplicasSendOnce(t *testing.T) {
	p := pool(t)
	s := &sink{}
	for i := range 200 {
		if err := Insert(context.Background(), p, "scoring", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		o := New(p, "scoring", s, quiet)
		o.Batch, o.Poll = 7, 10*time.Millisecond
		run(t, o)
	}
	waitFor(t, "all sent", func() bool { return pending(t, p) == 0 })
	seen := map[string]int{}
	for _, id := range s.ids() {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s sent %d times", id, n)
		}
	}
	if len(seen) != 200 {
		t.Fatalf("sent %d of 200", len(seen))
	}
}

// A row written in a transaction that rolls back never existed; another publisher's
// rows are not this relay's.
func TestTransactionAndPublisher(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Insert(ctx, tx, "ingest", msg(1)); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if err := Insert(ctx, p, "api", msg(2)); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	o.Poll = 10 * time.Millisecond
	run(t, o)
	time.Sleep(200 * time.Millisecond)
	if len(s.ids()) != 0 {
		t.Fatalf("ingest's relay sends neither the rolled-back row nor api's: %v", s.ids())
	}
	if err := Insert(ctx, p, "nobody", msg(3)); err == nil {
		t.Fatal("only the three publishers may write")
	}
}

// A row that keeps failing (no queue takes it) is set aside after MaxAttempts, and the
// rows after it go; old published rows are deleted.
func TestStuckAndClean(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	poison := msg(0)
	poison.Key = "nobody.listens"
	if err := Insert(ctx, p, "ingest", poison); err != nil {
		t.Fatal(err)
	}
	if err := Insert(ctx, p, "ingest", msg(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE outbox SET attempts = $1 WHERE routing_key = 'nobody.listens'`, MaxAttempts); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload, published_at) VALUES ('ingest', 'ingested', '{}', now() - interval '8 days')`); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	o.Poll = 10 * time.Millisecond
	run(t, o)
	waitFor(t, "the row after the stuck one", func() bool { return len(s.ids()) == 1 })
	if s.ids()[0] != "id-01" {
		t.Fatalf("the stuck row must be set aside: %v", s.ids())
	}
	o.clean(ctx)
	var old int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at < now() - interval '7 days'`).Scan(&old)
	if old != 0 {
		t.Fatal("published rows older than a week are deleted")
	}
}
