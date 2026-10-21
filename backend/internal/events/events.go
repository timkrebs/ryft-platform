// Package events is what the services exchange: five trigger events, how they are
// encoded, and what a handler's error means. The transports live beside it:
// natsbus (NATS JetStream, until the cutover) and amqpbus (RabbitMQ, D97); an
// outbox sits between a service's database writes and its transport.
//
// Events are triggers, not records: Postgres is the source of truth, POST
// /v1/recompute replays, and every handler is idempotent, so a duplicate delivery
// changes nothing. ingested (a sync or recompute touched days) and
// session.completed (a strength session was finished) go to scoring;
// scored, session.scored and refresh (scoring has scored a morning, a session, a
// sync's days) go to notify. Each event has exactly one consumer (D71).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Event keys: the routing key on RabbitMQ's ryft.events, the NATS subject without
// its "ryft." prefix.
const (
	KeyIngested         = "ingested"
	KeySessionCompleted = "session.completed"
	KeyScored           = "scored"
	KeySessionScored    = "session.scored"
	KeyRefresh          = "refresh"
)

// The two consumers: a NATS durable consumer each, a RabbitMQ queue each.
const (
	ConsumerScoring = "scoring"
	ConsumerNotify  = "notify"
)

// The keys each consumer takes. A new event key must be added to one of them (and
// to the transports' topology).
var (
	ScoringKeys = []string{KeyIngested, KeySessionCompleted}
	NotifyKeys  = []string{KeyScored, KeySessionScored, KeyRefresh}
)

// Headers on a dead-lettered message, the same on both transports.
const (
	HeaderSubject    = "Ryft-Subject"    // the key it was published with
	HeaderConsumer   = "Ryft-Consumer"   // who gave up on it
	HeaderDeliveries = "Ryft-Deliveries" // how often it was handled
	HeaderError      = "Ryft-Error"      // the last error, at most 512 bytes
)

// ReasonSessionCompleted is the IngestedEvent.Reason a session event becomes.
const ReasonSessionCompleted = "session_completed"

// Message is one event on the wire.
type Message struct {
	Key    string // event key (above)
	ID     string // stable for the same trigger: the dedup id, a consumer's idempotency key
	Body   []byte // JSON
	Reason string // the label of ryft_events_published_total
}

// Sender moves a message: a transport, or the outbox (which a relay drains into one).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Bus is what the services use: publish the five events, consume their queue, and
// whether the transport is connected. natsbus and amqpbus implement it; Combine
// joins one for publishing with several for consuming (the cutover).
type Bus interface {
	// Sender is the raw publish the typed ones below are made of; the outbox's relay
	// sends its rows with it.
	Sender
	PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error
	PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error
	PublishScored(ctx context.Context, ev model.ScoredEvent) error
	PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error
	PublishRefresh(ctx context.Context, ev model.RefreshEvent) error
	// ConsumeIngested blocks, handing scoring every ingested and session.completed
	// (a session event as an IngestedEvent for its day).
	ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error
	// ConsumeNotify blocks, handing notify every scored, session.scored and refresh.
	ConsumeNotify(ctx context.Context, log *slog.Logger, fn func(context.Context, NotifyMessage) error) error
	Ready(ctx context.Context) error
	Close()
}

// Publisher implements Bus's publishing half on a Sender.
type Publisher struct{ Sender Sender }

// PublishIngested queues days for scoring. A non-empty dedupID marks repeats of the
// same trigger (NATS drops them within two minutes; RabbitMQ delivers them, and
// rescoring a day twice changes nothing).
func (p Publisher) PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error {
	return p.send(ctx, func() (Message, error) { return Ingested(ev, dedupID) })
}

// PublishSessionCompleted queues a finished session's day for scoring.
func (p Publisher) PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error {
	return p.send(ctx, func() (Message, error) { return SessionCompleted(ev, dedupID) })
}

// PublishScored tells notify a day got its morning score.
func (p Publisher) PublishScored(ctx context.Context, ev model.ScoredEvent) error {
	return p.send(ctx, func() (Message, error) { return Scored(ev) })
}

// PublishSessionScored tells notify a completed session has its load.
func (p Publisher) PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error {
	return p.send(ctx, func() (Message, error) { return SessionScored(ev) })
}

// PublishRefresh tells notify a sync's days are scored (notify throttles further).
func (p Publisher) PublishRefresh(ctx context.Context, ev model.RefreshEvent) error {
	return p.send(ctx, func() (Message, error) { return Refresh(ev) })
}

func (p Publisher) send(ctx context.Context, encode func() (Message, error)) error {
	m, err := encode()
	if err != nil {
		return err
	}
	return p.Sender.Send(ctx, m)
}

// Ingested encodes an ingested event.
func Ingested(ev model.IngestedEvent, dedupID string) (Message, error) {
	return encode(KeyIngested, ev.Reason, dedupID, ev)
}

// SessionCompleted encodes a session.completed event.
func SessionCompleted(ev model.SessionCompletedEvent, dedupID string) (Message, error) {
	return encode(KeySessionCompleted, ReasonSessionCompleted, dedupID, ev)
}

// Scored encodes a scored event: one id per user and day.
func Scored(ev model.ScoredEvent) (Message, error) {
	return encode(KeyScored, "scored", "scored:"+ev.UserID.String()+":"+ev.Day, ev)
}

// SessionScored encodes a session.scored event: one id per session.
func SessionScored(ev model.SessionScoredEvent) (Message, error) {
	return encode(KeySessionScored, "session_scored", "session-scored:"+ev.SessionID.String(), ev)
}

// Refresh encodes a refresh event: one id per user and day.
func Refresh(ev model.RefreshEvent) (Message, error) {
	return encode(KeyRefresh, "refresh", "refresh:"+ev.UserID.String()+":"+ev.Day, ev)
}

func encode(key, reason, id string, v any) (Message, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Message{}, err
	}
	return Message{Key: key, ID: id, Body: b, Reason: reason}, nil
}

// CountPublished is ryft_events_published_total for one send by a transport.
func CountPublished(m Message, err error) {
	metrics.EventsPublished.WithLabelValues(m.Reason, metrics.Result(err)).Inc()
}

// DecodeIngested turns a message for scoring into the event scoring handles.
func DecodeIngested(key string, data []byte) (model.IngestedEvent, error) {
	switch key {
	case KeySessionCompleted:
		var s model.SessionCompletedEvent
		if err := json.Unmarshal(data, &s); err != nil {
			return model.IngestedEvent{}, err
		}
		if s.Day == "" {
			return model.IngestedEvent{}, errors.New("session event without a day")
		}
		id := s.SessionID
		return model.IngestedEvent{UserID: s.UserID, Days: []string{s.Day}, Reason: ReasonSessionCompleted,
			RequestID: s.RequestID, SessionID: &id}, nil
	case KeyIngested:
		var ev model.IngestedEvent
		err := json.Unmarshal(data, &ev)
		return ev, err
	}
	return model.IngestedEvent{}, fmt.Errorf("not an event for scoring: %q", key)
}

// NotifyMessage is one event for notify: exactly one of the three is set.
type NotifyMessage struct {
	Scored        *model.ScoredEvent
	SessionScored *model.SessionScoredEvent
	Refresh       *model.RefreshEvent
}

// Reason is the metric label of the message.
func (m NotifyMessage) Reason() string {
	switch {
	case m.Scored != nil:
		return "scored"
	case m.Refresh != nil:
		return "refresh"
	}
	return "session_scored"
}

// DecodeNotify turns a message for notify into a NotifyMessage.
func DecodeNotify(key string, data []byte) (NotifyMessage, error) {
	var m NotifyMessage
	switch key {
	case KeyScored:
		m.Scored = &model.ScoredEvent{}
		if err := json.Unmarshal(data, m.Scored); err != nil {
			return m, fmt.Errorf("bad scored event: %w", err)
		}
		if m.Scored.Day == "" {
			return m, errors.New("scored event without a day")
		}
	case KeyRefresh:
		m.Refresh = &model.RefreshEvent{}
		if err := json.Unmarshal(data, m.Refresh); err != nil {
			return m, fmt.Errorf("bad refresh event: %w", err)
		}
		if m.Refresh.Day == "" {
			return m, errors.New("refresh event without a day")
		}
	case KeySessionScored:
		m.SessionScored = &model.SessionScoredEvent{}
		if err := json.Unmarshal(data, m.SessionScored); err != nil {
			return m, fmt.Errorf("bad session scored event: %w", err)
		}
		if m.SessionScored.Day == "" {
			return m, errors.New("session scored event without a day")
		}
	default:
		return m, fmt.Errorf("not an event for notify: %q", key)
	}
	return m, nil
}

// Guarded runs a handler and turns a panic into an error, so one bad message is
// retried and dead-lettered instead of crashing the consumer's service.
func Guarded(log *slog.Logger, key string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			metrics.ConsumerPanics.WithLabelValues(key).Inc()
			log.Error("event handler panicked", "subject", key, "panic", fmt.Sprint(p))
			err = fmt.Errorf("handler panicked: %v", p)
		}
	}()
	return fn()
}

// permanentError marks a handler failure that retrying cannot fix.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the transport drops the message instead of retrying it
// (the event's user no longer exists). It is counted as dropped, not dead-lettered:
// such an event cannot and need not succeed.
func Permanent(err error) error { return permanentError{err} }

// IsPermanent reports whether err was wrapped by Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// deferError asks for the message again after a delay, without counting it as a
// failure (notify during quiet hours).
type deferError struct{ after time.Duration }

func (d deferError) Error() string { return "deferred for " + d.after.String() }

// Defer hands the message back after d (at least a second, at most a day).
func Defer(d time.Duration) error {
	return deferError{min(max(d, time.Second), 24*time.Hour)}
}

// Deferred reports whether err came from Defer, and for how long.
func Deferred(err error) (time.Duration, bool) {
	var d deferError
	if errors.As(err, &d) {
		return d.after, true
	}
	return 0, false
}

// Combine is the cutover's bus: publishes go to pub, and consuming runs on every
// bus in consume at once (the same handler; idempotency makes the overlap safe).
func Combine(pub Bus, consume ...Bus) Bus {
	return combined{pub: pub, consume: consume}
}

type combined struct {
	pub     Bus
	consume []Bus
}

func (c combined) Send(ctx context.Context, m Message) error { return c.pub.Send(ctx, m) }

func (c combined) PublishIngested(ctx context.Context, ev model.IngestedEvent, id string) error {
	return c.pub.PublishIngested(ctx, ev, id)
}
func (c combined) PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, id string) error {
	return c.pub.PublishSessionCompleted(ctx, ev, id)
}
func (c combined) PublishScored(ctx context.Context, ev model.ScoredEvent) error {
	return c.pub.PublishScored(ctx, ev)
}
func (c combined) PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error {
	return c.pub.PublishSessionScored(ctx, ev)
}
func (c combined) PublishRefresh(ctx context.Context, ev model.RefreshEvent) error {
	return c.pub.PublishRefresh(ctx, ev)
}

func (c combined) ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error {
	return c.all(ctx, func(ctx context.Context, b Bus) error { return b.ConsumeIngested(ctx, log, fn) })
}

func (c combined) ConsumeNotify(ctx context.Context, log *slog.Logger, fn func(context.Context, NotifyMessage) error) error {
	return c.all(ctx, func(ctx context.Context, b Bus) error { return b.ConsumeNotify(ctx, log, fn) })
}

// all consumes on every bus until ctx ends or one fails; a failure stops the others.
func (c combined) all(ctx context.Context, run func(context.Context, Bus) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(c.consume))
	for _, b := range c.consume {
		go func() { errs <- run(ctx, b) }()
	}
	var first error
	for range c.consume {
		if err := <-errs; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

func (c combined) Ready(ctx context.Context) error {
	if err := c.pub.Ready(ctx); err != nil {
		return err
	}
	for _, b := range c.consume {
		if err := b.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c combined) Close() {
	seen := map[Bus]bool{}
	for _, b := range append([]Bus{c.pub}, c.consume...) {
		if !seen[b] {
			seen[b] = true
			b.Close()
		}
	}
}
