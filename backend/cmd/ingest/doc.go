// ingest serves POST /v1/sync, the HealthKit upload, and the one-off fold-workouts subcommand.
//
// It stores what the app sends and tells scoring which local days changed; it
// scores nothing. The only event it emits is ingested, and only through the
// outbox (D101).
//
// Invariants:
//   - Consent and the invite gate run before the body is read, so a refused
//     upload costs no parse (D78, D87, D129).
//   - Records are idempotent upserts on their natural key. A request's deletions,
//     its queued days and the ingested event commit in one transaction under the
//     per-user lock (D145), after the records are stored, so the app can send an
//     unfinished request again unchanged.
//   - A conflict between two syncs of one user (unique violation, deadlock,
//     serialization failure) and a failed apply are a 503 with Retry-After, never
//     a 500.
//   - NULs in strings are stripped, not refused: the app does not retry a 422,
//     and one NUL from a device name would stall that upload stream for good.
//   - Unknown sample types and sleep stages, and records too far in the future or
//     past, are dropped and counted (ignored.*) rather than failing the sync.
//
// See docs/API.md → POST /v1/sync; D78, D87, D101, D129, D144, D145.
package main
