// Package natsbus is events.Bus on NATS JetStream, the transport until the RabbitMQ
// cutover (D97); it goes with the cutover's last release.
//
// Stream RYFT persists ryft.<key> for the five event keys; scoring and notify each
// consume their keys with one durable pull consumer. RYFT is a work queue: a subject
// has exactly one consumer (D71). A message that cannot be handled, unparseable or
// failed on its last delivery, moves to stream RYFT_DLQ as ryft.dlq.<key> with the
// reason in its headers (D84).
package natsbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/retry"
)

const (
	StreamName = "RYFT"
	maxDeliver = 10

	DLQStreamName = "RYFT_DLQ"
	dlqPrefix     = "ryft.dlq."
)

// Subject is the NATS subject of an event key.
func Subject(key string) string { return "ryft." + key }

// streamSubjects are RYFT's subjects (not ryft.>, which would overlap the DLQ's
// ryft.dlq.>).
var streamSubjects = subjects(append(append([]string{}, events.ScoringKeys...), events.NotifyKeys...))

// notifySubjects are what notify's consumer takes (a test that sets up the consumer
// must use them too, or it narrows the live one).
var notifySubjects = subjects(events.NotifyKeys)

func subjects(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = Subject(k)
	}
	return out
}

// DLQSubject is where a message of subject goes when it cannot be handled.
func DLQSubject(subject string) string { return dlqPrefix + strings.TrimPrefix(subject, "ryft.") }

// OriginalSubject is the inverse of DLQSubject.
func OriginalSubject(dlq string) string { return "ryft." + strings.TrimPrefix(dlq, dlqPrefix) }

// Bus is events.Bus on JetStream.
type Bus struct {
	events.Publisher
	nc *nats.Conn
	js jetstream.JetStream
}

// Options for Connect.
type Options struct {
	// Name is the client name. Replies (publish acks, JetStream API answers) arrive
	// on _INBOX_<name>.>, the only subjects the service may subscribe to
	// (deploy/k8s/base/nats/nats.conf).
	Name string
	// User and Password authenticate the service as its own NATS user. Empty: no
	// authentication (a NATS without users).
	User, Password string
	// OwnStream creates or updates the RYFT stream. Only scoring, the consumer, may;
	// publishers only publish.
	OwnStream bool
}

// Connect dials NATS and, with OwnStream, ensures the streams exist (idempotent).
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
	b := &Bus{nc: nc, js: js}
	b.Publisher = events.Publisher{Sender: b}
	if !o.OwnStream {
		return b, nil
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
	return b, nil
}

// Open is Connect, retried with backoff for up to five minutes while NATS is not
// reachable yet (its pod starting) or refuses the login.
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

// Send publishes m on ryft.<key>; a non-empty ID makes JetStream drop identical
// publishes within its duplicate window (2 minutes).
func (b *Bus) Send(ctx context.Context, m events.Message) error {
	var opts []jetstream.PublishOpt
	if m.ID != "" {
		opts = append(opts, jetstream.WithMsgID(m.ID))
	}
	_, err := b.js.Publish(ctx, Subject(m.Key), m.Body, opts...)
	events.CountPublished(m, err)
	return err
}

// deadLetter moves msg to RYFT_DLQ and terminates it in RYFT. When that publish
// fails, the message is NAKed instead: it stays in RYFT (until its MaxAge), so
// nothing vanishes.
func (b *Bus) deadLetter(ctx context.Context, log *slog.Logger, msg jetstream.Msg, consumer string, delivered uint64, cause error) {
	out := nats.NewMsg(DLQSubject(msg.Subject()))
	out.Data = msg.Data()
	out.Header.Set(events.HeaderSubject, msg.Subject())
	out.Header.Set(events.HeaderConsumer, consumer)
	out.Header.Set(events.HeaderDeliveries, strconv.FormatUint(delivered, 10))
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	out.Header.Set(events.HeaderError, reason)
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

// watch reports a consumer's backlog every 30 s until ctx ends, and for scoring (the
// streams' owner) the size of both streams.
func (b *Bus) watch(ctx context.Context, cons jetstream.Consumer, name string) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		if info, err := cons.Info(short); err == nil {
			metrics.ConsumerPending.WithLabelValues(name).Set(float64(info.NumPending))
			metrics.ConsumerAckPending.WithLabelValues(name).Set(float64(info.NumAckPending))
		}
		if name == events.ConsumerScoring {
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

// key is an event key from a RYFT subject.
func key(subject string) string { return strings.TrimPrefix(subject, "ryft.") }

// ConsumeIngested blocks, handing every event of both subjects to fn. fn returning
// an error NAKs the message so JetStream redelivers it after a delay.
func (b *Bus) ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error {
	cfg := jetstream.ConsumerConfig{
		Durable:        events.ConsumerScoring,
		FilterSubjects: subjects(events.ScoringKeys),
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
			metrics.Redeliveries.WithLabelValues(events.ConsumerScoring).Inc()
		}
		ev, err := events.DecodeIngested(key(msg.Subject()), msg.Data())
		if err != nil {
			log.Error("bad event, dead-lettering", "err", err, "subject", msg.Subject())
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			b.deadLetter(ctx, log, msg, events.ConsumerScoring, delivered, err)
			return
		}
		if err := events.Guarded(log, msg.Subject(), func() error { return fn(ctx, ev) }); err != nil {
			if events.IsPermanent(err) {
				log.Warn("event cannot be handled, dropping", "err", err, "user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "dropped").Inc()
				_ = msg.Term()
				return
			}
			if delivered >= maxDeliver {
				log.Error("handle event failed on last delivery, dead-lettering", "err", err, "days", ev.Days,
					"user_id", ev.UserID, "request_id", ev.RequestID)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "failed").Inc()
				b.deadLetter(ctx, log, msg, events.ConsumerScoring, delivered, err)
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
	go b.watch(ctx, cons, events.ConsumerScoring)
	<-ctx.Done()
	cc.Stop()
	return nil
}

// ConsumeNotify blocks, handing every notify subject to fn with notify's own durable
// consumer (the stream is scoring's; notify only creates its consumer). fn returning
// events.Defer(d) hands the message back after d; another error retries it after
// 30 s, events.Permanent terminates it.
func (b *Bus) ConsumeNotify(ctx context.Context, log *slog.Logger, fn func(context.Context, events.NotifyMessage) error) error {
	cfg := jetstream.ConsumerConfig{
		Durable:        events.ConsumerNotify,
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
			metrics.Redeliveries.WithLabelValues(events.ConsumerNotify).Inc()
		}
		m, err := events.DecodeNotify(key(msg.Subject()), msg.Data())
		if err != nil {
			log.Error("bad event, dead-lettering", "err", err, "subject", msg.Subject())
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			b.deadLetter(ctx, log, msg, events.ConsumerNotify, delivered, err)
			return
		}
		err = events.Guarded(log, msg.Subject(), func() error { return fn(ctx, m) })
		after, deferred := events.Deferred(err)
		switch {
		case err == nil:
			metrics.EventsHandled.WithLabelValues(m.Reason(), "ok").Inc()
			_ = msg.Ack()
		case deferred:
			metrics.EventsHandled.WithLabelValues(m.Reason(), "deferred").Inc()
			_ = msg.NakWithDelay(after)
		case events.IsPermanent(err):
			log.Warn("event cannot be handled, dropping", "err", err)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "dropped").Inc()
			_ = msg.Term()
		case delivered >= maxDeliver+5:
			log.Error("handle event failed on last delivery, dead-lettering", "err", err, "delivery", delivered)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "failed").Inc()
			b.deadLetter(ctx, log, msg, events.ConsumerNotify, delivered, err)
		default:
			log.Error("handle event failed, will retry", "err", err, "delivery", delivered)
			metrics.EventsHandled.WithLabelValues(m.Reason(), "retry").Inc()
			_ = msg.NakWithDelay(30 * time.Second)
		}
	})
	if err != nil {
		return err
	}
	go b.watch(ctx, cons, events.ConsumerNotify)
	<-ctx.Done()
	cc.Stop()
	return nil
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
			Consumer: m.Header.Get(events.HeaderConsumer), Deliveries: m.Header.Get(events.HeaderDeliveries),
			Error: m.Header.Get(events.HeaderError), Data: string(m.Data)})
	}
	return out, nil
}

// DropDeadLetterStream deletes RYFT_DLQ. Only for rolling back to a release from
// before it, whose scoring sets RYFT back to ryft.>, which would overlap it
// (docs/OPERATIONS.md → Deploy and roll back).
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

// Key is the event key of a RYFT subject (a dead letter's, for re-driving it).
func Key(subject string) string { return key(subject) }
