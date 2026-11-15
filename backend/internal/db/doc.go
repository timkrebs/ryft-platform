// Package db owns the Postgres pool, the embedded migrations, the service roles'
// passwords, the built-in seeds and the services' wait for the schema.
//
// It must not hold domain queries (store owns those) or edit an applied
// migration: a change is a new numbered file in migrations/. Only cmd/migrate
// runs Migrate, Seed and the role functions, as the schema owner; the services
// use their own least-privilege roles, which cannot run DDL, and wait for the
// schema with Open and WaitMigrated instead (D42, D47). Each service's tick
// pings the database with Ping, which sets ryft_db_up (D219).
//
// Invariants:
//   - Each migration runs in its own transaction under one advisory lock, with
//     a lock_timeout, so concurrent migrators queue and none blocks a busy
//     table indefinitely.
//   - A role's password reaches Postgres only as a SCRAM-SHA-256 verifier, never
//     as plaintext (D42), and EnsureRolePassword writes it only when it changed
//     (D93).
//   - A table's grants for each service role belong in the migration that adds
//     the table; TestServiceRoleGrants pins the resulting grant matrix.
//   - On Cloud Run (K_SERVICE or CLOUD_RUN_JOB) DATABASE_URL has no password (pgx
//     reads PGPASSWORD), sslmode=verify-full and an sslrootcert; a service's also
//     has port 6543 (Supavisor's transaction mode) and default_query_exec_mode=exec.
//     PoolConfig refuses anything else without repeating the URL (cloudrun.go), and
//     Open retries no refused login there (Supavisor's circuit breaker).
//   - The pool leaves no session state on a server connection: no startup parameter,
//     no statement from a hook; AfterConnect maps types on the client only
//     (registerTypes, which also gives []uuid.UUID a type in exec mode). Services' SQL
//     uses SET LOCAL and transaction-level advisory locks
//     (scripts/check_session_state.sh).
//
// See docs/ARCHITECTURE.md → Database roles; docs/SUPABASE.md; D28, D42, D47, D93, D221.
package db
