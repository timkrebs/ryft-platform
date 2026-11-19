// Package httpx owns the HTTP plumbing every service shares: error shape, JSON I/O with size and NUL rules, the middleware chain, the server, health, the logger.
//
// It must stay below auth, store, db and events: auth, gate and the services
// import httpx, never the reverse. It knows logx and metrics only.
//
// Invariants:
//   - api and ingest wrap their mux as Instrument, Recover, SecurityHeaders, the
//     per-address edge limit and app's start gate (internal/app), then the mux.
//     Behind auth a /v1 route usually passes Timeout and WithRoutes (api puts its
//     invite gate in between), with exceptions: DELETE /v1/me and the training
//     import have their own deadlines, and /v1/admin has its own chain (CORS, a
//     per-address limit, the admin key or an admin's Supabase token), not auth.
//     scoring and notify serve only health, their pushes and the tick and use
//     Recover alone.
//   - Server speaks HTTP/1.1 and h2c with prior knowledge on one port (D220), so
//     Cloud Run's front end carries a 32 MiB sync over HTTP/2.
//   - The health paths come in pairs, /health/live = /healthz and /health/ready =
//     /readyz (Cloud Run cannot route the z paths). On Cloud Run (K_SERVICE) a
//     ready path answers 503 {"error":"not ready"} with the cause only in the log,
//     and reuses its answer for 5 s (CloudReady).
//   - CORS is for the operator's page only: api wraps /v1/admin/ in it and nothing
//     else, with exact origins from ADMIN_WEB_ORIGINS and no credentials. It
//     answers a preflight itself, before any auth.
//   - ClientKeys is the one reading of the client's address, made once per
//     process from XFF_TRUSTED_HOPS: X-Forwarded-For from the right, Google's
//     front-end ranges skipped, the hops-th remaining entry as it is (a client
//     inside a VPC keys by its own private address); anything else is one shared
//     fallback key, never RemoteAddr. A limiter gets an HMAC of the address
//     under a key of the process, never the address. No address is logged, not
//     even in part (D222): the sampled "client address" line (one a minute per
//     instance, no request id or user, for runbook step 9) says only which
//     entry was picked (picked, xff_index) and how many there were (xff_entries).
//   - Detach is the one way to run past the client's cancel: it ends at the
//     server's hard stop (WithStop), so the pool can close after the HTTP step.
//   - The request id is the server's (a client's X-Request-ID is logged as
//     client_request_id, never used, D35). The route label is the ServeMux
//     pattern, so metric cardinality stays bounded (D14).
//   - Fail answers a generic message for what the client cannot act on (D37) and
//     the details go to the log. A deadline is a context timeout, which Fail turns
//     into a retryable 503 (D38).
//   - Text Postgres cannot store (NUL, bytes that are not UTF-8) is refused or
//     stripped at the edge, so it never becomes a 500.
//
// The package reads the environment in two places: Logger (LOG_LEVEL; K_SERVICE or
// CLOUD_RUN_JOB pick Cloud Logging's format, GCP_PROJECT its traces) and
// CloudRunService (K_SERVICE: Health's ready answer, app's /metrics listener).
//
// See docs/ARCHITECTURE.md → Services (Request path); docs/API.md → Errors; D14,
// D19, D35, D37, D38.
package httpx
