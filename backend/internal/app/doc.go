// Package app owns a service's lifecycle: it opens the pool and the transport, serves the push, tick and health routes, pushes the metrics, and stops in reverse.
//
// A service's main hands Main (or a test, Run) a Spec: its settings, and a Build
// that makes the routes, readiness checks, push routes and tick steps from the
// opened pool and transport, or (api, ingest) its whole mux (Parts.Handler, its
// tick from Deps.Tick). Subcommands and migrate are one-shot and stay outside.
//
// It may import the database and the transport (pgxpool in Deps, eventbus to open
// it), which is why the arch test names it in R7; only the four service
// commands may import it (R13).
//
// Invariants:
//   - Nothing connects before the service's settings are valid (D78): the
//     service's own, config.LoadOTLP's (the metrics push, D224) and the common
//     ones. The HTTP listener opens before the pool, behind the start gate:
//     /health/live and /healthz answer 200 at once (Cloud Run's startup probe),
//     the ready paths 503, any other request waits a few seconds for the start
//     and is then served or answered 503 with Retry-After: 5, and a tick that
//     waited in vain reports ryft_db_up 0 and pushes it. A signal while the start
//     waits ends Build's context.
//   - The metrics push is the tick's final step and runs once more after the
//     http step at SIGTERM. On Cloud Run (K_SERVICE) there is no /metrics
//     listener, and the ready paths answer {"error":"not ready"} with the cause
//     only in the log, reused for 5 s.
//   - Cloud Run gives CPU only while a request runs (D219), so nothing works in
//     the background: the consumers are push routes (Parts.Push), the timers
//     are steps of POST /internal/tick (Parts.Ticks, after app's db ping and
//     outbox sweep, within Spec.TickBudget), and the outbox is flushed inline
//     by whoever wrote it. The tick and the pushes sit outside any /v1
//     middleware; the transport's push verifier is their auth (D218).
//   - One signal handler (Main). The first signal, or a server that fails or
//     panics, starts the shutdown, which undoes the start in reverse: /readyz
//     answers 503 draining, the drain delay passes, HTTP drains within its
//     budget, the metrics push runs, then the pool closes. Each step logs
//     "shutdown step" with its duration (the /metrics stop only when it fails)
//     and is bounded; the bounds add up to less than Spec.Grace, Cloud Run's
//     10 s (see app.go), and a watchdog ends the process a second before its
//     SIGKILL. A step that overruns is logged and the shutdown goes on; Run then
//     returns an error (exit 1, and 2 after a recovered panic, as an unrecovered
//     one exits). A signal during the start's waits is a clean stop (exit 0); a
//     Build that ignores its ctx ends at the SIGKILL.
//   - The context Build receives ends after the http step, not at the signal. A
//     request still in flight when the http step's budget ends is cut: its
//     context ends (the server's base context, and httpx.Detach's with it), so
//     its database calls give their connections back before the pool closes;
//     Pub/Sub delivers a push again (D100), the tick's steps see their context
//     end.
//   - The servers are Tasks in errgroup tiers with panic recovery
//     (ryft_goroutine_panics_total). The /readyz checks and the bounded
//     waits at shutdown are short-lived goroutines of their own: a check ends
//     with its context, and a wait gives up after its bound (the call it
//     waits for keeps running; the process is about to exit).
//   - /healthz and /health/live stay 200 until the http step closes the
//     listener. Off Cloud Run /readyz and /health/ready name each check (db,
//     auth) and answer the same status codes as before.
//
// See docs/ARCHITECTURE.md → Services; D78, D168-D172 (the lifecycle, the shutdown order
// and budget, /readyz, errgroup and goleak, shutdown measured in the log), D218, D219.
package app
