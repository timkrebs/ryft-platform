// Package eventbus opens a service's broker from its configuration: RabbitMQ (D97),
// the only one since the cutover (D102).
package eventbus

import (
	"context"
	"log/slog"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/amqpbus"
)

// Open connects as the service name (its connection name in the broker), retrying
// at start-up.
func Open(ctx context.Context, cfg config.Config, name string, log *slog.Logger) (events.Bus, error) {
	bus, err := amqpbus.Open(ctx, amqpbus.Options{URL: cfg.AMQP.URL, User: cfg.AMQP.User, Password: cfg.AMQP.Password,
		CAFile: cfg.AMQP.CAFile, CertFile: cfg.AMQP.CertFile, KeyFile: cfg.AMQP.KeyFile, Name: name}, log)
	if err != nil {
		return nil, err
	}
	log.Info("events", "broker", "amqp", "url", cfg.AMQP.URL)
	return bus, nil
}
