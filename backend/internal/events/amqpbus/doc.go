// Package amqpbus owns events.Bus on RabbitMQ (AMQP 0-9-1) and the broker's
// topology as Go, from which the manifests and the dev definitions are generated.
//
// It must not requeue a handler's failure, and only eventbus and
// cmd/rabbitmq-topology import it: the services see events.Bus.
//
// Invariants:
//   - Delivery is at least once. A publish waits for the broker's confirm and is
//     mandatory (an unroutable event is ErrUnroutable, not a silent drop), so a
//     caller can see a failure and the outbox resends. A consumer acks by hand, so
//     it can see a message again after a crash; every handler must be idempotent
//     (D100).
//   - A failed handling is acked and copied into a delay tier (5 s, 30 s, then
//     5 min) that dead-letters it back to its own queue; after MaxAttempts the
//     copy goes to dlq.<queue> with the Ryft-* headers, kept 14 days (scripts/dlq.sh
//     lists and replays them). A Permanent error is dropped and a message that
//     does not decode is dead-lettered at once. Only when even the copy cannot be
//     published is the delivery rejected WITH requeue; after DeliveryLimit (10)
//     redeliveries the broker dead-letters it itself, and those letters carry
//     x-death, not the Ryft-* headers.
//   - The connection is kept up with backoff and jitter while the service runs;
//     Ready reports why it is down, and the outbox keeps events meanwhile (D101).
//
// See docs/MESSAGING_DESIGN.md; docs/OPERATIONS.md → RabbitMQ; D47, D97, D99, D100, D101, D102.
package amqpbus
