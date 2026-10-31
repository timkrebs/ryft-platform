package store

// Advisory locks (transaction-level, pg_advisory_xact_lock) in use. The first key is
// the class, the second a hash of the object. A class is never reused: a new lock takes
// the next number, so two unrelated paths never serialise on one key.
//
//	7419001  migrationLock (internal/db/db.go; one key, all migrators)
//	7419002  lockUser         EnsureUser and DeleteUser, per user
//	7419003  lockSession      saves of one Train session, per user and session
//	7419004  lockEnrollments  a user's plan enrolment changes
//	7419005  lockApply        a user's sync apply transaction (deletions, queueing)
const (
	lockUser        = `SELECT pg_advisory_xact_lock(7419002, hashtext($1::text))`
	lockSession     = `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`
	lockEnrollments = `SELECT pg_advisory_xact_lock(7419004, hashtext($1::text))`
	lockApply       = `SELECT pg_advisory_xact_lock(7419005, hashtext($1::text))`
)
