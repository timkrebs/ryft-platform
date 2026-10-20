// Package events is the thin NATS JetStream layer between the services.
//
// Stream RYFT persists ryft.ingested (a sync or recompute touched days) and
// ryft.session.completed (a strength session was finished), which scoring
// consumes with one durable pull consumer, so a scoring restart never loses a
// recompute request; and ryft.scored and ryft.session.scored (scoring has
// scored a morning or a session), which notify consumes with its own. RYFT is
// a work queue: a subject has exactly one consumer (D71).
//
// A message that cannot be handled, unparseable or failed on its last
// delivery, moves to stream RYFT_DLQ as ryft.dlq.<subject without "ryft.">
// (ryft.dlq.ingested), with the reason in its headers (D84).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/retry"
)

const (
	StreamName      = "RYFT"
	SubjectIngested = "ryft.ingested"
	// SubjectSessionCompleted carries a model.SessionCompletedEvent (api).
	SubjectSessionCompleted = "ryft.session.completed"
	// SubjectScored carries a model.ScoredEvent, SubjectSessionScored a
	// model.SessionScoredEvent (scoring → notify).
	SubjectScored        = "ryft.scored"
	SubjectSessionScored = "ryft.session.scored"
	// SubjectRefresh carries a model.RefreshEvent (scoring → notify).
	SubjectRefresh  = "ryft.refresh"
	ConsumerScoring = "scoring"
	ConsumerNotify  = "notify"
	maxDeliver      = 10

	DLQStreamName = "RYFT_DLQ"
	dlqPrefix     = "ryft.dlq."
)

// streamSubjects are RYFT's subjects. A new event subject must be added here:
// the stream no longer takes ryft.>, which would overlap the DLQ's ryft.dlq.>.
var streamSubjects = []string{SubjectIngested, SubjectSessionCompleted, SubjectScored, SubjectSessionScored, SubjectRefresh}

// notifySubjects are what notify's consumer takes (a test that sets up the
// consumer must use them too, or it narrows the live one).
var notifySubjects = []string{SubjectScored, SubjectSessionScored, SubjectRefresh}

// DLQSubject is where a message of subject goes when it cannot be handled.
func DLQSubject(subject string) string { return dlqPrefix + strings.TrimPrefix(subject, "ryft.") }

// OriginalSubject is the inverse of DLQSubject.
func OriginalSubject(dlq string) string { return "ryft." + strings.TrimPrefix(dlq, dlqPrefix) }

// Headers on a dead-lettered message.
const (
	HeaderSubject    = "Ryft-Subject"    // the subject it was published to
	HeaderConsumer   = "Ryft-Consumer"   // who gave up on it
	HeaderDeliveries = "Ryft-Deliveries" // how often it was delivered
	HeaderError      = "Ryft-Error"      // the last error, at most 512 bytes
)

type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Options for Connect.
type Options struct {
	// Name is the client name. Replies (publish acks, JetStream API answers)
	// arrive on _INBOX_<name>.>, the only subjects the service may subscribe
	// to (deploy/k8s/base/nats/nats.conf).
	Name string
	// User and Password authenticate the service as its own NATS user.
	// Empty: no authentication (a NATS without users).
	User, Password string
	// OwnStream creates or updates the RYFT stream. Only scoring, the
	// consumer, may; publishers only publish.
	OwnStream bool
}

// Connect dials NATS and, with OwnStream, ensures the stream exists (idempotent).
func Connect(ctx context.Context, url string, o Options) (*Bus, error) {
	opts := []nats.Option{
		nats.Name(o.Name),
		nats.CustomInboxPrefix("_INBOX_" + o.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
	}
	if o.User != "" {
		opts = append(opts, nats.UserInfo(o.User, o.Password))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if !o.OwnStream {
		return &Bus{nc: nc, js: js}, nil
	}
	// RYFT first: until it gives up ryft.>, the DLQ's subjects would overlap it.
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  streamSubjects,
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      DLQStreamName,
		Subjects:  []string{dlqPrefix + ">"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    30 * 24 * time.Hour,
		MaxBytes:  64 << 20,
		Discard:   jetstream.DiscardOld,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure dead-letter stream: %w", err)
	}
	return &Bus{nc: nc, js: js}, nil
}

// deadLetter moves msg to RYFT_DLQ and terminates it in RYFT. When that
// publish fails, the message is NAKed instead: it stays in RYFT (until its
// MaxAge), so nothing vanishes.
func (b *Bus) deadLetter(ctx context.Context, log *slog.Logger, msg jetstream.Msg, consumer string, delivered uint64, cause error) {
	out := nats.NewMsg(DLQSubject(msg.Subject()))
	out.Data = msg.Data()
	out.Header.Set(HeaderSubject, msg.Subject())
	out.Header.Set(HeaderConsumer, consumer)
	out.Header.Set(HeaderDeliveries, strconv.FormatUint(delivered, 10))
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	out.Header.Set(HeaderError, reason)
	short, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := b.js.PublishMsg(short, out); err != nil {
		metrics.EventsDLQ.WithLabelValues(msg.Subject(), "error").Inc()
		log.Error("dead-letter publish failed, the message stays in RYFT", "err", err, "subject", msg.Subject())
		_ = msg.NakWithDelay(30 * time.Second)
		return
	}
	metrics.EventsDLQ.WithLabelValues(msg.Subject(), "ok").Inc()
	log.Warn("event dead-lettered", "subject", msg.Subject(), "dlq", out.Subject, "deliveries", delivered, "err", cause)
	_ = msg.Term()
}

// watch reports a consumer's backlog every 30 s until ctx ends, and for
// scoring (the streams' owner) the size of both streams.
func (b *Bus) watch(ctx context.Context, cons jetstream.Consumer, name string) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		if info, err := cons.Info(short); err == nil {
			metrics.ConsumerPending.WithLabelValues(name).Set(float64(info.NumPending))
			metrics.ConsumerAckPending.WithLabelValues(name).Set(float64(info.NumAckPending))
		}
		if name == ConsumerScoring {
			for _, stream := range []string{StreamName, DLQStreamName} {
				if s, err := b.js.Stream(short, stream); err == nil {
					metrics.StreamMessages.WithLabelValues(stream).Set(float64(s.CachedInfo().State.Msgs))
				}
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Open is Connect, retried with backoff for up to five minutes while NATS is
// not reachable yet (its pod starting) or refuses the login.
func Open(ctx context.Context, url string, o Options, log *slog.Logger) (*Bus, error) {
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var bus *Bus
	err := retry.Until(wait, log, "nats", retry.Startup, func(ctx context.Context) error {
		b, err := Connect(ctx, url, o)
		bus = b
		return err
	})
	return bus, err
}

func (b *Bus) Close() { _ = b.nc.Drain() }

func (b *Bus) Ready(ctx context.Context) error {
	if !b.nc.IsConnected() {
		return fmt.Errorf("nats disconnected")
	}
	return nil
}

// PublishIngested queues an event for scoring. A non-empty dedupID makes
// JetStream drop identical publishes within its duplicate window (2 minutes).
func (b *Bus) PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var opts []jetstream.PublishOpt
	if dedupID != "" {
		opts = append(opts, jetstream.WithMsgID(dedupID))
	}
	_, err = b.js.Publish(ctx, SubjectIngested, data, opts...)
	metrics.EventsPublished.WithLabelValues(ev.Reason, metrics.Result(err)).Inc()
	return err
}

// PublishSessionCompleted queues a finished session's day for scoring, which
// handles it like an ingest event for that day.
func (b *Bus) PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var opts []jetstream.PublishOpt
	if dedupID != "" {
		opts = append(opts, jetstream.WithMsgID(dedupID))
	}
	_, err = b.js.Publish(ctx, SubjectSessionCompleted, data, opts...)
	metrics.EventsPublished.WithLabelValues(ReasonSessionCompleted, metrics.Result(err)).Inc()
	return err
}

// PublishScored tells notify a day got its morning score; one per user and
// day within JetStream's duplicate window.
func (b *Bus) PublishScored(ctx context.Context, ev model.ScoredEvent) error {
	return b.publish(ctx, SubjectScored, "scored", ev, "scored:"+ev.UserID.String()+":"+ev.Day)
}

// PublishSessionScored tells notify a completed session has its load.
func (b *Bus) PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error {
	return b.publish(ctx, SubjectSessionScored, "session_scored", ev, "session-scored:"+ev.SessionID.String())
}

// PublishRefresh tells notify that a sync's days are scored; at most one per
// user and day within JetStream's duplicate window (notify throttles further).
func (b *Bus) PublishRefresh(ctx context.Context, ev model.RefreshEvent) error {
	return b.publish(ctx, SubjectRefresh, "refresh", ev, "refresh:"+ev.UserID.String()+":"+ev.Day)
}

func (b *Bus) publish(ctx context.Context, subject, reason string, v any, dedupID string) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, subject, data, jetstream.WithMsgID(dedupID))
	metrics.EventsPublished.WithLabelValues(reason, metrics.Result(err)).Inc()
	return err
}

// ReasonSessionCompleted is the IngestedEvent.Reason a session event becomes.
const ReasonSessionCompleted = "session_completed"

// decode turns a message of either subject into the event scoring handles.
func decode(msg jetstream.Msg) (model.IngestedEvent, error) {
	return DecodeIngested(msg.Subject(), msg.Data())
}

// DecodeIngested is decode for a subject and payload (a dead letter's, too).
func DecodeIngested(subject string, data []byte) (model.IngestedEvent, error) {
	if subject == SubjectSessionCompleted {
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
	}
	if subject != SubjectIngested {
		return model.IngestedEvent{}, fmt.Errorf("not a scoring subject: %s", subject)
	}
	var ev model.IngestedEvent
	err := json.Unmarshal(data, &ev)
	return ev, err
}

// DeadLetter is one message in RYFT_DLQ.
type DeadLetter struct {
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Subject    string    `json:"subject"` // the original subject
	Consumer   string    `json:"consumer"`
	Deliveries string    `json:"deliveries"`
	Error      string    `json:"error"`
	Data       string    `json:"data"` // as published, possibly not JSON
}

// DeadLetters lists RYFT_DLQ, oldest first.
func (b *Bus) DeadLetters(ctx context.Context) ([]DeadLetter, error) {
	s, err := b.js.Stream(ctx, DLQStreamName)
	if err != nil {
		return nil, err
	}
	state := s.CachedInfo().State
	var out []DeadLetter
	for seq := state.FirstSeq; state.Msgs > 0 && seq <= state.LastSeq; seq++ {
		m, err := s.GetMsg(ctx, seq)
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			continue // deleted
		}
		if err != nil {
			return out, err
		}
		out = append(out, DeadLetter{Seq: seq, Time: m.Time, Subject: OriginalSubject(m.Subject),
			Consumer: m.Header.Get(HeaderConsumer), Deliveries: m.Header.Get(HeaderDeliveries),
			Error: m.Header.Get(HeaderError), Data: string(m.Data)})
	}
	return out, nil
}

// DropDeadLetterStream deletes RYFT_DLQ. Only for rolling back to a release
// from before it, whose scoring sets RYFT back to ryft.>, which would overlap
// it (docs/OPERATIONS.md → Deploy and roll back).
func (b *Bus) DropDeadLetterStream(ctx context.Context) error {
	return b.js.DeleteStream(ctx, DLQStreamName)
}

// DeleteDeadLetter removes one message from RYFT_DLQ.
func (b *Bus) DeleteDeadLetter(ctx context.Context, seq uint64) error {
	s, err := b.js.Stream(ctx, DLQStreamName)
	if err != nil {
		return err
	}
	return s.DeleteMsg(ctx, seq)
}

// guarded runs a handler and turns a panic into an error, so one bad message
// is retried and dead-lettered instead of crashing the consumer's service.
func guarded(log *slog.Logger, subject string, fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			metrics.ConsumerPanics.WithLabelValues(subject).Inc()
			log.Error("event handler panicked", "subject", subject, "panic", fmt.Sprint(p))
			err = fmt.Errorf("handler panicked: %v", p)
		}
	}()
	return fn()
}

// permanentError marks a handler failure that retrying cannot fix.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so ConsumeIngested terminates the message instead of
// redelivering it (e.g. the event's user no longer exists).
func Permanent(err error) error { return permanentError{err} }

// deferError asks for the message again after a delay, without counting it as
// a failure (notify during quiet hours).
type deferError struct{ after time.Duration }

func (d deferError) Error() string { return "deferred for " + d.after.String() }

// Defer makes ConsumeNotify hand the message back after d (at least a second,
// at most a day).
func Defer(d time.Duration) error {
	return deferError{min(max(d, time.Second), 24*time.Hour)}
}

// ConsumeIngested blocks, handing every event of both subjects to fn (a
// session event as an IngestedEvent for its day). fn returning an error NAKs
// the message so JetStream redelivers it after a delay.
func (b *Bus) ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error {
	cfg := jetstream.ConsumerConfig{
		Durable:        ConsumerScoring,
		FilterSubjects: []string{SubjectIngested, SubjectSessionCompleted},
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        5 * time.Minute,
		MaxDeliver:     maxDeliver,
	}
	var cons jetstream.Consumer
	var err error
	for attempt := 1; ; attempt++ {
		cons, err = b.js.CreateOrUpdateConsumer(ctx, StreamName, cfg)
		// Moving the existing durable to a second filter subject was once answered
		// without the subjects (NATS 2.11.17, 2026-10-19, while the previous pod still
		// pulled), which nats.go reports as unsupported; the same request a second
		// later succeeded. Retry that instead of exiting.
		if err == nil || !errors.Is(err, jetstream.ErrConsumerMultipleFilterSubjectsNotSupported) || attempt == 5 {
			break
		}
		log.Warn("consumer update not confirmed yet, retrying", "err", err, "attempt", attempt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		return fmt.Errorf("ensure consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		delivered := uint64(1)
		if md, err := msg.Metadata(); err == nil {
			delivered = md.NumDelivered
		}
		if delivered > 1 {
			metrics.Redeliveries.WithLabelValues(ConsumerScoring).Inc()
		}
		ev, err := decode(msg)
		if err != nil {
			log.Error("bad event, dead-lettering", "err", err, "subject", msg.Subject())
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			b.deadLetter(ctx, log, msg, ConsumerScoring, delivered, err)
			return
		}
		if err := guarded(log, msg.Subject(), func() error { return fn(ctx, ev) }); err != nil {
			var perm permanentError
			if errors.As(err, &perm) {
				log.Warn("event cannot be handled, dropping", "err", err, "user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "dropped").Inc()
				_ = msg.Term()
				return
			}
			if delivered >= maxDeliver {
				log.Error("handle event failed on last delivery, dead-lettering", "err", err, "days", ev.Days,
					"user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "failed").Inc()
				b.deadLetter(ctx, log, msg, ConsumerScoring, delivered, err)
				return
			}
			log.Error("handle event failed, will retry", "err", err, "days", ev.Days, "delivery", delivered,
				"user_id", ev.UserID, "request_id", ev.RequestID)
			metrics.EventsHandled.WithLabelValues(ev.Reason, "retry").Inc()
			_ = msg.NakWithDelay(30 * time.Second)
			return
		}
		metrics.EventsHandled.WithLabelValues(ev.Reason, "ok").Inc()
		_ = msg.Ack()
	})
	if err != nil {
		return err
	}
	go b.watch(ctx, cons, ConsumerScoring)
	<-ctx.Done()
	cc.Stop()
	return nil
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

func decodeNotify(msg jetstream.Msg) (NotifyMessage, error) {
	var m NotifyMessage
	switch msg.Subject() {
	case SubjectScored:
		m.Scored = &model.ScoredEvent{}
		if err := json.Unmarshal(msg.Data(), m.Scored); err != nil {
			return m, fmt.Errorf("bad scored event: %w", err)
		}
		if m.Scored.Day == "" {
			return m, errors.New("scored event without a day")
		}
	case SubjectRefresh:
		m.Refresh = &model.RefreshEvent{}
		if err := json.Unmarshal(msg.Data(), m.Refresh); err != nil {
			return m, fmt.Errorf("bad refresh event: %w", err)
		}
		if m.Refresh.Day == "" {
			return m, errors.New("refresh event without a day")
		}
	case SubjectSessionScored:
		m.SessionScored = &model.SessionScoredEvent{}
		if err := json.Unmarshal(msg.Data(), m.SessionScored); err != nil {
			return m, fmt.Errorf("bad session scored event: %w", err)
		}
		if m.SessionScored.Day == "" {
			return m, errors.New("session scored event without a day")
		}
	default:
		return m, fmt.Errorf("unexpected subject %s", msg.Subject())
	}
	return m, nil
}

// ConsumeNotify blocks, handing every ryft.scored and ryft.session.scored to
// fn with notify's own durable consumer (the stream is scoring's; notify only
// creates its consumer). fn returning Defer(d) hands the message back after d;
// another error retries it after 30 s, Permanent terminates it.
func (b *Bus) ConsumeNotify(ctx context.Context, log *slog.Logger, fn func(context.Context, NotifyMessage) error) error {
	cfg := jetstream.ConsumerConfig{
		Durable:        ConsumerNotify,
		FilterSubjects: notifySubjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Minute,
		// Deferrals count as deliveries: quiet hours hand a message back once or
		// twice, a retry up to ten times.
		MaxDeliver: maxDeliver + 5,
	}
	// scoring creates the stream; on a fresh install notify may start first.
	var cons jetstream.Consumer
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := retry.Until(wait, log, "stream "+StreamName, retry.Startup, func(ctx context.Context) error {
		c, err := b.js.CreateOrUpdateConsumer(ctx, StreamName, cfg)
		cons = c
		return err
	})
	if err != nil {
		return fmt.Errorf("ensure consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		delivered := uint64(1)
		if md, err := msg.Metadata(); err == nil {
			delivered = md.NumDelivered
		}
		if delivered > 1 {
			metrics.Redeliveries.WithLabelValues(ConsumerNotify).Inc()
		}
		m, err := decodeNotify(msg)
		if err != nil {
			log.Error("bad event, dead-lettering", "err", err, "subject", msg.Subject())
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			b.deadLetter(ctx, log, msg, ConsumerNotify, delivered, err)
			return
		}
		err = guarded(log, msg.Subject(), func() error { return fn(ctx, m) })
		var perm permanentError
		var later deferError
		switch {
		case err == nil:
			metrics.EventsHandled.WithLabelValues(m.Reason(), "ok").Inc()
			_ = msg.Ack()
		case errors.As(err, &later):
			metrics.EventsHandled.WithLabelValues(m.Reason(), "deferred").Inc()
			_ = msg.NakWithDelay(later.after)
		case errors.As(err, &perm):
			log.Warn("event cannot be handled, dropping", "err", err)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "dropped").Inc()
			_ = msg.Term()
		case delivered >= maxDeliver+5:
			log.Error("handle event failed on last delivery, dead-lettering", "err", err, "delivery", delivered)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "failed").Inc()
			b.deadLetter(ctx, log, msg, ConsumerNotify, delivered, err)
		default:
			log.Error("handle event failed, will retry", "err", err, "delivery", delivered)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "retry").Inc()
			_ = msg.NakWithDelay(30 * time.Second)
		}
	})
	if err != nil {
		return err
	}
	go b.watch(ctx, cons, ConsumerNotify)
	<-ctx.Done()
	cc.Stop()
	return nil
}
