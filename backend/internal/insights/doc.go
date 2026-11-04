// Package insights turns recent scored days into the short cards on the
// Insights screen, one rule at a time.
//
// It must not do I/O: a History goes in and cards come out, so a rule is
// testable without a database. A rule is a func(History) *model.Insight that
// returns nil when it has nothing to say; adding one is a function and a line
// in Rules.
//
// Invariant: thresholds sit together in rules.go, so a tweak is a one-line
// change.
//
// See docs/ARCHITECTURE.md → Services; D13.
package insights
