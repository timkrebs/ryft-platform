// migrate applies the migrations as the schema owner, loads the built-in
// exercises and plans, and sets the service roles' passwords where they changed.
//
// It runs on every deploy before the services: deploy.yml executes the Cloud Run
// job deploy/run/migrate.yaml and stops when it fails (D223; a one-shot compose
// service locally), so it must be idempotent. It must not run as a service role,
// which cannot run DDL. Outside a deploy the operator runs the job by hand only to
// rotate a role's password (docs/OPERATIONS.md).
// It reads its own environment and does not use internal/config:
//
//	DATABASE_URL          the owner (postgres on Supabase)
//	DB_PASSWORD_INGEST    password of ryft_ingest, likewise _SCORING, _API and _NOTIFY
//
// Each migration runs in its own transaction under an advisory lock, and a
// role's password is written only when it changed, so the pooler's circuit
// breaker is not tripped on every deploy (D93).
//
// `migrate smoke` is instead the Go smoke (internal/smoke), which CI runs against
// the compose stack (`make smoke-hook`): this image is the one tool image, so the
// smoke needs no image of its own. It reads SMOKE_* and SUPABASE_* variables for
// that.
//
// See docs/ARCHITECTURE.md → Services, Database roles; docs/OPERATIONS.md; D42, D93, D95.
package main
