// Package app owns a long-running service's lifecycle: it starts the pool, the broker, consumers and HTTP in order and stops them in reverse.
//
// A service's main hands Main (or a test, Run) a Spec: its settings, and a Build
// that makes the routes, readiness checks, consumers and timers from the opened
// pool and broker. Subcommands and migrate are one-shot and stay outside.
//
// It may import the database and the broker (pgxpool in Deps, eventbus to open
// it), which is why the arch test names it in R7; only the four service
// commands may import it (R13).
//
// Invariants:
//   - Nothing connects before the service's settings are valid (D78), and the
//     HTTP listener opens last, so the start-up probe still covers the database
//     and broker wait.
//   - One signal handler (Main). The first signal, or any Task that fails or
//     panics, starts the shutdown, which undoes the start in reverse: /readyz
//     answers 503 draining, timers stop, the drain delay passes, consumers
//     stop, HTTP drains within its budget, then the relay and db watch, the
//     broker and the pool. Each step logs "shutdown step" with its duration.
//     A step that overruns is logged and the shutdown goes on; Run then
//     returns an error (exit 1).
//   - The contexts that tasks and Build receive end with the shutdown's own
//     steps, never at the signal, so a delivery in hand is not cut by it.
//   - Every goroutine of the service is a Task in an errgroup tier with panic
//     recovery (ryft_goroutine_panics_total) and ends when its ctx ends.
//   - /healthz stays 200 until the process exits. /readyz names each check
//     (db, auth, broker) and answers the same status codes as before; the
//     broker gates it only where Spec.BrokerRequired says so (D101).
//
// See docs/ARCHITECTURE.md → Services; D78, D101.
package app
