// Package outbox owns the outbox table's rows and the relay that moves them to the
// broker, so an event is never lost between a database write and a publish.
//
// It must not import a transport (amqpbus, eventbus): the relay sends through
// events.Sender, and the service wires the broker in.
//
// Invariants:
//   - Insert writes through the caller's Querier, so with a transaction the event
//     commits or rolls back together with the write it announces (D101).
//   - Several replicas drain one publisher's rows without sending a row twice
//     (FOR UPDATE SKIP LOCKED); a row that fails MaxAttempts times is set aside
//     and alerted instead of holding up the rows after it.
//   - A crash between a send and its mark sends the row again. That is allowed:
//     delivery is at least once and the handlers are idempotent (D100).
//
// See docs/OBSERVABILITY.md (outbox metrics); D100, D101.
package outbox
