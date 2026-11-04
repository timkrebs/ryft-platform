// Package httpx owns the HTTP plumbing every service shares: error shape, JSON I/O with size and NUL rules, the middleware chain, health and shutdown.
//
// It must stay below auth, store, db and events: auth, gate and the services
// import httpx, never the reverse. It knows logx and metrics only.
//
// Invariants:
//   - api and ingest wrap their mux as Instrument, Recover, SecurityHeaders, mux.
//     Behind auth a /v1 route usually passes Timeout and WithRoutes (api puts its
//     invite gate in between), with exceptions: DELETE /v1/me and the training
//     import have their own deadlines, and /v1/admin is behind its own key, not
//     auth. scoring and notify serve only health and metrics and use Recover alone.
//   - The request id is the server's (a client's X-Request-ID is logged as
//     client_request_id, never used, D35). The route label is the ServeMux
//     pattern, so metric cardinality stays bounded (D14).
//   - Fail answers a generic message for what the client cannot act on (D37) and
//     the details go to the log. A deadline is a context timeout, which Fail turns
//     into a retryable 503 (D38).
//   - Text Postgres cannot store (NUL, bytes that are not UTF-8) is refused or
//     stripped at the edge, so it never becomes a 500.
//
// httpx.Logger also reads LOG_LEVEL; that is the one place the package touches
// the environment.
//
// See docs/ARCHITECTURE.md → Services (Request path); docs/API.md → Errors; D14,
// D19, D35, D37, D38.
package httpx
