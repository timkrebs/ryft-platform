// Package ratelimit owns the in-memory token bucket per user and the platform's per-request-class limits.
//
// Limits are per pod: with N replicas behind the load balancer a user gets up
// to N times the configured rate, which is fine for damping a runaway client. It
// must not be the only guard of anything that needs a global limit: a recompute
// is limited in the database (store.TryRequestRecompute), across replicas.
//
// Invariants:
//   - Buckets are keyed by the verified user id, not the client IP: mobile
//     clients share carrier NAT addresses (D36).
//   - Idle buckets are dropped, so memory follows the active users, not every
//     user ever seen.
//   - The class limits (sync, session saves, invite codes, the training import,
//     everything else) are the contract's numbers: docs/API.md → Conventions,
//     and for invite codes POST /v1/me/invite.
//
// See docs/API.md → Conventions (Rate limits); D36.
package ratelimit
