// Package logx owns the services' JSON logging: one logger per service, with the request's identity added to every line.
//
// It is a leaf: it imports nothing internal. Every line carries the service name
// and, when logged with a request's context (InfoContext and friends), its
// request_id, user_id and route, so one request can be followed across ingest,
// the broker and scoring (events carry the request id and rebuild the Fields).
//
// Two formats: New and NewWriter write time, level, msg (compose, the tests, the
// runbooks' kubectl greps); NewCloud writes Cloud Logging's severity and message and the
// request's trace (logging.googleapis.com/trace), which httpx.Logger picks on Cloud Run
// (K_SERVICE or CLOUD_RUN_JOB set, design §6). The other keys are the same in both.
//
// Invariants:
//   - logx does not filter. That no token, email, request body or health value is
//     logged is the call sites' rule (D114); each service's log test lists the
//     keys a message may carry and fails on any other (internal/logx/logcheck).
//   - Where a decode error or an HTTP status would be logged, JSONError
//     (for a type-mismatch decode error) and StatusLine give versions that cannot
//     quote a number from the body or the server's own words.
//
// See docs/OPERATIONS.md → Data protection (logs); docs/OBSERVABILITY.md; D114.
package logx
