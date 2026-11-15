package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// MaxAttempts is how many failed sends a row gets before the relay sets it aside
// (stuck, alerted as OutboxStuck) instead of letting it hold up the rows after it. Only
// a Sweep's failed send counts (the tick's, one in 15 minutes, so about 12 hours of an
// outage): a Flush runs on every request, and during an outage fifty requests would
// otherwise set the head row aside in seconds. A send the transport rejects for good
// (events.Rejected: a key no topic takes, a body that cannot be a message, Pub/Sub's
// 400, 403 or 404) sets the row aside at once, from either. A send cut off because the
// drain's budget ended counts nothing. A stuck row needs a person.
const MaxAttempts = 50

// Keep is how long published rows stay, for looking back.
const Keep = 7 * 24 * time.Hour

// The relay lock (D101): transaction-level, one per publisher. Class 7419006 belongs
// with the others listed in internal/store/locks.go; a class is never reused.
const (
	relayLockClass = 7419006
	relayLock      = `SELECT pg_advisory_xact_lock(7419006, hashtext($1::text))`
)

// markGrace is how long past its budget a drain may take to mark the rows it already
// sent and commit, so a budget that ends mid-batch does not send them a second time.
const markGrace = time.Second

// sweepReserve is the most of a Sweep's budget kept back from the drain for observe
// and clean (a quarter of the budget when that is less).
const sweepReserve = 2 * time.Second

// Querier runs a statement: a pool, or the caller's transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Insert writes m as publisher's row in q, the transaction of the write it announces,
// due at once. Call (*Outbox).Flush after the commit; the tick's Sweep takes what it
// leaves.
func Insert(ctx context.Context, q Querier, publisher string, m events.Message) error {
	return insert(ctx, q, publisher, m, nil)
}

// InsertAt is Insert with a not-before time: no drain takes the row before at, and
// until then it counts in neither ryft_outbox_pending nor ryft_outbox_oldest_seconds.
// A zero or past at is due at once.
func InsertAt(ctx context.Context, q Querier, publisher string, m events.Message, at time.Time) error {
	return insert(ctx, q, publisher, m, &at)
}

// insert writes the row; a nil at is now(), the column's default.
func insert(ctx context.Context, q Querier, publisher string, m events.Message, at *time.Time) error {
	var who struct {
		UserID *uuid.UUID `json:"user_id"`
	}
	_ = json.Unmarshal(m.Body, &who)
	_, err := q.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, message_id, reason, payload, available_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, COALESCE($7::timestamptz, now()))`,
		publisher, who.UserID, m.Key, m.ID, m.Reason, string(m.Body), at)
	if err != nil {
		return fmt.Errorf("outbox insert: %w", err)
	}
	return nil
}

// Outbox is one publisher's outbox and relay.
type Outbox struct {
	pool      *pgxpool.Pool
	publisher string
	to        events.Sender
	log       *slog.Logger

	// turn lets one drain of this Outbox at a time hold a connection; the others wait
	// here rather than in Postgres for the relay lock. The lock orders the instances.
	turn chan struct{}

	Batch       int           // rows per drain transaction (default 50)
	Poll        time.Duration // Run's pause between sweeps (default 2 s)
	FlushBudget time.Duration // how long a Flush may take (default 2 s)
}

// New is publisher's outbox (ingest, api, scoring or notify), relaying into to (the
// transport). With a nil to (tests that never relay) every drain is a no-op and the
// rows stay.
func New(pool *pgxpool.Pool, publisher string, to events.Sender, log *slog.Logger) *Outbox {
	return &Outbox{pool: pool, publisher: publisher, to: to, log: log, turn: make(chan struct{}, 1),
		Batch: 50, Poll: 2 * time.Second, FlushBudget: 2 * time.Second}
}

// Send writes m as a row of its own, due at once, and flushes (events.Sender): for a
// publish without a transaction of the caller's. Its error is the INSERT's, so there
// is no row; a relay that fails only logs, as the row stays for the next Flush or
// Sweep.
func (o *Outbox) Send(ctx context.Context, m events.Message) error {
	if err := Insert(ctx, o.pool, o.publisher, m); err != nil {
		metrics.EventsPublished.WithLabelValues(m.Reason, "error").Inc()
		return err
	}
	o.Flush(ctx)
	return nil
}

// SendAt writes m as a row of its own that no drain takes before at, and does not
// flush: notify's deferral sink (quiet hours, D219). The tick's Sweep publishes it once
// due, so it goes up to a tick late. An error means no row: the caller nacks and the
// message comes again.
func (o *Outbox) SendAt(ctx context.Context, m events.Message, at time.Time) error {
	return InsertAt(ctx, o.pool, o.publisher, m, at)
}

// Flush relays the publisher's due rows now, inline, for a caller about to answer: on
// Cloud Run nothing runs after the response (D219). Call it after the commit that
// inserted rows. It takes at most FlushBudget (plus markGrace to record what it sent)
// on a context detached from ctx's cancel, so a client that hangs up after the commit
// does not skip the relay; the server's hard stop still ends it (detach). It logs a
// failure and returns nothing: the rows stay for the next Flush or the tick's Sweep.
func (o *Outbox) Flush(ctx context.Context) {
	ctx, cancel := detach(ctx, o.FlushBudget)
	defer cancel()
	if err := o.drainAll(ctx, flushing); err != nil {
		o.log.WarnContext(ctx, "outbox flush: events left for the next flush or tick", "publisher", o.publisher, "err", err)
	}
}

// detach is ctx without its cancel and deadline, its values (the request's log fields)
// kept, ending after budget or at the server's hard stop, whichever comes first
// (httpx.Detach): a flush or a mark still running when the shutdown's http step runs out
// gives its connection back before the pool closes (review-a1 finding 6). Off a request
// (a sweep from a test or a tool) there is no hard stop and only budget ends it.
func detach(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	return httpx.Detach(ctx, budget)
}

// Sweep relays every due row, batch after batch, until none is left, a send fails or
// ctx ends (the tick's budget), then sets the gauges and deletes published rows older
// than Keep. It takes what the inline flushes left: rows of an instance that died,
// failed publishes, deferrals that came due. Its failed send counts an attempt
// (MaxAttempts). The drain stops a little before ctx's deadline (see reserve), so the
// gauges are set even when it ran out of time, which is when they matter. It returns
// the first error (the drain's, observe's, clean's) for the tick to log, after trying
// all three.
func (o *Outbox) Sweep(ctx context.Context) error {
	dctx, cancel := reserve(ctx)
	err := o.drainAll(dctx, sweeping)
	cancel()
	if oerr := o.observe(ctx); err == nil {
		err = oerr
	}
	if cerr := o.clean(ctx); err == nil {
		err = cerr
	}
	return err
}

// reserve is ctx ending sweepReserve (or a quarter of what is left, when that is less)
// before ctx's deadline; without a deadline it is ctx.
func reserve(ctx context.Context) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, dl.Add(-min(sweepReserve, time.Until(dl)/4)))
}

// Run sweeps every Poll until ctx ends, backing off after a failure (1 s doubling to
// 30 s). It is for tests and local tools: the services relay with Flush and the tick's
// Sweep, as Cloud Run gives no CPU between requests.
func (o *Outbox) Run(ctx context.Context) {
	var backoff time.Duration
	for {
		err := o.Sweep(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := o.Poll
		if err != nil {
			backoff = min(max(2*backoff, time.Second), 30*time.Second)
			wait = backoff
			o.log.Warn("outbox relay: sweep failed, retrying", "publisher", o.publisher, "err", err, "retry_in", backoff.String())
		} else {
			backoff = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

type row struct {
	id                         int64
	key, messageID, reason, js string
}

// mode is who drains: a Flush or a Sweep. Only a Sweep's failed send counts an attempt.
type mode bool

const (
	flushing mode = false
	sweeping mode = true
)

// drainAll drains batch after batch until one comes back short (nothing more is due),
// a send fails or ctx ends. A rejected row does not stop it: it is set aside, so the
// rows after it go now. It returns the first error.
func (o *Outbox) drainAll(ctx context.Context, by mode) error {
	var first error
	for {
		took, err := o.drain(ctx, by)
		if first == nil {
			first = err
		}
		switch {
		case err != nil && !events.IsRejected(err):
			return first
		case err == nil && took < o.batch():
			return first
		}
	}
}

func (o *Outbox) batch() int { return max(o.Batch, 1) }

// drain sends up to one batch of due rows, oldest first, and stops at the first failed
// send. It returns how many rows it took. The failed row keeps the error in last_error;
// a rejected one (events.Rejected) is set aside at once (attempts = MaxAttempts) and the
// error returned is still a rejection, and any other counts an attempt when a Sweep
// drains (by), not when a Flush does.
//
// Order (D101): the batch is one transaction that first takes the publisher's relay
// lock, so a publisher's batches reach the transport one after another, in id order,
// across goroutines and instances, and one user's rows with them. The first relay had
// FOR UPDATE SKIP LOCKED alone, and with inline flushes that let a second drain send a
// later row while the first still held an earlier one. The lock is a transaction-level
// advisory lock and lock_timeout is set with set_config(…, true), so nothing outlives
// the transaction: that is what Supabase's transaction pooler allows (no session
// state, no SET outside a transaction, no named statements assumed). The row locks
// stay, so a relay without the lock (an older binary in a rollout) still never sends a
// row twice at the same time.
//
// A drain that finds the lock taken waits for it, at most until just before its ctx's
// deadline (lock_timeout, so the server ends the wait cleanly, 55P03), then takes
// whatever is due by then, its own rows included: a Flush that loses the race does not
// leave its row for the tick. Only when its budget ends first (the holder is slow,
// which means the transport is) does the row wait for the next Flush or Sweep.
func (o *Outbox) drain(ctx context.Context, by mode) (int, error) {
	if o.to == nil {
		return 0, nil
	}
	select {
	case o.turn <- struct{}{}:
		defer func() { <-o.turn }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		rb, cancel := detach(ctx, markGrace) // a no-op after Commit
		_ = tx.Rollback(rb)
		cancel()
	}()
	if dl, ok := ctx.Deadline(); ok {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, lockTimeout(time.Until(dl))); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, relayLock, o.publisher); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
			return 0, fmt.Errorf("outbox: another drain held the relay lock until the budget ended: %w", err)
		}
		return 0, fmt.Errorf("outbox relay lock: %w", err)
	}
	// statement_timestamp, not now(): now() is the transaction's start, before the wait.
	rows, err := tx.Query(ctx, `SELECT id, routing_key, message_id, reason, payload::text FROM outbox
		WHERE publisher = $1 AND published_at IS NULL AND attempts < $2 AND available_at <= statement_timestamp()
		ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED`, o.publisher, MaxAttempts, o.batch())
	if err != nil {
		return 0, err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.key, &x.messageID, &x.reason, &x.js)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	sent := make([]int64, 0, len(batch))
	var failed *row
	var sendErr error
	for i := range batch {
		r := &batch[i]
		if err := ctx.Err(); err != nil {
			sendErr = err
			break
		}
		m := events.Message{Key: r.key, ID: r.messageID, Body: []byte(r.js), Reason: r.reason}
		if err := o.to.Send(ctx, m); err != nil {
			sendErr = err
			if ctx.Err() == nil { // the transport refused it; a budget that ended is no attempt
				failed = r
				metrics.OutboxRelayed.WithLabelValues(o.publisher, "error").Inc()
			}
			break
		}
		metrics.OutboxRelayed.WithLabelValues(o.publisher, "ok").Inc()
		sent = append(sent, r.id)
	}
	fin, cancel := detach(ctx, markGrace)
	defer cancel()
	if len(sent) > 0 {
		if _, err := tx.Exec(fin, `UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = ANY($1)`, sent); err != nil {
			return len(batch), err
		}
	}
	if failed != nil {
		// A rejection: set aside now. A sweep's failure: one attempt more. A flush's: the
		// error only.
		q, args := `UPDATE outbox SET last_error = $2 WHERE id = $1`, []any{failed.id, truncate(sendErr.Error(), 512)}
		switch {
		case events.IsRejected(sendErr):
			q = `UPDATE outbox SET attempts = GREATEST(attempts, $3), last_error = $2 WHERE id = $1`
			args = []any{failed.id, truncate("rejected, set aside: "+sendErr.Error(), 512), MaxAttempts}
			sendErr = fmt.Errorf("outbox: row %d set aside: %w", failed.id, sendErr)
		case by == sweeping:
			q = `UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`
		}
		if _, err := tx.Exec(fin, q, args...); err != nil {
			return len(batch), err
		}
	}
	if err := tx.Commit(fin); err != nil {
		return len(batch), err
	}
	return len(batch), sendErr
}

// lockTimeout is the lock_timeout for a drain with left until its deadline: nine
// tenths of it, so the server gives up on the relay lock before the client cuts the
// connection, and at least 1 ms (0 would mean no timeout).
func lockTimeout(left time.Duration) string {
	return fmt.Sprintf("%dms", max(left.Milliseconds()*9/10, 1))
}

// truncate cuts s to at most n bytes without splitting a character (Postgres refuses
// invalid UTF-8 in a text column).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// gauges are the publisher's outbox numbers.
type gauges struct {
	pending, stuck int     // due rows waiting; rows set aside
	oldest         float64 // seconds the oldest due row has waited
}

// counts are the publisher's unpublished rows. Only due rows (available_at passed)
// count as pending, and the oldest one's wait is measured from when it could first
// have been sent, the later of created_at and available_at: a deferral that just came
// due after a night of quiet hours is no backlog of hours, and OutboxBacklog fires only
// for rows that missed their flush and two ticks. A row set aside counts as stuck, due
// or not.
func (o *Outbox) counts(ctx context.Context) (gauges, error) {
	var g gauges
	err := o.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE attempts < $2 AND available_at <= now()),
			count(*) FILTER (WHERE attempts >= $2),
			COALESCE(extract(epoch FROM now() - min(GREATEST(created_at, available_at))
				FILTER (WHERE attempts < $2 AND available_at <= now())), 0)
		FROM outbox WHERE publisher = $1 AND published_at IS NULL`, o.publisher, MaxAttempts).Scan(&g.pending, &g.stuck, &g.oldest)
	return g, err
}

// observe sets ryft_outbox_pending, ryft_outbox_oldest_seconds and ryft_outbox_stuck.
func (o *Outbox) observe(ctx context.Context) error {
	g, err := o.counts(ctx)
	if err != nil {
		return fmt.Errorf("outbox observe: %w", err)
	}
	metrics.OutboxPending.WithLabelValues(o.publisher).Set(float64(g.pending))
	metrics.OutboxStuck.WithLabelValues(o.publisher).Set(float64(g.stuck))
	metrics.OutboxOldest.WithLabelValues(o.publisher).Set(g.oldest)
	return nil
}

// clean deletes published rows older than Keep.
func (o *Outbox) clean(ctx context.Context) error {
	if _, err := o.pool.Exec(ctx, `DELETE FROM outbox WHERE publisher = $1 AND published_at < now() - make_interval(secs => $2)`,
		o.publisher, Keep.Seconds()); err != nil {
		return fmt.Errorf("outbox clean: %w", err)
	}
	return nil
}
