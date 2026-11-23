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
//     The exceptions run before any token is checked, so there is no user yet:
//     the edge limit of api and ingest (internal/app: 20/s, bursts of 50, D53; and
//     its hook class for a provider's pushes to POST /connect/..., 50/s, bursts of 200, D257)
//     and /v1/admin/* (cmd/api: 10/s, bursts of 20, D155). Both are a Capped,
//     keyed by httpx.ClientKeys, an HMAC of the client's address under a key of
//     the process: no map holds an address, and none is logged or a label.
//   - Idle buckets are dropped, so memory follows the active users, not every
//     user ever seen. A Capped also bounds its keys (a client picks its address,
//     and one IPv6 /48 is 65,536 /64s): the newcomers beyond the cap share one
//     bucket, the known keys keep theirs.
//   - The class limits (sync, session saves, invite codes, the training import,
//     a connection start: bursts of 5, then one a minute, D257, everything else) are the contract's numbers: docs/API.md → Conventions,
//     and for invite codes POST /v1/me/invite.
//
// See docs/API.md → Conventions (Rate limits); D36.
package ratelimit
