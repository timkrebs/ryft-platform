// Package behavior owns the journal's arithmetic: the fixed ids of the built-in
// behaviors, what an entry may hold, and how a behavior changes the morning after.
//
// It must not do I/O: the impact statistics are pure functions of entry days, so
// the store and the api decide only what to load and persist.
//
// Invariants:
//   - Built-in ids are UUIDv5 of the slug in a fixed namespace, so an id is the
//     same in every environment and never changes.
//   - "Unknown" is not "no": a day never saved, or a behavior not sent that day,
//     does not count as a day without it (D74).
//
// See docs/SCORING.md → Journal; D74–D77.
package behavior
