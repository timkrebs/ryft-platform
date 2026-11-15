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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/testsupport"
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
	// The roles first: tests of other packages migrate on this server at the same time.
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
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

// asRole is a pool on p's database logged in as a service role.
func asRole(t *testing.T, p *pgxpool.Pool, role string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pw := testsupport.RolePassword(role)
	if err := testsupport.WithRoleLock(ctx, func() error { return db.SetRolePassword(ctx, p, role, pw) }); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(p.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)
	rp, err := db.Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	return rp
}

// sink records what the relay sends and can fail on purpose. Like a transport, it
// gives up once its ctx has ended.
type sink struct {
	mu     sync.Mutex
	sent   []events.Message
	fail   func(events.Message) error // runs under mu; nil accepts everything
	before func(context.Context)      // runs first, outside mu: a slow, hanging or racing transport
}

func (s *sink) Send(ctx context.Context, m events.Message) error {
	if s.before != nil {
		s.before(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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

func msgFor(user uuid.UUID, id string) events.Message {
	m, _ := events.Ingested(model.IngestedEvent{UserID: user, Days: []string{"2026-10-21"}, Reason: "sync"}, id)
	return m
}

func msg(i int) events.Message { return msgFor(uuid.New(), fmt.Sprintf("id-%02d", i)) }

// refresh is one of notify's events (the kind it defers).
func refresh(day string) events.Message {
	m, _ := events.Refresh(model.RefreshEvent{UserID: uuid.New(), Day: day})
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

func attempts(t *testing.T, p *pgxpool.Pool, messageID string) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), `SELECT attempts FROM outbox WHERE message_id = $1`, messageID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func equal(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// holdRelayLock is another instance mid-drain: a transaction holding publisher's relay
// lock. The test's cleanup rolls it back, so a failing assertion does not leave it held.
func holdRelayLock(t *testing.T, p *pgxpool.Pool, publisher string) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, relayLock, publisher); err != nil {
		t.Fatal(err)
	}
	return tx
}

// waitForRelayWaiter polls until a session waits for the relay lock, failing if the
// drain behind done finishes first or nobody waits within 10 s.
func waitForRelayWaiter(t *testing.T, p *pgxpool.Pool, done <-chan struct{}) {
	t.Helper()
	q := fmt.Sprintf(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = %d
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, relayLockClass)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var n int
		if err := p.QueryRow(context.Background(), q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		select {
		case <-done:
			t.Fatal("the flush finished while another drain held the relay lock")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("nobody waits for the relay lock")
		}
	}
}

// --- without Postgres ---

func TestLockTimeout(t *testing.T) {
	for _, c := range []struct {
		left time.Duration
		want string
	}{
		{2 * time.Second, "1800ms"},
		{45 * time.Second, "40500ms"},
		{100 * time.Millisecond, "90ms"},
		{time.Millisecond, "1ms"}, // 0 would turn the timeout off
		{0, "1ms"},
		{-time.Second, "1ms"},
	} {
		if got := lockTimeout(c.left); got != c.want {
			t.Errorf("lockTimeout(%s) = %q, want %q", c.left, got, c.want)
		}
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 512, "short"},
		{"abcdef", 3, "abc"},
		{"abé", 3, "ab"}, // é is two bytes: cut in half it would be refused by Postgres
		{"abé", 4, "abé"},
	} {
		if got := truncate(c.s, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// The tick's sweep keeps the end of its budget for the gauges: 2 s of a long budget, a
// quarter of a short one; without a deadline there is nothing to keep.
func TestReserve(t *testing.T) {
	for _, c := range []struct {
		budget, kept time.Duration
	}{
		{45 * time.Second, 2 * time.Second},
		{400 * time.Millisecond, 100 * time.Millisecond},
	} {
		parent, cancel := context.WithTimeout(context.Background(), c.budget)
		pdl, _ := parent.Deadline()
		child, stop := reserve(parent)
		dl, ok := child.Deadline()
		stop()
		cancel()
		if kept := pdl.Sub(dl); !ok || kept < c.kept-20*time.Millisecond || kept > c.kept {
			t.Errorf("budget %s: the drain ends %s before the sweep, want %s", c.budget, kept, c.kept)
		}
	}
	child, stop := reserve(context.Background())
	defer stop()
	if _, ok := child.Deadline(); ok {
		t.Error("no deadline, no reserve")
	}
}

type ctxKey struct{}

// Flush's context outlives the request's cancel, keeps its values (the log fields)
// and ends after the budget.
func TestDetach(t *testing.T) {
	req, hangUp := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "req-1"))
	hangUp()
	ctx, cancel := detach(req, 2*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		t.Fatal("a client that hung up does not end the flush")
	}
	if ctx.Value(ctxKey{}) != "req-1" {
		t.Fatal("the request's values stay")
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > 2*time.Second {
		t.Fatalf("the budget bounds it: %v %v", dl, ok)
	}
}

// Without a transport (tests that never relay) a flush is a no-op, not a panic.
func TestNilSenderRelaysNothing(t *testing.T) {
	o := New(nil, "ingest", nil, quiet)
	o.Flush(context.Background())
	if o.Batch != 50 || o.Poll != 2*time.Second || o.FlushBudget != 2*time.Second {
		t.Fatalf("defaults: %+v", o)
	}
}

func TestRelayLockClass(t *testing.T) {
	if !strings.Contains(relayLock, "pg_advisory_xact_lock("+strconv.Itoa(relayLockClass)+",") {
		t.Fatalf("the lock statement and its class disagree: %q", relayLock)
	}
}

// --- against Postgres ---

// Send relays before it returns (the inline flush): sent, confirmed, marked.
func TestSendRelaysInline(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	for i := range 3 {
		if err := o.Send(ctx, msg(i)); err != nil {
			t.Fatal(err)
		}
		if n := len(s.ids()); n != i+1 {
			t.Fatalf("Send relays before it returns: %d sent after %d", n, i+1)
		}
	}
	if got := s.ids(); !equal(got, "id-00", "id-01", "id-02") {
		t.Fatalf("oldest first: %v", got)
	}
	if n := pending(t, p); n != 0 {
		t.Fatalf("%d rows not marked", n)
	}
	var user string
	_ = p.QueryRow(ctx, `SELECT user_id::text FROM outbox LIMIT 1`).Scan(&user)
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

// A client that hangs up after the commit does not skip the relay; the send runs within
// the budget and with the request's values.
func TestFlushOutlivesTheRequest(t *testing.T) {
	p := pool(t)
	if err := Insert(context.Background(), p, "api", msg(0)); err != nil {
		t.Fatal(err)
	}
	var left time.Duration
	var value any
	s := &sink{before: func(ctx context.Context) {
		if dl, ok := ctx.Deadline(); ok {
			left = time.Until(dl)
		}
		value = ctx.Value(ctxKey{})
	}}
	o := New(p, "api", s, quiet)
	req, hangUp := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "req-1"))
	hangUp()
	o.Flush(req)
	if got := s.ids(); !equal(got, "id-00") {
		t.Fatalf("relayed after the client hung up: %v", got)
	}
	if left <= 0 || left > 2*time.Second {
		t.Fatalf("the send runs within the 2 s budget: %s left", left)
	}
	if value != "req-1" {
		t.Fatalf("the request's values reach the transport (log fields): %v", value)
	}
	if n := pending(t, p); n != 0 {
		t.Fatalf("%d rows not marked", n)
	}
}

// A transport that hangs costs a flush its budget, no more, and is no attempt.
func TestFlushStopsAtItsBudget(t *testing.T) {
	p := pool(t)
	if err := Insert(context.Background(), p, "api", msg(0)); err != nil {
		t.Fatal(err)
	}
	s := &sink{before: func(ctx context.Context) { <-ctx.Done() }}
	o := New(p, "api", s, quiet)
	o.FlushBudget = 300 * time.Millisecond
	start := time.Now()
	o.Flush(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a flush took %s with a 300 ms budget", d)
	}
	if len(s.ids()) != 0 || pending(t, p) != 1 {
		t.Fatalf("nothing sent, the row stays: %v", s.ids())
	}
	if n := attempts(t, p, "id-00"); n != 0 {
		t.Fatalf("a budget that ran out is not the row's failure: %d attempts", n)
	}
}

// A flush that loses the race to another instance waits for its drain, then takes its
// own row: it does not leave it for the tick.
func TestALosingFlushWaitsAndTakesItsRow(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	holder := holdRelayLock(t, p, "ingest")
	if err := Insert(ctx, p, "ingest", msg(0)); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	o.FlushBudget = 10 * time.Second
	done := make(chan struct{})
	go func() { defer close(done); o.Flush(ctx) }()
	waitForRelayWaiter(t, p, done)
	if got := s.ids(); len(got) != 0 {
		t.Fatalf("sent while another drain held the lock: %v", got)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the flush did not finish once the lock was free")
	}
	if got := s.ids(); !equal(got, "id-00") {
		t.Fatalf("the waiting flush takes its row: %v", got)
	}
}

// Behind a lock that stays held, a flush waits no longer than its budget (lock_timeout)
// and that is no attempt; the lock is the publisher's, so another publisher's relay
// does not wait.
func TestAFlushWaitsNoLongerThanItsBudget(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	holder := holdRelayLock(t, p, "ingest")
	if err := Insert(ctx, p, "ingest", msg(0)); err != nil {
		t.Fatal(err)
	}
	if err := Insert(ctx, p, "api", msg(1)); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	o.FlushBudget = 300 * time.Millisecond
	start := time.Now()
	o.Flush(ctx)
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("a flush behind a held lock waits for its budget, no longer: %s", d)
	}
	if len(s.ids()) != 0 {
		t.Fatalf("sent while another drain held the lock: %v", s.ids())
	}
	if n := attempts(t, p, "id-00"); n != 0 {
		t.Fatalf("a wait that ran out is no attempt: %d", n)
	}
	New(p, "api", s, quiet).Flush(ctx)
	if got := s.ids(); !equal(got, "id-01") {
		t.Fatalf("api's relay does not wait for ingest's lock: %v", got)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	o.Flush(ctx)
	if got := s.ids(); !equal(got, "id-01", "id-00") {
		t.Fatalf("the next flush takes the row once the lock is free: %v", got)
	}
}

// Several instances flush one publisher's rows at once, a backlog and rows written
// meanwhile: each user's rows reach the transport in id order, each once. With FOR
// UPDATE SKIP LOCKED alone, a second drain sends a later row while the first still
// holds an earlier one, which some round catches.
func TestConcurrentFlushesKeepEachUsersOrder(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	const instances, flushers, backlog, live = 4, 8, 30, 15
	for round := range 10 {
		s := &sink{before: func(context.Context) { time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond) }}
		obs := make([]*Outbox, instances)
		for i := range obs {
			obs[i] = New(p, "ingest", s, quiet)
			obs[i].Batch, obs[i].FlushBudget = 3, 10*time.Second
		}
		users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
		next := make([]int, len(users)) // each user's next place in its order
		row := func(i int) events.Message {
			u := i % len(users)
			m := msgFor(users[u], fmt.Sprintf("r%d-u%d-%03d", round, u, next[u]))
			next[u]++
			return m
		}
		for i := range backlog { // rows an instance that died left
			if err := Insert(ctx, p, "ingest", row(i)); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		for g := range flushers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 3 {
					obs[g%instances].Flush(ctx)
				}
			}()
		}
		wg.Add(1)
		go func() { // requests writing and flushing meanwhile, one after another
			defer wg.Done()
			for i := backlog; i < backlog+live; i++ {
				if err := obs[i%instances].Send(ctx, row(i)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		wg.Wait()
		if t.Failed() {
			t.FailNow()
		}
		if err := obs[0].Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		got := make([][]int, len(users))
		for _, id := range s.ids() {
			var r, u, n int
			if _, err := fmt.Sscanf(id, "r%d-u%d-%d", &r, &u, &n); err != nil || r != round || u < 0 || u >= len(users) {
				t.Fatalf("round %d: unexpected message %q", round, id)
			}
			got[u] = append(got[u], n)
		}
		for u := range users {
			if len(got[u]) != next[u] {
				t.Fatalf("round %d: user %d: %d sends of %d rows, each once: %v", round, u, len(got[u]), next[u], got[u])
			}
			for want, n := range got[u] {
				if n != want {
					t.Fatalf("round %d: user %d's rows out of order: %v", round, u, got[u])
				}
			}
		}
	}
}

// A row not yet due is neither drained nor counted; once due it is, and its wait counts
// from when it came due (or was written, if that is later), not from when it was written.
func TestNotBeforeIsNotDueAndNotCounted(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := &sink{}
	o := New(p, "ingest", s, quiet)
	if err := InsertAt(ctx, p, "ingest", msg(0), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	o.Flush(ctx)
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); len(got) != 0 {
		t.Fatalf("a row is not drained before its available_at: %v", got)
	}
	if n := pending(t, p); n != 1 {
		t.Fatalf("the deferred row stays: %d", n)
	}
	if v := testutil.ToFloat64(metrics.OutboxPending.WithLabelValues("ingest")); v != 0 {
		t.Fatalf("a deferred row is no pending row: %v", v)
	}
	if v := testutil.ToFloat64(metrics.OutboxOldest.WithLabelValues("ingest")); v != 0 {
		t.Fatalf("a deferred row has no age yet: %v", v)
	}

	// It comes due: written two hours ago, due ten seconds ago. A row InsertAt back-dates
	// by an hour counts from when it was written, just now.
	if _, err := p.Exec(ctx, `UPDATE outbox SET created_at = now() - interval '2 hours', available_at = now() - interval '10 seconds'
		WHERE message_id = 'id-00'`); err != nil {
		t.Fatal(err)
	}
	if err := InsertAt(ctx, p, "ingest", msg(1), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	g, err := o.counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.pending != 2 || g.stuck != 0 || g.oldest < 9 || g.oldest > 120 {
		t.Fatalf("both due, the oldest waiting about 10 s: %+v", g)
	}
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); !equal(got, "id-00", "id-01") {
		t.Fatalf("due rows go, oldest first: %v", got)
	}
	if v := testutil.ToFloat64(metrics.OutboxPending.WithLabelValues("ingest")); v != 0 {
		t.Fatalf("the sweep sets the gauge after draining: %v", v)
	}
}

// SendAt only writes, even a row that is due: the tick's sweep publishes it.
func TestSendAtDoesNotFlush(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := &sink{}
	o := New(p, "notify", s, quiet)
	at := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	later, due := refresh("2026-11-14"), refresh("2026-11-15")
	if err := o.SendAt(ctx, later, at); err != nil {
		t.Fatal(err)
	}
	if err := o.SendAt(ctx, due, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); len(got) != 0 {
		t.Fatalf("SendAt never flushes: %v", got)
	}
	var stored time.Time
	if err := p.QueryRow(ctx, `SELECT available_at FROM outbox WHERE message_id = $1`, later.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(at) {
		t.Fatalf("available_at = %s, want %s", stored, at)
	}
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); !equal(got, due.ID) {
		t.Fatalf("the sweep publishes the due deferral only: %v", got)
	}
}

// A sweep goes on batch after batch until nothing is due.
func TestSweepDrainsBatchAfterBatch(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	for i := range 7 {
		if err := Insert(ctx, p, "scoring", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	s := &sink{}
	o := New(p, "scoring", s, quiet)
	o.Batch = 2
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); !equal(got, "id-00", "id-01", "id-02", "id-03", "id-04", "id-05", "id-06") {
		t.Fatalf("four batches, in order: %v", got)
	}
	if n := pending(t, p); n != 0 {
		t.Fatalf("%d rows left", n)
	}
}

// A sweep stops when its context ends, marks what it sent and counts no attempt for
// the rest.
func TestSweepStopsWhenItsContextEnds(t *testing.T) {
	p := pool(t)
	for i := range 10 {
		if err := Insert(context.Background(), p, "scoring", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sink{}
	s.fail = func(events.Message) error { // runs under s.mu
		if len(s.sent) == 2 {
			cancel() // the third send is the last
		}
		return nil
	}
	o := New(p, "scoring", s, quiet)
	o.Batch = 2
	if err := o.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("the sweep reports why it stopped: %v", err)
	}
	if got := s.ids(); !equal(got, "id-00", "id-01", "id-02") {
		t.Fatalf("sent until the end: %v", got)
	}
	if n := pending(t, p); n != 7 {
		t.Fatalf("what was sent is marked, mid-batch too: %d pending, want 7", n)
	}
	if n := attempts(t, p, "id-03"); n != 0 {
		t.Fatalf("the row after the end was not tried: %d attempts", n)
	}
}

// A sweep that runs out of budget still sets the gauges: the drain leaves it the end.
func TestSweepKeepsTimeForTheGauges(t *testing.T) {
	p := pool(t)
	const rows = 200
	for i := range rows {
		if err := Insert(context.Background(), p, "scoring", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	s := &sink{before: func(context.Context) { time.Sleep(10 * time.Millisecond) }} // a slow transport
	o := New(p, "scoring", s, quiet)
	o.Batch = 10
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := o.Sweep(ctx); err == nil {
		t.Fatal("a sweep that ran out of budget says so")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("a 1 s sweep took %s", d)
	}
	sent := len(s.ids())
	if sent == 0 || sent == rows {
		t.Fatalf("%d of %d sent: the budget should end the sweep midway", sent, rows)
	}
	if v := testutil.ToFloat64(metrics.OutboxPending.WithLabelValues("scoring")); v != float64(rows-sent) {
		t.Fatalf("the gauges are set after a sweep that ran out of time: pending %v, want %d", v, rows-sent)
	}
}

// A refused send keeps the error and stops the drain, so the rows after it wait. A
// sweep's counts an attempt; a flush's does not (one runs on every request: an outage of
// a minute would set the head row aside), it only records the error. Once the transport
// takes it, all go in order.
func TestFailedSendCountsAndStops(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	var mu sync.Mutex
	refuse, why := "id-01", "topic refused"
	s := &sink{fail: func(m events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		if m.ID == refuse {
			return errors.New(why)
		}
		return nil
	}}
	o := New(p, "scoring", s, quiet)
	for i := range 3 {
		if err := Insert(ctx, p, "scoring", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Sweep(ctx); err == nil || !strings.Contains(err.Error(), "topic refused") {
		t.Fatalf("the sweep returns the send's error for the tick to log: %v", err)
	}
	if got := s.ids(); !equal(got, "id-00") {
		t.Fatalf("stopped at the refused row: %v", got)
	}
	var lastErr string
	if err := p.QueryRow(ctx, `SELECT last_error FROM outbox WHERE message_id = 'id-01'`).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if n := attempts(t, p, "id-01"); n != 1 || lastErr != "topic refused" {
		t.Fatalf("one attempt with its error: %d %q", n, lastErr)
	}
	if n := attempts(t, p, "id-02"); n != 0 {
		t.Fatalf("the row after it was not tried: %d", n)
	}
	mu.Lock()
	why = "pubsub: publish to ryft-ingested: HTTP 503: UNAVAILABLE"
	mu.Unlock()
	for range MaxAttempts + 5 {
		o.Flush(ctx)
	}
	if err := p.QueryRow(ctx, `SELECT last_error FROM outbox WHERE message_id = 'id-01'`).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if n := attempts(t, p, "id-01"); n != 1 || lastErr != why {
		t.Fatalf("after %d flushes: %d attempts, last_error %q; want 1 and the flush's error", MaxAttempts+5, n, lastErr)
	}
	if got := s.ids(); !equal(got, "id-00") {
		t.Fatalf("a flush stops at the failure too: %v", got)
	}
	if err := o.Sweep(ctx); err == nil {
		t.Fatal("the sweep fails")
	}
	if n := attempts(t, p, "id-01"); n != 2 {
		t.Fatalf("the next sweep counts: %d", n)
	}
	mu.Lock()
	refuse = ""
	mu.Unlock()
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); !equal(got, "id-00", "id-01", "id-02") {
		t.Fatalf("order kept across the failure: %v", got)
	}
	var cleared *string
	if err := p.QueryRow(ctx, `SELECT last_error FROM outbox WHERE message_id = 'id-01'`).Scan(&cleared); err != nil || cleared != nil {
		t.Fatalf("the error goes with the publish: %v %v", cleared, err)
	}
}

// The 50th failed send sets a row aside: until then it holds up the rows after it,
// then they go. Old published rows are deleted.
func TestSetAsideAfterMaxAttempts(t *testing.T) {
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
	if _, err := p.Exec(ctx, `UPDATE outbox SET attempts = $1 WHERE routing_key = 'nobody.listens'`, MaxAttempts-1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO outbox (publisher, routing_key, payload, published_at) VALUES ('ingest', 'ingested', '{}', now() - interval '8 days')`); err != nil {
		t.Fatal(err)
	}
	s := &sink{fail: func(m events.Message) error {
		if m.Key == "nobody.listens" {
			return errors.New("no topic")
		}
		return nil
	}}
	o := New(p, "ingest", s, quiet)
	if err := o.Sweep(ctx); err == nil {
		t.Fatal("the 50th attempt fails")
	}
	if got := s.ids(); len(got) != 0 {
		t.Fatalf("until it is set aside the row holds up the rest: %v", got)
	}
	if n := attempts(t, p, "id-00"); n != MaxAttempts {
		t.Fatalf("attempts = %d, want %d", n, MaxAttempts)
	}
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.ids(); !equal(got, "id-01") {
		t.Fatalf("set aside, the row after it goes: %v", got)
	}
	if v := testutil.ToFloat64(metrics.OutboxStuck.WithLabelValues("ingest")); v != 1 {
		t.Fatalf("ryft_outbox_stuck = %v, want 1", v)
	}
	if v := testutil.ToFloat64(metrics.OutboxPending.WithLabelValues("ingest")); v != 0 {
		t.Fatalf("a row set aside is not pending: %v", v)
	}
	var old int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at < now() - interval '7 days'`).Scan(&old)
	if old != 0 {
		t.Fatal("the sweep deletes published rows older than a week")
	}
}

// A send the transport rejects for good (events.Rejected: no topic for the key, a body
// that cannot be a message, Pub/Sub's 400, 403, 404) is set aside at once, by a flush as
// well as a sweep, with why in last_error; the rows after it go in the same drain.
func TestRejectedSendIsSetAsideAtOnce(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	for i := range 4 {
		if err := Insert(ctx, p, "api", msg(i)); err != nil {
			t.Fatal(err)
		}
	}
	s := &sink{fail: func(m events.Message) error {
		switch m.ID {
		case "id-00":
			return events.Rejected(errors.New("pubsub: no topic for this key"))
		case "id-02":
			return events.Rejected(errors.New("pubsub: publish to ryft-ingested: HTTP 400: INVALID_ARGUMENT"))
		}
		return nil
	}}
	o := New(p, "api", s, quiet)
	o.Flush(ctx)
	if got := s.ids(); !equal(got, "id-01", "id-03") {
		t.Fatalf("the rows after a rejected one go in the same flush: %v", got)
	}
	for id, why := range map[string]string{"id-00": "no topic", "id-02": "HTTP 400"} {
		var lastErr string
		if err := p.QueryRow(ctx, `SELECT last_error FROM outbox WHERE message_id = $1`, id).Scan(&lastErr); err != nil {
			t.Fatal(err)
		}
		if n := attempts(t, p, id); n != MaxAttempts || !strings.HasPrefix(lastErr, "rejected, set aside: ") || !strings.Contains(lastErr, why) {
			t.Fatalf("%s: %d attempts, last_error %q; want %d and why", id, n, lastErr, MaxAttempts)
		}
	}
	if err := o.Sweep(ctx); err != nil {
		t.Fatalf("nothing is due any more: %v", err)
	}
	if v := testutil.ToFloat64(metrics.OutboxStuck.WithLabelValues("api")); v != 2 {
		t.Fatalf("ryft_outbox_stuck = %v, want 2", v)
	}
	if got := s.ids(); !equal(got, "id-01", "id-03") {
		t.Fatalf("a row set aside is not sent again: %v", got)
	}

	// A sweep sets a rejected row aside the same way, and returns the rejection for the tick's log.
	if err := Insert(ctx, p, "api", msgFor(uuid.New(), "id-10")); err != nil {
		t.Fatal(err)
	}
	r := New(p, "api", &sink{fail: func(events.Message) error { return events.Rejected(errors.New("HTTP 403: PERMISSION_DENIED")) }}, quiet)
	if err := r.Sweep(ctx); err == nil || !events.IsRejected(err) || !strings.Contains(err.Error(), "set aside") {
		t.Fatalf("the sweep's error: %v", err)
	}
	if n := attempts(t, p, "id-10"); n != MaxAttempts {
		t.Fatalf("a sweep's rejection: %d attempts", n)
	}
}

// notify relays its deferrals under its own role (0034): the relay lock, lock_timeout,
// row locks, marks, attempts and clean need nothing the role lacks.
func TestNotifyRelaysAsItsRole(t *testing.T) {
	p := pool(t)
	np := asRole(t, p, "ryft_notify")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // a deadline: the drain sets lock_timeout
	defer cancel()
	s := &sink{}
	o := New(np, "notify", s, quiet)
	due, later := refresh("2026-11-14"), refresh("2026-11-15")
	if err := o.SendAt(ctx, due, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := o.SendAt(ctx, later, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := o.Sweep(ctx); err != nil {
		t.Fatalf("a sweep as ryft_notify: %v", err)
	}
	if got := s.ids(); !equal(got, due.ID) {
		t.Fatalf("the due deferral goes, the later one waits: %v", got)
	}
	refused := refresh("2026-11-16")
	bad := New(np, "notify", &sink{fail: func(events.Message) error { return errors.New("topic refused") }}, quiet)
	if err := bad.SendAt(ctx, refused, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := bad.Sweep(ctx); err == nil || !strings.Contains(err.Error(), "topic refused") {
		t.Fatalf("want the send's error, not a missing grant: %v", err)
	}
	if n := attempts(t, p, refused.ID); n != 1 {
		t.Fatalf("ryft_notify records the attempt: %d", n)
	}
	rejected := refresh("2026-11-17")
	gone := New(np, "notify", &sink{fail: func(events.Message) error { return events.Rejected(errors.New("HTTP 404: NOT_FOUND")) }}, quiet)
	if err := gone.SendAt(ctx, rejected, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := gone.Sweep(ctx); err == nil || !events.IsRejected(err) {
		t.Fatalf("want the rejection, not a missing grant: %v", err)
	}
	if n := attempts(t, p, rejected.ID); n != MaxAttempts {
		t.Fatalf("ryft_notify sets a rejected row aside: %d", n)
	}
}

// Run (tests, local tools): a transport that refuses leaves the rows and counts the
// attempt; once it accepts they go, still in order.
func TestRunRetriesInOrder(t *testing.T) {
	p := pool(t)
	var mu sync.Mutex
	down := true
	s := &sink{fail: func(events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		if down {
			return errors.New("transport down")
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
		t.Fatalf("nothing marked while the transport is down: sent %v", s.ids())
	}
	mu.Lock()
	down = false
	mu.Unlock()
	waitFor(t, "all sent", func() bool { return len(s.ids()) == 3 })
	if got := s.ids(); !equal(got, "id-00", "id-01", "id-02") {
		t.Fatalf("order kept across the outage: %v", got)
	}
}

// Two relays drain one publisher's rows: each row is sent once.
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
// rows are not this relay's; only the four publishers may write.
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
	if err := New(p, "ingest", s, quiet).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.ids()) != 0 {
		t.Fatalf("ingest's relay sends neither the rolled-back row nor api's: %v", s.ids())
	}
	if err := Insert(ctx, p, "notify", msg(3)); err != nil {
		t.Fatalf("notify publishes too (0034): %v", err)
	}
	if err := Insert(ctx, p, "nobody", msg(4)); err == nil {
		t.Fatal("only the four publishers may write")
	}
}
