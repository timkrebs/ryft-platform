// Package outbox is the transactional outbox between a service's database writes and
// its broker (D101): an event is a row in table outbox, written in the same
// transaction as what it announces (Insert) or right after it (Send), and the
// service's relay (Run) sends the rows with the broker's confirm and marks them
// published. Several replicas drain one publisher's rows without sending one twice
// (FOR UPDATE SKIP LOCKED); a crash between a send and its mark sends it again, which
// the idempotent handlers absorb (D100).
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
)

// MaxAttempts: a row that failed this often is set aside (stuck, alerted) instead of
// holding up the rows after it, e.g. an event no queue is bound for.
const MaxAttempts = 50

// Keep is how long published rows stay, for looking back.
const Keep = 7 * 24 * time.Hour

// Querier runs a statement: a pool, or the caller's transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Insert writes m as publisher's row in q, the transaction of the write it announces.
// Wake the relay (Kick) after the commit.
func Insert(ctx context.Context, q Querier, publisher string, m events.Message) error {
	var who struct {
		UserID *uuid.UUID `json:"user_id"`
	}
	_ = json.Unmarshal(m.Body, &who)
	_, err := q.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, message_id, reason, payload)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, publisher, who.UserID, m.Key, m.ID, m.Reason, string(m.Body))
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
	kick      chan struct{}

	Batch   int           // rows per pass (default 50)
	Poll    time.Duration // how often to look without a kick (default 2 s): rows of a crashed replica, retries
	Observe time.Duration // how often to update the gauges (default 15 s)
}

// New is publisher's outbox (ingest, api or scoring), relaying into to (the transport).
func New(pool *pgxpool.Pool, publisher string, to events.Sender, log *slog.Logger) *Outbox {
	return &Outbox{pool: pool, publisher: publisher, to: to, log: log, kick: make(chan struct{}, 1),
		Batch: 50, Poll: 2 * time.Second, Observe: 15 * time.Second}
}

// Send writes m as a row of its own and wakes the relay (events.Sender): for a publish
// without a transaction of the caller's.
func (o *Outbox) Send(ctx context.Context, m events.Message) error {
	if err := Insert(ctx, o.pool, o.publisher, m); err != nil {
		metrics.EventsPublished.WithLabelValues(m.Reason, "error").Inc()
		return err
	}
	o.Kick()
	return nil
}

// Kick wakes the relay (after a commit that inserted rows).
func (o *Outbox) Kick() {
	select {
	case o.kick <- struct{}{}:
	default:
	}
}

// Run relays until ctx ends: on a kick, every Poll, and again at once while a pass
// found a full batch; after a failure it backs off (1 s doubling to 30 s).
func (o *Outbox) Run(ctx context.Context) {
	var backoff time.Duration
	lastObserve, lastClean := time.Time{}, time.Now()
	for {
		n, err := o.drain(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			backoff = min(max(2*backoff, time.Second), 30*time.Second)
			o.log.Warn("outbox relay: send failed, retrying", "publisher", o.publisher, "err", err, "retry_in", backoff)
		} else {
			backoff = 0
		}
		if time.Since(lastObserve) >= o.Observe {
			o.observe(ctx)
			lastObserve = time.Now()
		}
		if time.Since(lastClean) >= time.Hour {
			o.clean(ctx)
			lastClean = time.Now()
		}
		if err == nil && n == o.Batch {
			continue // more are waiting
		}
		wait, kick := o.Poll, o.kick
		if backoff > 0 {
			wait, kick = backoff, nil // a kick does not cut a backoff short
		}
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-time.After(wait):
		}
	}
}

type row struct {
	id                         int64
	key, messageID, reason, js string
}

// drain sends up to one batch, oldest first, inside one transaction holding the rows'
// locks; it stops at the first failure so the order holds. It returns how many rows it
// took.
func (o *Outbox) drain(ctx context.Context) (int, error) {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `SELECT id, routing_key, message_id, reason, payload::text FROM outbox
		WHERE publisher = $1 AND published_at IS NULL AND attempts < $2
		ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED`, o.publisher, MaxAttempts, o.Batch)
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
	var sendErr error
	for _, r := range batch {
		m := events.Message{Key: r.key, ID: r.messageID, Body: []byte(r.js), Reason: r.reason}
		if err := o.to.Send(ctx, m); err != nil {
			sendErr = err
			metrics.OutboxRelayed.WithLabelValues(o.publisher, "error").Inc()
			reason := err.Error()
			if len(reason) > 512 {
				reason = reason[:512]
			}
			if _, uerr := tx.Exec(ctx, `UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, r.id, reason); uerr != nil {
				return len(batch), uerr
			}
			break
		}
		metrics.OutboxRelayed.WithLabelValues(o.publisher, "ok").Inc()
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = $1`, r.id); err != nil {
			return len(batch), err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return len(batch), err
	}
	return len(batch), sendErr
}

// observe updates the gauges: rows waiting, the oldest one's age, rows set aside.
func (o *Outbox) observe(ctx context.Context) {
	var pending, stuck int
	var oldest float64
	err := o.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE attempts < $2),
			count(*) FILTER (WHERE attempts >= $2),
			COALESCE(extract(epoch FROM now() - min(created_at) FILTER (WHERE attempts < $2)), 0)
		FROM outbox WHERE publisher = $1 AND published_at IS NULL`, o.publisher, MaxAttempts).Scan(&pending, &stuck, &oldest)
	if err != nil {
		o.log.Warn("outbox: observe", "err", err)
		return
	}
	metrics.OutboxPending.WithLabelValues(o.publisher).Set(float64(pending))
	metrics.OutboxStuck.WithLabelValues(o.publisher).Set(float64(stuck))
	metrics.OutboxOldest.WithLabelValues(o.publisher).Set(oldest)
}

// clean deletes published rows older than Keep.
func (o *Outbox) clean(ctx context.Context) {
	if _, err := o.pool.Exec(ctx, `DELETE FROM outbox WHERE publisher = $1 AND published_at < now() - make_interval(secs => $2)`,
		o.publisher, Keep.Seconds()); err != nil {
		o.log.Warn("outbox: clean", "err", err)
	}
}
