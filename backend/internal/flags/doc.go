// Package flags evaluates remote-config feature flags for one user (GET
// /v1/config): flags and a user id in, booleans out.
//
// It must not do I/O; the api loads the flags.
//
// Invariants:
//   - A rollout is deterministic per user and flag key, so a user does not flip
//     between requests or pods and raising a percentage only adds users.
//   - enabled = false wins over the allow-list and the rollout: it is the kill
//     switch.
//
// See docs/API.md → GET /v1/config; D69.
package flags
