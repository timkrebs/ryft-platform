package store

// Advisory locks (transaction-level, pg_advisory_xact_lock) in use. The first key is
// the class, the second a hash of the object. A class is never reused: a new lock takes
// the next number, so two unrelated paths never serialise on one key.
//
//	7419001  migrationLock (internal/db/db.go; one key, all migrators)
//	7419002  lockUser         EnsureUser and DeleteUser, per user; the merge takes it for both accounts
//	                        first (D302: then lockApply for both, each pair in uuid-string order)
//	7419003  lockSession      saves of one Train session, per user and session
//	7419004  lockEnrollments  a user's plan enrolment changes
//	7419005  lockApply        a user's HealthKit writes and sync apply, workout fold,
//	                        native connection changes, inbox writes, consent changes, and a
//	                        PATCH /v1/me with sex or weight_kg (its consent check, D306); and the
//	                        account merge, for both accounts, after lockUser (D302)
//	7419006  relayLock        the outbox relay, per publisher (internal/events/outbox; every
//	                        drain of a Flush or a Sweep, so one publisher's rows go out in
//	                        id order across instances, D101, D219)
//	7419007  lockAttest       a user's App Attest writes (CreateAttestChallenge, PutAttestKey),
//	                        so the caps of 5 challenges and 10 keys hold across api instances
//	7419009  lockConnectCap  a provider's user cap and its confirm swap (D257), per provider.
//	                        7419008 belongs to open PR push/activity-scored.
//	7419010  lockCheckin     a user's check-in writes and their rescoring of the later ones (D292), per user
const (
	lockUser        = `SELECT pg_advisory_xact_lock(7419002, hashtext($1::text))`
	lockSession     = `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`
	lockEnrollments = `SELECT pg_advisory_xact_lock(7419004, hashtext($1::text))`
	lockApply       = `SELECT pg_advisory_xact_lock(7419005, hashtext($1::text))`
	lockAttest      = `SELECT pg_advisory_xact_lock(7419007, hashtext($1::text))`
	lockConnectCap  = `SELECT pg_advisory_xact_lock(7419009, hashtext($1::text))`
	lockCheckin     = `SELECT pg_advisory_xact_lock(7419010, hashtext($1::text))`
)
