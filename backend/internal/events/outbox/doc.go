// Package outbox owns the outbox table's rows and the relay that moves them to the
// broker, so an event is never lost between a database write and a publish.
//
// It must not import a transport (pubsub, eventbus): the relay sends through
// events.Sender, and the service wires the transport in.
//
// How a row moves (Cloud Run gives CPU only while a request runs, D219):
//   - Inline: a request that wrote rows calls Flush after its commit and before it
//     answers (Send is Insert plus Flush). Flush has a 2 s budget and is detached
//     from the request's cancel, so a client hanging up does not stop it.
//   - The tick: Sweep relays whatever is still due (rows of an instance that died,
//     failed publishes, deferrals that came due), sets the gauges and deletes
//     published rows older than Keep. Run loops Sweep, for tests and local tools.
//   - Not before: available_at (default now()) is when a row becomes due. InsertAt
//     and SendAt set it; notify defers a push past quiet hours that way. Only due rows
//     are drained, and only they count in ryft_outbox_pending and
//     ryft_outbox_oldest_seconds.
//
// Invariants:
//   - Insert and InsertAt write through the caller's Querier, so with a transaction
//     the event commits or rolls back together with the write it announces (D101).
//   - A publisher's rows reach the transport in id order, so one user's do too, even
//     with concurrent Flushes in several instances: each batch is a transaction that
//     holds the publisher's relay lock (transaction-level, as Supabase's transaction
//     pooler requires). A drain stops at the first failed send; a row that failed
//     MaxAttempts sweeps (a Flush's failure counts no attempt), or that the transport
//     rejected for good (events.Rejected), is set aside and alerted instead of holding
//     up the rows after it. Id order is that of committed rows: a row whose
//     transaction commits after a later id was relayed goes after it. One user's syncs
//     cannot do that (the store's apply lock serialises them), and the handlers are
//     idempotent anyway.
//   - A crash, or a budget that ends, between a send and its mark sends the row again.
//     That is allowed: delivery is at least once and the handlers are idempotent (D100).
//
// See docs/OBSERVABILITY.md (outbox metrics); D100, D101, D219.
package outbox
