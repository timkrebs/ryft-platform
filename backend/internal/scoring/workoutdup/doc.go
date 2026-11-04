// Package workoutdup owns the two rules that make one activity one workout: the
// same-source fold and the cross-source duplicate mark.
//
// It must not import store, scoring or a clock. The rules are pure functions of
// the rows, so the store can run the fold inside the sync's transaction without
// an import cycle through the scoring engine, which imports the store (D146).
// Only store and scoring import it; ingest reaches the fold through the store.
//
// Invariants:
//   - Overlap ratios are integer fractions, so a boundary such as exactly 50 %
//     or 80 % does not depend on float rounding.
//   - Same source (SameWorkout, FoldClusters): copies of one recording that a
//     source re-wrote under a new HealthKit UUID; the store deletes all but one.
//   - Different sources (CrossSourceDuplicates): nothing is deleted; scoring
//     marks every copy but the primary with duplicate_of (D147).
//
// See docs/SCORING.md → Several sources, one reading; D146, D147.
package workoutdup
