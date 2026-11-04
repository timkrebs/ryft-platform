// Package store owns every SQL statement against the domain tables; the services
// reach Postgres only through its methods (db owns the schema, outbox its table).
//
// It must not talk to the broker (an event is an outbox row in the caller's
// transaction, D101) or decide a value: those rules are pure functions in
// scoring/workoutdup, train and behavior, which it calls. It does own the
// transactions, the advisory locks (locks.go, D145) and the SQL form of a rule.
//
// Invariants:
//   - A method is one unit of work: one statement, or one transaction it opens
//     and commits (ApplySync, DeleteUser, MergeUsers, FoldWorkouts, the import).
//   - A user-scoped method takes the user id first after ctx and puts it in
//     every WHERE. The service roles' RLS policies admit every row (migration
//     0009, D42): the grants limit a service to its tables, and this WHERE is
//     the only per-user line inside it. RLS keeps anon and authenticated out (D28).
//   - The exceptions are cross-user or global, and serve the operator, the
//     timers and the reconcilers: the admin, audit, feedback and invite reads,
//     the flags, the active-user, sync-age and pending-work scans, push-token
//     pruning, ResolveUser, MergeUsers, EnsureUser, Ready and WithImportRules.
//
// See docs/ARCHITECTURE.md → Data model, Database roles; D31, D39, D42, D145, D146.
package store
