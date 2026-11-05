// notify wires internal/notify to its queue and a 15-minute timer.
//
// It sends the morning score and a silent refresh when scoring publishes scored,
// a scored strength session, and a reminder when a user's data has stopped
// arriving.
//
// It must not publish events (it uses no outbox) or read more than the
// ryft_notify role may (migrations 0015, 0023, 0028).
//
// main hands internal/app a Spec; service.go builds the stale check (a job) and the
// consumer. Both log and swallow a transient error: one they return stops the service.
//
// Invariant: without an APNs key it runs and counts the event pushes it would
// have sent (ryft_push_notifications_total{result="unconfigured"}) instead of
// failing; the stale check does nothing without a key.
//
// See docs/ARCHITECTURE.md → Services; D70–D73, D92.
package main
