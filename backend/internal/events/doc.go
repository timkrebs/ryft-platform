// Package events owns the five trigger events the services exchange, their
// encoding, and what a handler's error means (Permanent, Defer, Redelivered).
//
// It must not know a transport or a table: it imports no amqpbus, store or db,
// and the outbox and the broker client both meet it through Sender and Bus.
// ingested and session.completed go to scoring; scored, session.scored and
// refresh go to notify.
//
// Invariants:
//   - Events are triggers, not records: Postgres is the source of truth and
//     POST /v1/recompute replays what was lost.
//   - Each event has exactly one consumer (D71).
//   - A handler is idempotent, because delivery is at least once and nothing
//     dedups a repeat (D100): the same message twice must change nothing.
//
// See docs/ARCHITECTURE.md → Events; docs/MESSAGING_DESIGN.md; D71, D97, D100, D101.
package events
