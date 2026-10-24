// Package amqpbus is events.Bus on RabbitMQ (AMQP 0-9-1, github.com/rabbitmq/amqp091-go;
// D97, D99). One connection per service, reconnected with backoff and jitter while the
// service keeps running (/readyz says why, D47); publishes wait for the broker's
// confirm and are mandatory (an unroutable event is an error, not a silent drop);
// consumers acknowledge by hand, and a failure is a copy into a delay tier (TTL queue,
// then back to the queue) instead of a requeue, so retries are spaced and counted.
package amqpbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/retry"
)

// HeaderAttempt counts a message's failed handlings (0 or absent: the first try).
const HeaderAttempt = "x-ryft-attempt"

// HeaderReplayed marks a dead letter that scripts/dlq.sh put back on ryft.events. Its
// handler takes it as a redelivery (events.Redelivered): the attempts before the dead
// letter may have done part of the work. Only the boolean true counts; it is apart from
// HeaderAttempt, so a replayed letter gets all its attempts again, and every copy into a
// delay tier or the dead-letter queue keeps it.
const HeaderReplayed = "x-ryft-replayed"

// ErrUnroutable: the broker had no queue for the event's routing key (a topology
// that lacks a binding), so nothing would ever handle it.
var ErrUnroutable = errors.New("amqp: unroutable (no queue bound for this key)")

// ErrDisconnected: no connection to the broker right now.
var ErrDisconnected = errors.New("amqp: not connected")

// Options for Open.
type Options struct {
	URL      string // amqps://rabbitmq.ryft-rabbitmq.svc.cluster.local:5671/ryft
	User     string // AMQP_USER; empty: whatever the URL carries
	Password string
	CAFile   string // PEM bundle the broker's certificate must chain to (amqps); empty: the system pool
	// CertFile and KeyFile are the service's client certificate (the broker verifies
	// peers, D98): read at every handshake, so a renewed certificate is used on the
	// next connection without a restart.
	CertFile, KeyFile string
	Name              string // connection name in the management UI
	Prefetch          int    // unacknowledged deliveries per consumer (default 10)

	ConfirmTimeout time.Duration // how long a publish waits for the broker's confirm (default 10 s)
}

// Bus is events.Bus on RabbitMQ.
type Bus struct {
	events.Publisher
	o      Options
	log    *slog.Logger
	cfg    amqp.Config
	url    string
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	conn      *amqp.Connection
	up        chan struct{} // closed while connected
	pub       *publisher
	lastErr   error
	downSince time.Time
	pubMu     sync.Mutex // one confirmed publish at a time on the publish channel
}

type publisher struct {
	ch      *amqp.Channel
	returns chan amqp.Return
	closed  chan *amqp.Error
}

// Open dials the broker, retrying with backoff for up to five minutes (its pods
// starting, the user not created yet), then keeps the connection up in the
// background until Close.
func Open(ctx context.Context, o Options, log *slog.Logger) (*Bus, error) {
	b, err := New(o, log)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := retry.Until(wait, log, "amqp", retry.Startup, func(ctx context.Context) error { return b.connect() }); err != nil {
		b.Close()
		return nil, err
	}
	go b.supervise()
	return b, nil
}

// New is a Bus that has not dialled yet (Open dials).
func New(o Options, log *slog.Logger) (*Bus, error) {
	metrics.UseAMQP()
	if o.Prefetch <= 0 {
		o.Prefetch = 10
	}
	if o.ConfirmTimeout <= 0 {
		o.ConfirmTimeout = 10 * time.Second
	}
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") {
		return nil, fmt.Errorf("AMQP_URL must be amqp:// or amqps://, got %q", Redacted(o.URL))
	}
	if u.Opaque != "" || u.Host == "" { // amqp:user:pw@host has no User to redact
		return nil, errors.New("AMQP_URL must be amqp://host or amqps://host")
	}
	cfg := amqp.Config{
		Heartbeat:  10 * time.Second,
		Locale:     "en_US",
		Properties: amqp.Table{"connection_name": o.Name, "product": "ryft"},
		Dial:       amqp.DefaultDial(10 * time.Second),
	}
	if o.User != "" {
		cfg.SASL = []amqp.Authentication{&amqp.PlainAuth{Username: o.User, Password: o.Password}}
		u.User = nil
	}
	if u.Scheme == "amqps" {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostOf(u.Host)}
		if o.CAFile != "" {
			pem, err := os.ReadFile(o.CAFile)
			if err != nil {
				return nil, fmt.Errorf("AMQP_CA_FILE: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("AMQP_CA_FILE %s holds no PEM certificate", o.CAFile)
			}
			tc.RootCAs = pool
		}
		if o.CertFile != "" || o.KeyFile != "" {
			if _, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile); err != nil {
				return nil, fmt.Errorf("AMQP_CERT_FILE/AMQP_KEY_FILE: %w", err)
			}
			certFile, keyFile := o.CertFile, o.KeyFile
			tc.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				c, err := tls.LoadX509KeyPair(certFile, keyFile)
				return &c, err
			}
		}
		cfg.TLSClientConfig = tc
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bus{o: o, log: log, cfg: cfg, url: u.String(), ctx: ctx, cancel: cancel, up: make(chan struct{}), downSince: time.Now()}
	b.Publisher = events.Publisher{Sender: b}
	return b, nil
}

func hostOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// connect dials once and opens the publish channel.
func (b *Bus) connect() error {
	conn, err := amqp.DialConfig(b.url, b.cfg)
	if err != nil {
		b.setDown(err)
		return fmt.Errorf("amqp dial: %w", err)
	}
	pub, err := openPublisher(conn)
	if err != nil {
		_ = conn.Close()
		b.setDown(err)
		return err
	}
	b.mu.Lock()
	b.conn, b.pub, b.lastErr = conn, pub, nil
	close(b.up)
	b.mu.Unlock()
	metrics.AMQPConnected.Set(1)
	b.log.Info("amqp connected", "server", conn.RemoteAddr().String())
	return nil
}

func openPublisher(conn *amqp.Connection) (*publisher, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("amqp channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("amqp confirm mode: %w", err)
	}
	return &publisher{ch: ch, returns: ch.NotifyReturn(make(chan amqp.Return, 16)), closed: ch.NotifyClose(make(chan *amqp.Error, 1))}, nil
}

func (b *Bus) setDown(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.up: // was up: now down
		b.up = make(chan struct{})
		b.downSince = time.Now()
	default:
	}
	b.conn, b.pub, b.lastErr = nil, nil, err
	metrics.AMQPConnected.Set(0)
}

// supervise reconnects after the connection closes, with backoff and jitter, until
// Close.
func (b *Bus) supervise() {
	for {
		b.mu.Lock()
		conn := b.conn
		b.mu.Unlock()
		if conn != nil {
			closed := conn.NotifyClose(make(chan *amqp.Error, 1))
			select {
			case <-b.ctx.Done():
				return
			case e := <-closed:
				err := ErrDisconnected
				if e != nil {
					err = e
				}
				b.log.Warn("amqp connection closed, reconnecting", "err", err)
				metrics.AMQPReconnects.Inc()
				b.setDown(err)
			}
		}
		for delay := time.Second; ; delay = min(delay*2, 30*time.Second) {
			if err := b.connect(); err == nil {
				break
			} else {
				b.log.Warn("amqp reconnect failed", "err", err, "retry_in", delay.String())
			}
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(jitter(delay)):
			}
		}
	}
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // spread, not secrecy
}

// Ready is nil while connected, else why not and since when.
func (b *Bus) Ready(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil && !b.conn.IsClosed() {
		return nil
	}
	down := time.Since(b.downSince).Round(time.Second)
	if b.lastErr == nil {
		return fmt.Errorf("amqp not connected for %s", down)
	}
	return fmt.Errorf("amqp not connected for %s: %w", down, b.lastErr)
}

// Close stops reconnecting and closes the connection.
func (b *Bus) Close() {
	b.cancel()
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// waitConn blocks until connected or ctx ends.
func (b *Bus) waitConn(ctx context.Context) (*amqp.Connection, error) {
	for {
		b.mu.Lock()
		conn, up := b.conn, b.up
		b.mu.Unlock()
		if conn != nil && !conn.IsClosed() {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.ctx.Done():
			return nil, ErrDisconnected
		case <-up:
		case <-time.After(time.Second):
		}
	}
}

// Send publishes m on ryft.events with its key (events.Sender).
func (b *Bus) Send(ctx context.Context, m events.Message) error {
	id := m.ID
	if id == "" {
		id = uuid.NewString()
	}
	err := b.publish(ctx, ExchangeEvents, m.Key, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: id, Type: m.Key,
		Timestamp: time.Now().UTC(), Body: m.Body,
	})
	result := metrics.Result(err)
	if errors.Is(err, ErrUnroutable) {
		result = "unroutable"
		b.log.Error("event unroutable: no queue bound for its key", "key", m.Key)
	}
	metrics.EventsPublished.WithLabelValues(m.Reason, result).Inc()
	return err
}

// publish sends one message and waits for the broker's confirm. It fails for a nack,
// a return (mandatory: no queue bound), a closed channel, or the confirm timeout.
func (b *Bus) publish(ctx context.Context, exchange, key string, msg amqp.Publishing) error {
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	b.mu.Lock()
	pub, conn := b.pub, b.conn
	b.mu.Unlock()
	if conn == nil || conn.IsClosed() {
		return ErrDisconnected
	}
	if pub == nil || pub.ch.IsClosed() {
		// The broker closes a channel on an error (e.g. a refused publish): a new one.
		p, err := openPublisher(conn)
		if err != nil {
			return err
		}
		b.mu.Lock()
		b.pub = p
		b.mu.Unlock()
		pub = p
	}
	msg.CorrelationId = uuid.NewString()
	ctx, cancel := context.WithTimeout(ctx, b.o.ConfirmTimeout)
	defer cancel()
	dc, err := pub.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, msg)
	if err != nil {
		return fmt.Errorf("amqp publish: %w", err)
	}
	acked, err := dc.WaitContext(ctx)
	if err != nil {
		// No confirm in time: the channel's state is unknown, start a new one.
		_ = pub.ch.Close()
		return fmt.Errorf("amqp confirm: %w", err)
	}
	// The broker returns an unroutable message before it confirms it.
drain:
	for {
		select {
		case r := <-pub.returns:
			if r.CorrelationId == msg.CorrelationId {
				return fmt.Errorf("%w: exchange %s, key %s", ErrUnroutable, exchange, key)
			}
		case e := <-pub.closed:
			if e == nil {
				return ErrDisconnected
			}
			return fmt.Errorf("amqp channel closed: %w", e)
		default:
			break drain
		}
	}
	if !acked {
		return fmt.Errorf("amqp: the broker refused %s/%s (nack: a full queue, or a node failing)", exchange, key)
	}
	return nil
}

// ConsumeIngested blocks, handing scoring every message of its queue.
func (b *Bus) ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error {
	return b.consume(ctx, log, events.ConsumerScoring, func(ctx context.Context, d amqp.Delivery, reason *string) error {
		ev, err := events.DecodeIngested(keyOf(d), d.Body)
		if err != nil {
			return badEvent{err}
		}
		*reason = ev.Reason
		return fn(ctx, ev)
	})
}

// ConsumeNotify blocks, handing notify every message of its queue. events.Defer(d)
// parks the message in the longest wait tier that fits d.
func (b *Bus) ConsumeNotify(ctx context.Context, log *slog.Logger, fn func(context.Context, events.NotifyMessage) error) error {
	return b.consume(ctx, log, events.ConsumerNotify, func(ctx context.Context, d amqp.Delivery, reason *string) error {
		m, err := events.DecodeNotify(keyOf(d), d.Body)
		if err != nil {
			return badEvent{err}
		}
		*reason = m.Reason()
		return fn(ctx, m)
	})
}

// keyOf is the event key a delivery is handled as. From ryft.events it is the routing
// key: that is what the publisher's topic permission limits (D99). A retry tier's copy
// comes back through ryft.requeue with the queue's name as routing key and keeps the
// type its first delivery was checked with (typeMatches), so there it is the type.
func keyOf(d amqp.Delivery) string {
	if d.Exchange == ExchangeEvents || d.Type == "" {
		return d.RoutingKey
	}
	return d.Type
}

// typeMatches: a delivery from ryft.events carries no type or its routing key. A
// publisher sets the type freely, so ingest, which may publish only `ingested`, must not
// get a session handled by calling its message session.completed: such a delivery is a
// bad event, dead-lettered under its routing key.
func typeMatches(d amqp.Delivery) bool {
	return d.Exchange != ExchangeEvents || d.Type == "" || d.Type == d.RoutingKey
}

// handler decodes and handles a delivery, setting the metric reason once decoded; a
// delivery that does not decode is a badEvent.
type handler func(ctx context.Context, d amqp.Delivery, reason *string) error

// badEvent is a delivery that does not decode: dead-lettered at once.
type badEvent struct{ err error }

func (e badEvent) Error() string { return e.err.Error() }
func (e badEvent) Unwrap() error { return e.err }

// consume subscribes to queue and handles its deliveries until ctx ends,
// resubscribing after the channel or the connection closes (a broker restart, the
// queue not created yet).
func (b *Bus) consume(ctx context.Context, log *slog.Logger, queue string, h handler) error {
	for delay := time.Second; ; {
		conn, err := b.waitConn(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // shutting down is not a failure
			}
			return err
		}
		start := time.Now()
		err = b.subscribe(ctx, log, conn, queue, h)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down is not a failure
		}
		if time.Since(start) > time.Minute {
			delay = time.Second // it ran a while: this is a new outage, not the same one
		}
		log.Warn("amqp consumer stopped, resubscribing", "queue", queue, "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter(delay)):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (b *Bus) subscribe(ctx context.Context, log *slog.Logger, conn *amqp.Connection, queue string, h handler) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()
	if err := ch.Qos(b.o.Prefetch, 0, false); err != nil {
		return err
	}
	tag := fmt.Sprintf("%s-%s", b.o.Name, uuid.NewString()[:8])
	deliveries, err := ch.Consume(queue, tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	log.Info("amqp consuming", "queue", queue, "prefetch", b.o.Prefetch)
	for {
		select {
		case <-ctx.Done():
			_ = ch.Cancel(tag, false)
			return nil
		case e := <-closed:
			if e == nil {
				return ErrDisconnected
			}
			return e
		case d, ok := <-deliveries:
			if !ok {
				return ErrDisconnected
			}
			b.handle(ctx, log, queue, d, h)
		}
	}
}

// handle runs the handler for one delivery and settles it: ack; a copy into a delay
// tier and ack; a copy into the dead-letter queue and ack; or, when even that publish
// fails, a reject with requeue, which the broker counts against the queue's delivery
// limit (and then dead-letters itself). A retry tier's copy, the broker's redelivery and
// a replayed dead letter (HeaderReplayed) reach the handler as a redelivery.
func (b *Bus) handle(ctx context.Context, log *slog.Logger, queue string, d amqp.Delivery, h handler) {
	attempt := attemptOf(d)
	replayed, malformed := replayOf(d)
	if malformed {
		log.Warn("amqp: ignoring "+HeaderReplayed+", it is not the boolean true", "queue", queue, "key", keyOf(d),
			"type", fmt.Sprintf("%T", d.Headers[HeaderReplayed]))
	}
	if attempt > 0 || d.Redelivered || replayed {
		metrics.Redeliveries.WithLabelValues(queue).Inc()
		ctx = events.WithRedelivery(ctx)
	}
	reason := "unknown"
	var err error
	if typeMatches(d) {
		err = events.Guarded(log, keyOf(d), func() error { return h(ctx, d, &reason) })
	} else {
		err = badEvent{fmt.Errorf("its type %.64q is not its routing key %q", d.Type, d.RoutingKey)}
	}
	var bad badEvent
	settle := func(what string, perr error) {
		if perr != nil {
			log.Error("amqp: could not "+what+", rejecting for redelivery", "err", perr, "queue", queue, "key", keyOf(d))
			_ = d.Reject(true)
			return
		}
		_ = d.Ack(false)
	}
	after, deferred := events.Deferred(err)
	switch {
	case errors.As(err, &bad):
		log.Error("bad event, dead-lettering", "err", err, "queue", queue, "key", keyOf(d))
		metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
		settle("dead-letter", b.deadLetter(ctx, log, queue, d, attempt, err))
	case err == nil:
		metrics.EventsHandled.WithLabelValues(reason, "ok").Inc()
		_ = d.Ack(false)
	case deferred:
		metrics.EventsHandled.WithLabelValues(reason, "deferred").Inc()
		settle("defer", b.delay(ctx, d, RetryQueue(queue, waitTierFor(queue, after)), attempt))
	case events.IsPermanent(err):
		log.Warn("event cannot be handled, dropping", "err", err, "queue", queue)
		metrics.EventsHandled.WithLabelValues(reason, "dropped").Inc()
		_ = d.Ack(false)
	case attempt+1 >= MaxAttempts:
		log.Error("handle event failed on its last attempt, dead-lettering", "err", err, "queue", queue, "attempt", attempt+1)
		metrics.EventsHandled.WithLabelValues(reason, "failed").Inc()
		settle("dead-letter", b.deadLetter(ctx, log, queue, d, attempt+1, err))
	default:
		tier := tierFor(attempt + 1)
		log.Error("handle event failed, will retry", "err", err, "queue", queue, "attempt", attempt+1, "retry_in", tier.Delay.String())
		metrics.EventsHandled.WithLabelValues(reason, "retry").Inc()
		settle("retry", b.delay(ctx, d, RetryQueue(queue, tier), attempt+1))
	}
}

// delay publishes a copy of d into the delay queue q (bound to ryft.retry under its
// own name), with attempt as its count.
func (b *Bus) delay(ctx context.Context, d amqp.Delivery, q string, attempt int) error {
	h := amqp.Table{}
	for k, v := range d.Headers {
		h[k] = v
	}
	h[HeaderAttempt] = int64(attempt)
	return b.publish(ctx, ExchangeRetry, q, copyOf(d, h))
}

// deadLetter publishes a copy of d into consumer queue's dead-letter queue with the
// reason in its headers (Ryft-Subject, Ryft-Consumer, Ryft-Deliveries, Ryft-Error).
func (b *Bus) deadLetter(ctx context.Context, log *slog.Logger, queue string, d amqp.Delivery, attempts int, cause error) error {
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	h := amqp.Table{}
	for k, v := range d.Headers {
		h[k] = v
	}
	subject := subjectOf(queue, d)
	h[events.HeaderSubject] = subject
	h[events.HeaderConsumer] = queue
	h[events.HeaderDeliveries] = strconv.Itoa(attempts)
	h[events.HeaderError] = reason
	err := b.publish(ctx, ExchangeDLX, queue, copyOf(d, h))
	metrics.EventsDLQ.WithLabelValues(subject, metrics.Result(err)).Inc()
	if err == nil {
		log.Warn("event dead-lettered", "queue", queue, "dlq", DLQ(queue), "key", subject, "attempts", attempts, "err", cause)
	}
	return err
}

// subjectOf is the key a dead letter is filed under (Ryft-Subject, the key scripts/dlq.sh
// replays it with, and the DLQ metric's label): its event key (keyOf) when the type
// matches (typeMatches) and queue's consumer is bound for that key, else its routing
// key. A publisher may write only its own routing keys (topic permissions, D99) but sets
// the type freely, so a type that does not match is not trusted: it goes into neither
// the DLQ's subject nor a metric label.
func subjectOf(queue string, d amqp.Delivery) string {
	if key := keyOf(d); typeMatches(d) && slices.Contains(consumerKeys[queue], key) {
		return key
	}
	return d.RoutingKey
}

// copyOf keeps d's type (a dead letter keeps a forged one as evidence; dlq.sh does not
// replay a letter whose type is not its key) and gives an untyped delivery its key.
func copyOf(d amqp.Delivery, h amqp.Table) amqp.Publishing {
	typ := d.Type
	if typ == "" {
		typ = keyOf(d)
	}
	return amqp.Publishing{Headers: h, ContentType: d.ContentType, DeliveryMode: amqp.Persistent, MessageId: d.MessageId,
		Type: typ, Timestamp: d.Timestamp, Body: d.Body}
}

// replayOf reads HeaderReplayed strictly: replayed only for the boolean true under exactly
// that name. Any other value there (a string typed into the management UI, a number,
// false) is malformed and does not count.
func replayOf(d amqp.Delivery) (replayed, malformed bool) {
	v, ok := d.Headers[HeaderReplayed]
	if !ok {
		return false, false
	}
	b, isBool := v.(bool)
	replayed = isBool && b
	return replayed, !replayed
}

// attemptOf reads HeaderAttempt, whatever integer type it arrived as.
func attemptOf(d amqp.Delivery) int {
	switch v := d.Headers[HeaderAttempt].(type) {
	case int64:
		return int(v)
	case int32:
		return int(v)
	case int16:
		return int(v)
	case int8:
		return int(v)
	case int:
		return v
	case uint8:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
