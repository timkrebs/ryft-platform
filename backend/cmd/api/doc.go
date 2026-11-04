// api serves the app-facing /v1 API (scores, workouts, settings, Train, journal, consent, push, account deletion) and the operator's /v1/admin.
//
// It does not accept HealthKit uploads (ingest owns POST /v1/sync) and does not
// write the day scores: it queues days by publishing ingested (and
// session.completed) and scoring consumes them. What it does compute and store
// (a completed session's load, personal records, the estimated max heart rate)
// uses scoring's own functions. The session load matches what scoring stores
// for the day; scoring never stores the records or max_hr, so the api is their
// only writer. Events go out only through the outbox, never to the broker
// directly (D101).
//
// Invariants:
//   - api is the only service that builds the Supabase admin client and the only
//     importer of sealed, so the Supabase secret key and DATA_ENCRYPTION_KEY
//     exist only in its namespace.
//   - Account deletion erases the rows in one transaction under the user's lock
//     (D39), then revokes the Sign in with Apple grant, then deletes the Supabase
//     user. Each later step is retried by the reconciler until it succeeds.
//     DELETE /v1/me skips the user lookup and the invite gate, so a retry reaches
//     the handler.
//   - The invite gate lets through only the routes docs/API.md names (D87).
//   - Every authorized /v1/admin call writes an admin_audit row (D88).
//   - One recompute per user per minute across replicas, because each can queue
//     hundreds of days on the single scoring consumer (D31, D17). The training import
//     has its own budget and runs one per user at a time (D129).
//
// See docs/ARCHITECTURE.md → Services (Account deletion, Request path); docs/API.md; D31,
// D39, D78, D87, D88, D101, D129.
package main
