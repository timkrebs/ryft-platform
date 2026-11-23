package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
)

// MaxPushBody bounds a push request. The events are a few kilobytes; a bigger body is
// answered 413 (a nack), and Pub/Sub dead-letters it after its attempts.
const MaxPushBody = 1 << 20

// What an envelope may hold that is logged or stored. The envelope is whoever pushed
// it: Pub/Sub's own values are far shorter, and one over these bounds is cut (or, a
// messageId, refused) before it reaches a log line or the outbox.
const (
	// maxLogValue bounds a value from the envelope in a log line (the subscription).
	maxLogValue = 256
	// maxLogKey bounds a ryft-key that is not a key, in a log line.
	maxLogKey = 64
	// maxMessageID bounds the ryft-message-id a deferral keeps in the outbox (ours are
	// under 100 bytes; Pub/Sub refuses an attribute value over 1024).
	maxMessageID = 256
)

// pubsubMessageID is Pub/Sub's messageId: decimal digits. An envelope without one is
// not a push envelope (400).
var pubsubMessageID = regexp.MustCompile(`^[0-9]{1,32}$`)

// Deferrer stores a message to be published again at a later time: notify's outbox
// (SendAt writes a row with that not-before time, which a tick relays into
// TopicNotifyWait).
type Deferrer interface {
	SendAt(ctx context.Context, m events.Message, at time.Time) error
}

// Publisher publishes one message to a topic: the Client (the push handlers
// dead-letter with it).
type Publisher interface {
	Publish(ctx context.Context, topic string, m Message) (string, error)
}

// PushOptions configure a consumer's push handling.
type PushOptions struct {
	Consumer string            // events.ConsumerScoring, events.ConsumerNotify or events.ConsumerIngest
	Verifier auth.PushVerifier // the push token check (auth.GoogleOIDC; auth.InsecurePush locally)
	DLQ      Publisher         // publishes the consumer's bad events to its dead-letter topic
	Deferrer Deferrer          // stores a Defer; nil: a Defer is a nack
	Log      *slog.Logger
	Now      func() time.Time // default time.Now (tests)
}

// Push is one consumer's push handling: a handler per push subscription path of the
// consumer's service in the topology.
type Push struct {
	o     PushOptions
	dlq   string
	keys  []string
	paths []string
	fn    events.Handler
}

// NewPush is consumer's push handling with fn as the event handler. It needs a verifier
// and a dead-letter publisher: there is no unchecked or undead-lettered push.
func NewPush(o PushOptions, fn events.Handler) (*Push, error) {
	dlq, ok := DLQTopic(o.Consumer)
	switch {
	case !ok:
		return nil, fmt.Errorf("pubsub: %q is not a consumer", o.Consumer)
	case o.Verifier == nil:
		return nil, errors.New("pubsub: a push handler needs a verifier")
	case o.DLQ == nil:
		return nil, errors.New("pubsub: a push handler needs a dead-letter publisher")
	case fn == nil || o.Log == nil:
		return nil, errors.New("pubsub: a push handler needs a handler and a logger")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Push{o: o, dlq: dlq, keys: consumerKeys(o.Consumer), paths: PushPaths(o.Consumer), fn: fn}, nil
}

// Paths are the routes Register serves: scoring's /push/ingested and
// /push/session.completed; notify's /push/scored, /push/session.scored, /push/refresh
// and /push/wait; ingest's /push/inbox.received.
func (p *Push) Paths() []string { return slices.Clone(p.paths) }

// Register serves "POST <path>" on mux for every path of Paths.
func (p *Push) Register(mux *http.ServeMux) {
	for _, path := range p.paths {
		mux.Handle(http.MethodPost+" "+path, p.Handler(path))
	}
}

// Handler is the handler of one push path. Every answer settles the message:
//
//	204  ack: handled; Permanent (dropped); a bad event dead-lettered; a Defer stored
//	500  nack: the handler failed or panicked; the dead letter or the deferral could not be written
//	401  nack: a missing or bad token (503: the token cannot be checked right now)
//	400, 405, 413  nack: not a push envelope (or no decimal messageId), not a POST, too big
//
// The key rule: on /push/<key> the ryft-key attribute must be absent or <key>; on
// /push/wait it must be present and one of the consumer's keys. Otherwise the message is
// a bad event, dead-lettered under the path's key (on /push/wait under none).
func (p *Push) Handler(path string) http.Handler {
	pathKey, keyed := KeyOfPath(path)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !verified(w, r, p.o.Verifier, p.o.Log, path) {
			return
		}
		env, status := readEnvelope(w, r)
		if status != 0 {
			p.o.Log.WarnContext(r.Context(), "push: not a push envelope", "path", path, "status", status)
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(p.handle(r.Context(), path, pathKey, keyed, env))
	})
}

// handle runs the event handler for one delivery and returns the status that settles it.
func (p *Push) handle(ctx context.Context, path, pathKey string, keyed bool, env envelope) int {
	m := env.Message
	attempt := env.attempt()
	log := p.o.Log.With("consumer", p.o.Consumer, "subscription", clip(env.Subscription, maxLogValue), "message_id", m.MessageID, "attempt", attempt)

	key, keyErr := p.keyOf(path, pathKey, keyed, m.Attributes)
	replayed := m.Attributes[AttrReplayed]
	if replayed != "" && replayed != "true" {
		log.WarnContext(ctx, "push: ignoring "+AttrReplayed+", it is not true", "key", key)
	}
	if attempt > 1 || replayed == "true" {
		metrics.Redeliveries.WithLabelValues(p.o.Consumer).Inc()
		ctx = events.WithRedelivery(ctx)
		if replayed == "true" {
			log.InfoContext(ctx, "push: handling a replayed dead letter", "key", key)
		}
	}
	if keyErr != nil {
		return p.deadLetter(ctx, log, key, m, events.Bad(keyErr))
	}

	ctx, reason := events.WithReason(ctx)
	err := events.Guarded(log, key, func() error { return p.fn(ctx, key, m.Data) })
	after, deferred := events.Deferred(err)
	switch {
	case err == nil:
		metrics.EventsHandled.WithLabelValues(reason(), "ok").Inc()
		return http.StatusNoContent
	case events.IsBad(err):
		return p.deadLetter(ctx, log, key, m, err)
	case deferred:
		return p.deferTo(ctx, log, key, reason(), m, after)
	case events.IsPermanent(err):
		log.WarnContext(ctx, "event cannot be handled, dropping", "key", key, "err", err)
		metrics.EventsHandled.WithLabelValues(reason(), "dropped").Inc()
		return http.StatusNoContent
	case attempt >= MaxDeliveryAttempts:
		log.ErrorContext(ctx, "handle event failed on its last attempt, Pub/Sub dead-letters it", "key", key, "err", err)
		metrics.EventsHandled.WithLabelValues(reason(), "failed").Inc()
		return http.StatusInternalServerError
	default:
		log.ErrorContext(ctx, "handle event failed, Pub/Sub delivers it again", "key", key, "err", err)
		metrics.EventsHandled.WithLabelValues(reason(), "retry").Inc()
		return http.StatusInternalServerError
	}
}

// keyOf is the key a delivery is handled and filed under, and why it may not be handled.
// On /push/<key> it is always the path's key: the subscription's path is what Terraform
// and the topic's publish rights fix, a publisher sets the attribute freely. On
// /push/wait (only notify publishes there) it is the attribute, if it is one of the
// consumer's keys; otherwise "" (filed under none).
func (p *Push) keyOf(path, pathKey string, keyed bool, attrs map[string]string) (string, error) {
	attr, present := attrs[AttrKey]
	switch {
	case keyed && (!present || attr == pathKey):
		return pathKey, nil
	case keyed:
		return pathKey, fmt.Errorf("its %s %.64q is not the key of %s", AttrKey, attr, path)
	case !present:
		return "", fmt.Errorf("no %s on %s", AttrKey, path)
	case !slices.Contains(p.keys, attr):
		return "", fmt.Errorf("its %s %.64q is not one of %s's keys", AttrKey, attr, p.o.Consumer)
	}
	return attr, nil
}

// deadLetter publishes the message to the consumer's dead-letter topic with ryft-error
// and acks it (204); if that publish fails it nacks (500), and Pub/Sub's own
// dead-letter policy takes it after its attempts.
func (p *Push) deadLetter(ctx context.Context, log *slog.Logger, key string, m pushedMessage, cause error) int {
	metrics.EventsHandled.WithLabelValues(events.ReasonUnknown, "bad").Inc()
	attrs := map[string]string{AttrError: clip(cause.Error(), MaxErrorBytes)}
	if key != "" {
		attrs[AttrKey] = key
	}
	for _, a := range []string{AttrMessageID, AttrReason} {
		if v, ok := m.Attributes[a]; ok && v != "" {
			attrs[a] = clip(v, MaxErrorBytes)
		}
	}
	subject := key
	if subject == "" {
		subject = events.ReasonUnknown
	}
	id, err := p.o.DLQ.Publish(ctx, p.dlq, Message{Data: m.Data, Attributes: attrs})
	metrics.EventsDLQ.WithLabelValues(subject, metrics.Result(err)).Inc()
	if err != nil {
		log.ErrorContext(ctx, "bad event, and the dead-letter publish failed: nacking", "key", subject, "dlq", p.dlq, "err", cause, "publish_err", err)
		return http.StatusInternalServerError
	}
	log.WarnContext(ctx, "bad event, dead-lettered", "key", subject, "dlq", p.dlq, "dlq_message_id", id, "err", cause)
	return http.StatusNoContent
}

// deferTo stores the message for after d (the Deferrer's not-before row) and acks it;
// without a Deferrer, or when the write fails, it nacks. The row keeps the
// ryft-message-id, cut to maxMessageID: one longer could never be published again.
func (p *Push) deferTo(ctx context.Context, log *slog.Logger, key, reason string, m pushedMessage, d time.Duration) int {
	if p.o.Deferrer == nil {
		log.WarnContext(ctx, "event deferred, but this consumer stores no deferrals: nacking", "key", key, "after", d.String())
		metrics.EventsHandled.WithLabelValues(reason, "retry").Inc()
		return http.StatusInternalServerError
	}
	at := p.o.Now().Add(d)
	msg := events.Message{Key: key, ID: clip(m.Attributes[AttrMessageID], maxMessageID), Body: m.Data, Reason: reason}
	if err := p.o.Deferrer.SendAt(ctx, msg, at); err != nil {
		log.ErrorContext(ctx, "event deferred, but storing the deferral failed: nacking", "key", key, "err", err)
		metrics.EventsHandled.WithLabelValues(reason, "retry").Inc()
		return http.StatusInternalServerError
	}
	metrics.EventsHandled.WithLabelValues(reason, "deferred").Inc()
	log.InfoContext(ctx, "event deferred", "key", key, "until", at.UTC().Format(time.RFC3339))
	return http.StatusNoContent
}

// envelope is Pub/Sub's wrapped push body.
type envelope struct {
	Message         pushedMessage `json:"message"`
	DeliveryAttempt *int          `json:"deliveryAttempt"`
	Subscription    string        `json:"subscription"`
}

type pushedMessage struct {
	Data        []byte            `json:"data"` // base64 on the wire; encoding/json decodes it
	Attributes  map[string]string `json:"attributes"`
	MessageID   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
	OrderingKey string            `json:"orderingKey"`
}

// attempt is the delivery attempt: 1 when Pub/Sub sent none (no dead-letter policy) or
// one below 1.
func (e envelope) attempt() int {
	if e.DeliveryAttempt == nil || *e.DeliveryAttempt < 1 {
		return 1
	}
	return *e.DeliveryAttempt
}

// readEnvelope reads and decodes the body; status is 0, or what to answer instead. An
// envelope whose messageId is not Pub/Sub's (decimal, at most 32 digits) is not one.
func readEnvelope(w http.ResponseWriter, r *http.Request) (envelope, int) {
	var env envelope
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPushBody))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return env, http.StatusRequestEntityTooLarge
	case err != nil:
		return env, http.StatusBadRequest
	}
	if json.Unmarshal(raw, &env) != nil || !pubsubMessageID.MatchString(env.Message.MessageID) {
		return env, http.StatusBadRequest
	}
	return env, 0
}

// verified checks the push or tick token and answers 401 or 503 when it fails. It
// logs the reason, never the Authorization header or the body.
func verified(w http.ResponseWriter, r *http.Request, v auth.PushVerifier, log *slog.Logger, path string) bool {
	err := v.VerifyPush(r)
	if err == nil {
		return true
	}
	var ae *auth.Error
	if !errors.As(err, &ae) {
		ae = auth.Unauthorized("invalid", err)
	}
	if ae.Status == http.StatusUnauthorized {
		log.InfoContext(r.Context(), "push rejected", "path", path, "reason", ae.Reason, "err", errText(ae.Err))
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	log.WarnContext(r.Context(), "push token cannot be checked", "path", path, "reason", ae.Reason, "err", errText(ae.Err))
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(ae.Status)
	return false
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
