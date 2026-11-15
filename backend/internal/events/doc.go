// Package events owns the five trigger events the services exchange, their
// encoding, and what a handler's error means.
//
// That is Permanent, Defer and Bad; the Redelivered flag on a handler's context is
// how a transport says a message came before (Pub/Sub's redelivery or a dead letter
// replayed with scripts/dlq.sh). A Handler is what a push transport calls with an
// event's key and data; ScoringHandler and NotifyHandler decode the data and mark
// what does not decode as Bad, which the transport dead-letters at once instead of
// retrying.
//
// It must not know a transport or a table: it imports no events/pubsub, store or
// db, and the outbox and the Pub/Sub client both meet it through Sender and Bus.
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
// See docs/ARCHITECTURE.md → Events; docs/MESSAGING_DESIGN.md; D71, D100, D101, D218.
package events
