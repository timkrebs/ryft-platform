// Package store owns every SQL statement against the domain tables; the services
// reach Postgres only through its methods (db owns the schema, outbox its table).
//
// It must not talk to the broker (an event is an outbox row in the caller's
// transaction, D101) or decide a value: those rules are pure functions in
// scoring/workoutdup, train and behavior, which it calls. It does own the
// transactions, the advisory locks (locks.go, D145) and the SQL form of a rule.
//
// Invariants:
//   - The multi-row writes that must not be seen half done (ApplySync, DeleteUser,
//     MergeUsers, FoldWorkouts, AddFeedback, a Train session's completion) are one
//     transaction the method opens and commits. That is not a rule for every
//     method: some run several statements on the pool (ReleaseSessionWorkouts,
//     AdvanceEnrollment, ArchiveBehavior, AcquireImportLease), and some reads
//     (AdminUserDetail, JournalDay, Trend, SyncStateOf) read without one
//     snapshot. The import writes its outbox row in its own transaction under
//     publisher "api" (FinishImport).
//   - Of the 134 exported methods, 107 take the user id first after ctx and scope
//     by it. Not every WHERE names it: RedeemInvite matches the global invite by
//     code, IsAdminAccount looks the id up in the operators' allow-list (admin_accounts,
//     operator configuration that account deletion does not touch), a statement that looks a row up by its own id (ArchiveBehavior,
//     UpdateCustomExercise, Enroll) checks ownership around the lookup, and
//     built-in rows are read with user_id IS NULL OR user_id = $1.
//   - The service roles' RLS policies admit every row (USING (true), migration
//     0009, D42): the grants limit a service to its tables, and the WHERE clause
//     is the only per-user line inside a service role. The RLS that does bite is
//     RESTRICTIVE per source (ryft_scoring may write only ryft-session workouts,
//     0011; ryft_ingest may not delete them, 0029). anon and authenticated are
//     kept out by REVOKE ALL, and authenticated sees only its own rows (0007, D28).
//   - The other 27 take no user id or take another key. Operator and admin:
//     AdminUsers, Audit, AuditSince, CreateInvite, DeleteInvite, Invites,
//     ResolveFeedback, FeedbackSince, ResolveUser, MergeUsers, PutFlag,
//     DeleteFlag. Timers and reconcilers: ActiveUsers, SyncAges, StaleCandidates,
//     ResurrectedUsers, PendingAppleRevocations, PendingAuthDeletions,
//     PruneScoreQueue. One-off commands (rescore, fold-workouts): UserIDs. Keyed
//     by a push token: PushDelivered, DropPushToken. Global reads called on user
//     request paths (api and ingest): Flags (GET /v1/config, the import) and
//     ExpectedScoreSec. Wiring: Ready, WithImportRules. EnsureUser is
//     per-user but keyed by identity.
//
// See docs/ARCHITECTURE.md → Data model, Database roles; D31, D39, D42, D145, D146.
package store
