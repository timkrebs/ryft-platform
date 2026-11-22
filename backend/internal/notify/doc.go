// Package notify decides what RYFT pushes, to whom and when.
//
// The pushes are the morning score, a scored strength session or its records, a
// scored Apple Health workout (ACTIVITY_SCORED), a silent refresh, and a reminder
// when no data has arrived. cmd/notify runs it.
//
// It must not write anything but notifications_sent, workout_alerts and
// push_tokens (dead tokens are deleted, last_success_at is updated): the
// ryft_notify role allows nothing else (migrations 0015, 0023, 0028 for reading
// sessions.origin, and 0041 for the ledger and the workout columns the scan reads).
// It publishes no events.
//
// Invariants:
//   - A claimed (user, day, category) is sent once: a redelivered event or a
//     second pod finds the claim and sends nothing; the claim is released only
//     when APNs could not be reached (D72). Per-sync refreshes are never
//     claimed.
//   - Quiet hours hold the morning alert until they end if that is still the
//     same local day, and drop it otherwise; session and record pushes are
//     dropped, SYNC_STALE is skipped, and silent refreshes ignore them (D70).
//   - Per-sync refreshes are throttled to one per RefreshEvery per account, in
//     the single replica's memory; the morning refresh (once a day, claimed)
//     counts toward it (D92).
//   - Every refresh event, whether its silent push was sent, throttled or had no
//     device, scans the account's workouts of the last 12 hours and decides each
//     once (ACTIVITY_SCORED, D253): the pure ActivityRule, Inherited and
//     ActivityOutcome, and a workout_alerts row written under a per-user lock
//     (store.DecideWorkoutAlert), so a redelivered event, a second pod or a
//     rewrite of the workout under a new id sends nothing twice (D100, D254). A
//     workout that fails a rule leaves no row and is looked at again.
//   - Every payload carries the recipient's account in ryft.account (D256).
//   - Messages carry localisation keys the app resolves, not finished prose.
//
// See docs/API.md → Push notifications; D70–D73, D92.
package notify
