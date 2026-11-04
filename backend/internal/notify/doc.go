// Package notify decides what RYFT pushes, to whom and when: the morning score,
// a scored strength session or its records, a silent refresh, and a reminder
// when no data has arrived. cmd/notify runs it.
//
// It must not write anything but notifications_sent and push_tokens (dead
// tokens are deleted, last_success_at is updated): the ryft_notify role allows
// nothing else (migration 0015). It publishes no events.
//
// Invariants:
//   - A (user, day, category) is claimed before it is sent, so a redelivered
//     event or a second pod sends nothing; the claim is released only when
//     APNs could not be reached (D72).
//   - Quiet hours defer a push instead of dropping it.
//   - A refresh reaches one account at most once per RefreshEvery (D92).
//   - Messages carry localisation keys the app resolves, not finished prose.
//
// See docs/API.md → Push notifications; D70–D73, D92.
package notify
