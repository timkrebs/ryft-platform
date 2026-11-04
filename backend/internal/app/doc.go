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
//     and broker wait. A signal while the start waits ends Build's context.
//   - One signal handler (Main). The first signal, or any Task that fails or
//     panics, starts the shutdown, which undoes the start in reverse: /readyz
//     answers 503 draining, timers stop, the drain delay passes, consumers
//     stop, HTTP drains within its budget, then the relay and db watch, the
//     broker and the pool. Each step logs "shutdown step" with its duration and
//     is bounded; the bounds add up to less than Spec.Grace (see app.go), and a
//     watchdog ends the process before the kubelet's SIGKILL. A step that
//     overruns is logged and the shutdown goes on; Run then returns an error
//     (exit 1, and 2 after a recovered panic, as an unrecovered one exits).
//   - The contexts that Build and the tasks receive end with the shutdown's own
//     steps: the consumers' at their step, after the drain delay, not at the
//     signal. Until amqpbus lets a delivery in hand finish (A4), a consumer
//     still sees its ctx end when its step begins, so the delivery in hand is
//     cut then.
//   - The service's own goroutines (consumers, timers, relay, db watch, the
//     servers) are Tasks in errgroup tiers with panic recovery
//     (ryft_goroutine_panics_total). The /readyz checks and the bounded
//     waits at shutdown are short-lived goroutines of their own, and end with
//     their context or their bound.
//   - /healthz stays 200 until the http step closes the listener. /readyz names
//     each check (db, auth, broker) and answers the same status codes as
//     before; the broker gates it only where Spec.BrokerRequired says so (D101).
//
// See docs/ARCHITECTURE.md → Services; D78, D101.
package app
