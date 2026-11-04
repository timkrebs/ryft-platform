// Package db owns the Postgres pool, the embedded migrations, the service roles'
// passwords, the built-in seeds and the services' wait for the schema.
//
// It must not hold domain queries (store owns those) or edit an applied
// migration: a change is a new numbered file in migrations/. Only cmd/migrate
// runs Migrate, Seed and the role functions, as the schema owner; the services
// use their own least-privilege roles, which cannot run DDL, and wait for the
// schema with Open and WaitMigrated instead (D42, D47).
//
// Invariants:
//   - Each migration runs in its own transaction under one advisory lock, with
//     a lock_timeout, so concurrent migrators queue and none blocks a busy
//     table indefinitely.
//   - A role's password reaches Postgres only as a SCRAM-SHA-256 verifier, never
//     as plaintext (D42), and EnsureRolePassword writes it only when it changed
//     (D93).
//   - A table's grants for each service role are in the migration that adds the
//     table (TestServiceRoleGrants).
//
// See docs/ARCHITECTURE.md → Database roles; docs/SUPABASE.md; D28, D42, D47, D93.
package db
