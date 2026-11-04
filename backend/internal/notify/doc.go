// Package notify decides what RYFT pushes, to whom and when.
//
// The pushes are the morning score, a scored strength session or its records, a
// silent refresh, and a reminder when no data has arrived. cmd/notify runs it.
//
// It must not write anything but notifications_sent and push_tokens (dead
// tokens are deleted, last_success_at is updated): the ryft_notify role allows
// nothing else (migrations 0015, 0023, and 0028 for reading sessions.origin).
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
//   - Messages carry localisation keys the app resolves, not finished prose.
//
// See docs/API.md → Push notifications; D70–D73, D92.
package notify
