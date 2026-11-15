// notify wires internal/notify to its push routes and the 15-minute tick.
//
// It sends the morning score and a silent refresh when scoring publishes scored,
// a scored strength session, and a reminder when a user's data has stopped
// arriving. Pub/Sub pushes the events (D218); the tick runs the stale check (D219).
//
// It must not publish events or read more than the ryft_notify role may (migrations
// 0015, 0023, 0028). Its one outbox is the wait box (publisher notify, relayed into
// ryft-notify-wait only): a push it defers (quiet hours, APNs down) is written there
// with its not-before time, the tick's sweep publishes it when due, and /push/wait
// hands it back under its own key and message ID (D219).
//
// main hands internal/app a Spec; service.go builds the push routes and the tick step.
// A push answers with the handler's result, so Pub/Sub retries and dead-letters by it;
// the stale check's error is the tick's to log.
//
// Invariant: without an APNs key it runs and counts the event pushes it would
// have sent (ryft_push_notifications_total{result="unconfigured"}) instead of
// failing; the stale check does nothing without a key.
//
// See docs/ARCHITECTURE.md → Services; D70–D73, D92, D218, D219.
package main
