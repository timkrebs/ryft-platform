// Package eventbus owns opening a service's events.Bus from its configuration.
//
// It must not be imported outside cmd/* and internal/app, which opens the bus for
// a service: the rest of the code takes the Bus it is given. RabbitMQ is the only transport since the cutover from NATS, so Open
// has no backend switch (D97, D102).
//
// See D97, D102.
package eventbus
