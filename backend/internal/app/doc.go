// Package app owns a long-running service's lifecycle: it opens the pool and the transport, serves the push, tick and health routes, and stops in reverse.
//
// A service's main hands Main (or a test, Run) a Spec: its settings, and a Build
// that makes the routes, readiness checks, push routes and tick steps from the
// opened pool and transport. Subcommands and migrate are one-shot and stay outside.
//
// It may import the database and the transport (pgxpool in Deps, eventbus to open
// it), which is why the arch test names it in R7; only the four service
// commands may import it (R13).
//
// Invariants:
//   - Nothing connects before the service's settings are valid (D78), and the
//     HTTP listener opens last, so the start-up probe still covers the database
//     wait. A signal while the start waits ends Build's context.
//   - Cloud Run gives CPU only while a request runs (D219), so nothing works in
//     the background: the consumers are push routes (Parts.Push), the timers
//     are steps of POST /internal/tick (Parts.Ticks, after app's db ping and
//     outbox sweep, within Spec.TickBudget), and the outbox is flushed inline
//     by whoever wrote it. The tick and the pushes sit outside any /v1
//     middleware; the transport's push verifier is their auth (D218).
//   - One signal handler (Main). The first signal, or a server that fails or
//     panics, starts the shutdown, which undoes the start in reverse: /readyz
//     answers 503 draining, the drain delay passes, HTTP drains within its
//     budget, then the pool closes. Each step logs "shutdown step" with its
//     duration (the /metrics stop only when it fails) and is bounded; the bounds
//     add up to less than Spec.Grace (see app.go), and a watchdog ends the
//     process before the kubelet's SIGKILL. A step that overruns is logged and
//     the shutdown goes on; Run then returns an error (exit 1, and 2 after a
//     recovered panic, as an unrecovered one exits). A signal during the start's
//     waits is a clean stop (exit 0); a Build that ignores its ctx ends at the
//     kubelet's SIGKILL.
//   - The context Build receives ends after the http step, not at the signal. A
//     push or a tick still in flight when the http step's budget ends is cut:
//     Pub/Sub delivers the push again (D100), the tick's steps see their
//     context end.
//   - The servers are Tasks in errgroup tiers with panic recovery
//     (ryft_goroutine_panics_total). The /readyz checks and the bounded
//     waits at shutdown are short-lived goroutines of their own: a check ends
//     with its context, and a wait gives up after its bound (the call it
//     waits for keeps running; the process is about to exit).
//   - /healthz stays 200 until the http step closes the listener. /readyz names
//     each check (db, auth) and answers the same status codes as before.
//
// See docs/ARCHITECTURE.md → Services; D78, D168-D172 (the lifecycle, the shutdown order
// and budget, /readyz, errgroup and goleak, shutdown measured in the log), D218, D219.
package app
