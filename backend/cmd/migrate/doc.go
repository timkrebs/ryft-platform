// migrate applies the migrations as the schema owner, loads the built-in
// exercises and plans, and sets the service roles' passwords where they changed.
//
// It runs once per deploy, before the services (Argo CD PreSync, D95; a one-shot
// compose service locally). It must not run as a service role, which cannot run
// DDL, and nobody runs it against production by hand. It reads its own
// environment and does not use internal/config:
//
//	DATABASE_URL          the owner (postgres on Supabase)
//	DB_PASSWORD_INGEST    password of ryft_ingest, likewise _SCORING, _API and _NOTIFY
//
// Each migration runs in its own transaction under an advisory lock, and a
// role's password is written only when it changed, so the pooler's circuit
// breaker is not tripped on every deploy (D93).
//
// `migrate smoke` is instead the in-cluster smoke after a deploy (internal/smoke),
// which Argo CD runs as the PostSync hook in namespace ryft-smoke: this image is
// the one tool image, so a hook needs no image of its own. It reads SMOKE_* and
// SUPABASE_* variables for that.
//
// See docs/ARCHITECTURE.md → Services, Database roles; docs/OPERATIONS.md; D42, D93, D95.
package main
