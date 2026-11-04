// Package scoring owns the Recovery, Strain, Sleep and Readiness model and the
// Engine that applies it to stored data and writes the daily rows.
//
// It must not delete raw rows (the scoring role cannot, D146) or write a stored
// number under an old ModelVersion.
//
// Only the formulas are pure (algorithms.go, sources.go, calibrating.go,
// trends.go, maxhr.go, version.go and others take loaded values and return
// values). compute.go, sleep.go, strength.go and workout.go import the store,
// so the Engine does I/O and its tests need Postgres.
//
// Invariants:
//   - Any change that moves a stored number bumps ModelVersion, adds a
//     changelog.json entry, updates docs/SCORING.md and plans a rescore.
//   - Sources are de-duplicated per hour and per night, and workouts by the
//     rules in scoring/workoutdup (docs/SCORING.md → Several sources).
//   - The calibration constants are pinned by the package tests, and six
//     synthetic users' stored output by the golden fixtures (D149).
//   - A change in a day's history re-scores the days after it (D112).
//
// See docs/SCORING.md → Model version; docs/ARCHITECTURE.md → Scoring; D3, D13,
// D40, D41, D80, D112, D146, D147, D149.
package scoring
