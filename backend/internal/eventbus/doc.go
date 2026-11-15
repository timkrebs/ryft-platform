// Package eventbus owns building a service's Pub/Sub transport from its configuration: the Bus, the push verifier, the push and tick handlers and the wait sender.
//
// It must not be imported outside cmd/* and internal/app, which build the
// transport for a service: the rest of the code takes the Bus or the handlers it
// is given. Pub/Sub is the only transport since RabbitMQ was removed, so Open has no
// backend switch (D218). It is the one importer of internal/events/pubsub besides
// cmd/pubsub-topology, so a service needs nothing of the transport but this.
//
// See D218, D219.
package eventbus
