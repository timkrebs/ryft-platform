// scoring scores the pushed ingest events and runs the 15-minute tick that keeps today and yesterday fresh.
//
// Its push routes (/push/ingested, /push/session.completed) turn raw samples into
// daily Recovery, Strain and Sleep scores when a sync's event arrives (D218); its tick
// step, the scoring pass, refreshes the active users and on Sundays runs the weekly
// sleep-baseline estimate (D219). The rescore and learn-baselines subcommands run the
// same work once and exit.
//
// It must not serve user HTTP (push, tick, health and metrics only) or run on more than
// one instance: a user's events are pushed one at a time by their ordering key, oldest
// first (D17 as amended by the GCP design: max-instances 1, concurrency 4).
//
// main hands internal/app a Spec; service.go builds the push routes and the tick step
// from the opened pool and transport. A push answers with the handler's result, so
// Pub/Sub retries and dead-letters by it; the pass logs and swallows a user's error
// and fails only when the tick's 240 s budget ends it. The subcommands run outside app,
// with the service's environment.
//
// Invariants:
//   - The handlers are idempotent (D100): Pub/Sub delivers at least once.
//   - What scoring tells notify (scored, session.scored, refresh) goes through
//     the outbox (D101) and is flushed before the push or the tick answers (D219).
//   - rescore skips users without consent (D78).
//
// See docs/ARCHITECTURE.md → Scoring; docs/SCORING.md; D50, D92, D112, D218, D219.
package main
