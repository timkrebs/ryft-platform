// Package events is the thin NATS JetStream layer between the services.
//
// Stream RYFT persists ryft.ingested (a sync or recompute touched days) and
// ryft.session.completed (a strength session was finished), which scoring
// consumes with one durable pull consumer, so a scoring restart never loses a
// recompute request; and ryft.scored and ryft.session.scored (scoring has
// scored a morning or a session), which notify consumes with its own. RYFT is
// a work queue: a subject has exactly one consumer (D71).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	ConsumerScoring      = "scoring"
	ConsumerNotify       = "notify"
	maxDeliver           = 10
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
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{"ryft.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	return &Bus{nc: nc, js: js}, nil
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
	if msg.Subject() == SubjectSessionCompleted {
		var s model.SessionCompletedEvent
		if err := json.Unmarshal(msg.Data(), &s); err != nil {
			return model.IngestedEvent{}, err
		}
		if s.Day == "" {
			return model.IngestedEvent{}, errors.New("session event without a day")
		}
		id := s.SessionID
		return model.IngestedEvent{UserID: s.UserID, Days: []string{s.Day}, Reason: ReasonSessionCompleted,
			RequestID: s.RequestID, SessionID: &id}, nil
	}
	var ev model.IngestedEvent
	err := json.Unmarshal(msg.Data(), &ev)
	return ev, err
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
			log.Error("bad event, dropping", "err", err)
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			_ = msg.Term()
			return
		}
		if err := fn(ctx, ev); err != nil {
			var perm permanentError
			if errors.As(err, &perm) {
				log.Warn("event cannot be handled, dropping", "err", err, "user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "dropped").Inc()
				_ = msg.Term()
				return
			}
			if delivered >= maxDeliver {
				log.Error("handle event failed on last delivery, giving up", "err", err, "days", ev.Days,
					"user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "failed").Inc()
			} else {
				log.Error("handle event failed, will retry", "err", err, "days", ev.Days, "delivery", delivered,
					"user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "retry").Inc()
			}
			_ = msg.NakWithDelay(30 * time.Second)
			return
		}
		metrics.EventsHandled.WithLabelValues(ev.Reason, "ok").Inc()
		_ = msg.Ack()
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}

// NotifyMessage is one event for notify: exactly one of the two is set.
type NotifyMessage struct {
	Scored        *model.ScoredEvent
	SessionScored *model.SessionScoredEvent
}

// Reason is the metric label of the message.
func (m NotifyMessage) Reason() string {
	if m.Scored != nil {
		return "scored"
	}
	return "session_scored"
}

func decodeNotify(msg jetstream.Msg) (NotifyMessage, error) {
	var m NotifyMessage
	switch msg.Subject() {
	case SubjectScored:
		m.Scored = &model.ScoredEvent{}
		if err := json.Unmarshal(msg.Data(), m.Scored); err != nil || m.Scored.Day == "" {
			return m, fmt.Errorf("bad scored event: %w", err)
		}
	case SubjectSessionScored:
		m.SessionScored = &model.SessionScoredEvent{}
		if err := json.Unmarshal(msg.Data(), m.SessionScored); err != nil || m.SessionScored.Day == "" {
			return m, fmt.Errorf("bad session scored event: %w", err)
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
		FilterSubjects: []string{SubjectScored, SubjectSessionScored},
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
			log.Error("bad event, dropping", "err", err, "subject", msg.Subject())
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			_ = msg.Term()
			return
		}
		err = fn(ctx, m)
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
		default:
			result := "retry"
			if delivered >= maxDeliver+5 {
				result = "failed"
			}
			log.Error("handle event failed", "err", err, "delivery", delivered, "result", result)
			metrics.EventsHandled.WithLabelValues(m.Reason(), result).Inc()
			_ = msg.NakWithDelay(30 * time.Second)
		}
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}
