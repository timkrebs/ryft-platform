// Package eventbus opens the brokers a service uses and joins them into one
// events.Bus (D102): EVENTS_BACKEND is the broker it publishes to, EVENTS_CONSUME the
// brokers it consumes from. During the cutover a consumer reads both, so nothing
// published to either is missed; the overlap is safe because the handlers are
// idempotent (D100).
package eventbus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/amqpbus"
	"github.com/timkrebs/ryft/internal/events/natsbus"
)

// Role is what a service does with events.
type Role struct {
	Name      string // the service: its NATS client name, its AMQP connection name
	Publishes bool
	Consumes  bool
	OwnStream bool // NATS: scoring creates and owns the streams
}

// Plan is which brokers to open, which one publishes, which ones consume.
type Plan struct {
	Open    []string
	Publish string
	Consume []string
}

// PlanFor decides from the configuration; a service that neither publishes nor
// consumes still opens its backend (for readiness).
func PlanFor(cfg config.Config, r Role) Plan {
	need := map[string]bool{}
	p := Plan{Publish: cfg.EventsBackend}
	if r.Publishes || !r.Consumes {
		need[cfg.EventsBackend] = true
	}
	if r.Consumes {
		p.Consume = cfg.EventsConsume
		for _, b := range cfg.EventsConsume {
			need[b] = true
		}
	}
	for _, b := range []string{config.BrokerNATS, config.BrokerAMQP} {
		if need[b] {
			p.Open = append(p.Open, b)
		}
	}
	if !need[p.Publish] {
		p.Publish = p.Open[0] // a consumer that publishes nothing
	}
	return p
}

// Open opens what PlanFor decides, each broker with its own startup retries.
func Open(ctx context.Context, cfg config.Config, r Role, log *slog.Logger) (events.Bus, error) {
	p := PlanFor(cfg, r)
	buses := map[string]events.Bus{}
	closeAll := func() {
		for _, b := range buses {
			b.Close()
		}
	}
	for _, name := range p.Open {
		var bus events.Bus
		var err error
		switch name {
		case config.BrokerNATS:
			bus, err = natsbus.Open(ctx, cfg.NatsURL, natsbus.Options{Name: r.Name, User: cfg.NatsUser, Password: cfg.NatsPass,
				OwnStream: r.OwnStream}, log)
		case config.BrokerAMQP:
			bus, err = amqpbus.Open(ctx, amqpbus.Options{URL: cfg.AMQP.URL, User: cfg.AMQP.User, Password: cfg.AMQP.Password,
				CAFile: cfg.AMQP.CAFile, CertFile: cfg.AMQP.CertFile, KeyFile: cfg.AMQP.KeyFile, Name: r.Name}, log)
		}
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		buses[name] = bus
	}
	log.Info("events", "publish", p.Publish, "consume", p.Consume, "open", p.Open)
	if len(buses) == 1 {
		return buses[p.Open[0]], nil
	}
	consumers := make([]events.Bus, 0, len(p.Consume))
	for _, name := range p.Consume {
		consumers = append(consumers, buses[name])
	}
	return events.Combine(buses[p.Publish], consumers...), nil
}
