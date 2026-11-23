package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Event keys: the ryft-key attribute of a message, one Pub/Sub topic each.
const (
	KeyIngested         = "ingested"
	KeySessionCompleted = "session.completed"
	KeyScored           = "scored"
	KeySessionScored    = "session.scored"
	KeyRefresh          = "refresh"
	KeyInboxReceived    = "inbox.received" // ingest's own: a provider record stored by the hook (D257)
)

// The three consumers, a dead-letter topic each.
const (
	ConsumerScoring = "scoring"
	ConsumerNotify  = "notify"
	ConsumerIngest  = "ingest"
)

// The keys each consumer takes. A new event key must be added to one of them (and
// to the transport's topology).
var (
	ScoringKeys = []string{KeyIngested, KeySessionCompleted}
	NotifyKeys  = []string{KeyScored, KeySessionScored, KeyRefresh}
	IngestKeys  = []string{KeyInboxReceived}
)

// ReasonSessionCompleted is the IngestedEvent.Reason a session event becomes.
const ReasonSessionCompleted = "session_completed"

// ReasonInbox is the metric reason of an inbox.received event.
const ReasonInbox = "inbox"

// Message is one event on the wire.
type Message struct {
	Key    string // event key (above)
	ID     string // stable for the same trigger: the ryft-message-id attribute (nothing dedups on it, D100)
	Body   []byte // JSON
	Reason string // the label of ryft_events_published_total
}

// Sender moves a message: a transport, or the outbox (which a relay drains into one).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Bus is what the services publish with: the five events, and the raw Send the
// outbox's relay uses. internal/events/pubsub implements it. It has no Ready and no
// Close: Pub/Sub's REST publish holds no connection, so there is nothing to be
// connected or to shut down, and consuming is a push handler the transport serves.
type Bus interface {
	// Sender is the raw publish the typed ones below are made of; the outbox's relay
	// sends its rows with it.
	Sender
	PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error
	PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error
	PublishScored(ctx context.Context, ev model.ScoredEvent) error
	PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error
	PublishRefresh(ctx context.Context, ev model.RefreshEvent) error
}

// Handler handles one pushed event: key is the event key the transport checked
// (the push path's, or notify's wait key), data the message's body. The transport
// settles the message by the error: nil acks it; Permanent drops it; Defer asks for
// it again later; Bad dead-letters it at once; anything else is a nack, retried and
// at last dead-lettered by Pub/Sub. ScoringHandler and NotifyHandler build one from a
// typed function.
type Handler func(ctx context.Context, key string, data []byte) error

// ScoringHandler decodes scoring's two events (DecodeIngested) and hands fn the
// IngestedEvent. Data that does not decode is Bad; the metric reason is the event's,
// when it is one of Reasons.
func ScoringHandler(fn func(context.Context, model.IngestedEvent) error) Handler {
	return func(ctx context.Context, key string, data []byte) error {
		ev, err := DecodeIngested(key, data)
		if err != nil {
			return Bad(err)
		}
		SetReason(ctx, ev.Reason)
		return fn(ctx, ev)
	}
}

// NotifyHandler decodes notify's three events (DecodeNotify) and hands fn the
// NotifyMessage. Data that does not decode is Bad; the metric reason is the message's.
func NotifyHandler(fn func(context.Context, NotifyMessage) error) Handler {
	return func(ctx context.Context, key string, data []byte) error {
		m, err := DecodeNotify(key, data)
		if err != nil {
			return Bad(err)
		}
		SetReason(ctx, m.Reason())
		return fn(ctx, m)
	}
}

// reasonSlot holds the metric reason of the event a handler is handling.
type reasonSlot struct{ reason string }

type reasonKey struct{}

// ReasonUnknown is the reason of an event that did not decode, or whose reason is none
// the publishers write.
const ReasonUnknown = "unknown"

// Reasons are the metric reasons the publishers write: an ingested event's (ingest's
// sync and fold-workouts, the api's recompute, merge, session edits and training
// import), session.completed's, and notify's three. The reason label of
// ryft_events_handled_total is one of them or ReasonUnknown. A new reason in a publisher
// must be added here, or it is counted as unknown.
var Reasons = []string{model.ReasonSync, "recompute", "merge", "session", model.ReasonImport,
	model.ReasonConnect, model.ReasonDisconnect, ReasonInbox,
	ReasonSessionCompleted, "scored", "session_scored", "refresh"}

// MetricReason is reason when it is one of Reasons, else ReasonUnknown. An ingested
// event's reason comes from its body, and a label taken from a body unchecked would let
// a forged event create any number of series.
func MetricReason(reason string) string {
	if slices.Contains(Reasons, reason) {
		return reason
	}
	return ReasonUnknown
}

// WithReason gives ctx a slot for the metric reason of the event about to be
// handled, and returns the function that reads it (ReasonUnknown until SetReason).
// The transport calls it before the Handler and labels ryft_events_handled_total
// with what it reads after.
func WithReason(ctx context.Context) (context.Context, func() string) {
	s := &reasonSlot{reason: ReasonUnknown}
	return context.WithValue(ctx, reasonKey{}, s), func() string { return s.reason }
}

// SetReason records the metric reason of the event being handled, as MetricReason
// bounds it; ScoringHandler and NotifyHandler call it once the data decodes. Outside
// WithReason it does nothing.
func SetReason(ctx context.Context, reason string) {
	if s, ok := ctx.Value(reasonKey{}).(*reasonSlot); ok && reason != "" {
		s.reason = MetricReason(reason)
	}
}

// Publisher implements Bus's publishing half on a Sender.
type Publisher struct{ Sender Sender }

// PublishIngested queues days for scoring. A non-empty dedupID marks repeats of the
// same trigger (Pub/Sub delivers every copy, and rescoring a day twice changes
// nothing, D100).
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
			return model.IngestedEvent{}, logx.JSONError(err)
		}
		if s.Day == "" {
			return model.IngestedEvent{}, errors.New("session event without a day")
		}
		id := s.SessionID
		return model.IngestedEvent{UserID: s.UserID, Days: []string{s.Day}, Reason: ReasonSessionCompleted,
			RequestID: s.RequestID, SessionID: &id}, nil
	case KeyIngested:
		var ev model.IngestedEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return ev, logx.JSONError(err)
		}
		return ev, nil
	}
	return model.IngestedEvent{}, fmt.Errorf("not an event for scoring: %q", key)
}

// InboxReceived encodes an inbox.received event. The message id carries the revision, so a changed re-push is
// another message and a repeat of the same one is the same (nothing dedups on it, D100).
func InboxReceived(ev model.InboxReceivedEvent) (Message, error) {
	return encode(KeyInboxReceived, ReasonInbox, fmt.Sprintf("inbox:%d:%d", ev.InboxID, ev.Revision), ev)
}

// DecodeInboxReceived turns a message for ingest into its event.
func DecodeInboxReceived(key string, data []byte) (model.InboxReceivedEvent, error) {
	var ev model.InboxReceivedEvent
	if key != KeyInboxReceived {
		return ev, fmt.Errorf("not an event for ingest: %q", key)
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return ev, logx.JSONError(err)
	}
	if ev.InboxID <= 0 {
		return ev, errors.New("inbox event without an id")
	}
	return ev, nil
}

// IngestHandler decodes ingest's one event (DecodeInboxReceived) and hands fn the InboxReceivedEvent. Data that
// does not decode is Bad; the metric reason is ReasonInbox.
func IngestHandler(fn func(context.Context, model.InboxReceivedEvent) error) Handler {
	return func(ctx context.Context, key string, data []byte) error {
		ev, err := DecodeInboxReceived(key, data)
		if err != nil {
			return Bad(err)
		}
		SetReason(ctx, ReasonInbox)
		return fn(ctx, ev)
	}
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
			return m, fmt.Errorf("bad scored event: %w", logx.JSONError(err))
		}
		if m.Scored.Day == "" {
			return m, errors.New("scored event without a day")
		}
	case KeyRefresh:
		m.Refresh = &model.RefreshEvent{}
		if err := json.Unmarshal(data, m.Refresh); err != nil {
			return m, fmt.Errorf("bad refresh event: %w", logx.JSONError(err))
		}
		if m.Refresh.Day == "" {
			return m, errors.New("refresh event without a day")
		}
	case KeySessionScored:
		m.SessionScored = &model.SessionScoredEvent{}
		if err := json.Unmarshal(data, m.SessionScored); err != nil {
			return m, fmt.Errorf("bad session scored event: %w", logx.JSONError(err))
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
// ryft_event_handler_panics_total counts it under key, which must be a key the
// transport trusts (never one a publisher wrote).
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

// badError marks an event that cannot be read: its data does not decode, or its key
// is not one the consumer takes.
type badError struct{ err error }

func (b badError) Error() string { return b.err.Error() }
func (b badError) Unwrap() error { return b.err }

// Bad wraps err so the transport dead-letters the message at once, with err as its
// ryft-error, and acks it: retrying an event that does not decode cannot help.
// ScoringHandler and NotifyHandler return it for a decode failure; a handler's own
// failure is not Bad.
func Bad(err error) error { return badError{err} }

// IsBad reports whether err was wrapped by Bad.
func IsBad(err error) bool {
	var b badError
	return errors.As(err, &b)
}

// rejectedError marks a send the transport refused for good.
type rejectedError struct{ err error }

func (r rejectedError) Error() string { return r.err.Error() }
func (r rejectedError) Unwrap() error { return r.err }

// Rejected wraps a send error that no retry can fix: the key has no topic, the body
// cannot be a message there, or the broker refused the request itself (Pub/Sub's 400,
// 403, 404). A transport returns it from Send; the outbox then sets the row aside at once
// instead of spending its attempts. Any other send error counts as transient.
func Rejected(err error) error { return rejectedError{err} }

// IsRejected reports whether err was wrapped by Rejected.
func IsRejected(err error) bool {
	var r rejectedError
	return errors.As(err, &r)
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

type redeliveryKey struct{}

// WithRedelivery marks ctx as handling a message that came before: one Pub/Sub
// delivers again after a nack or a lost ack (deliveryAttempt above 1), or a dead
// letter replayed with scripts/dlq.sh (ryft-replayed). The transport sets it.
func WithRedelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, redeliveryKey{}, true)
}

// Redelivered reports whether ctx handles such a message. A handler that
// decides what else to redo from what its own write changed cannot trust
// "nothing changed" then: the earlier attempt may have written the change and
// failed before the rest (scoring's re-score of the days after, D100).
func Redelivered(ctx context.Context) bool {
	v, _ := ctx.Value(redeliveryKey{}).(bool)
	return v
}
