// scoring runs the scoring consumer and the 15-minute pass that keeps today and yesterday fresh.
//
// The consumer turns raw samples into daily Recovery, Strain and Sleep scores
// when a sync's event arrives (queue scoring); on Sundays the pass also runs the
// weekly sleep-baseline estimate. The rescore and learn-baselines subcommands
// run the same work once and exit.
//
// It must not serve user HTTP (health and metrics only) or run with more than
// one replica: recompute ranges are scored oldest first by one consumer (D17).
//
// Invariants:
//   - The handlers are idempotent (D100).
//   - What scoring tells notify (scored, session.scored, refresh) goes through
//     the outbox (D101).
//   - rescore skips users without consent (D78).
//
// See docs/ARCHITECTURE.md → Scoring; docs/SCORING.md; D50, D92, D112.
package main
