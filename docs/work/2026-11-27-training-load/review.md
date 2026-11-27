# Review: training load on read (D308, branch `feat/training-load`)

## The lead's review of the Codex draft (before code-reviewer)

Codex (one `--fresh --effort high` run, 13m 45s) drafted the model types, `internal/scoring/trainingload.go`,
the store reads, the handler, the tests and the docs. Behaviour issues found and fixed by the lead:

1. **Kill switch broken (blocker).** Ratings counted in the Fitness load whatever `recovery.checkin` said, and
   the "Recovery without a watch" section of `docs/API.md` was rewritten to say so ("Fitness training load uses
   stored ratings independently"). The flag is the kill switch for counting ratings anywhere (D298, D299), and no
   published privacy version names the rating yet. Fixed: the handler reads `RecoveryEnabled` and skips the rated
   workouts while it is off; the sentence is restored; a flag-off case is in the api test.
2. **Rated workouts dropped on a date without a score row.** The no-watch day is the case the rating exists for.
   Fixed: they count (load and data day) on any date of the window; the test pins it (Oct 23 in the fixture).
3. **A 25-line DST helper** for the workout read bounds. Replaced by a one-day pad on each side plus a filter on
   the local date key.
4. **Contract text softened**: "It is not `CoachingBriefing.training_load` … must not be shown as one" was cut to a
   neutral sentence. Restored.

## code-reviewer (one pass): no blockers

| # | Finding | Severity | Outcome |
| --- | --- | --- | --- |
| 1 | One heart-rate query per rated workout without `avg_hr`, on an uncapped range; the lookback follows the request's `from`/`to`, not the rows | should-fix | fixed: range clamped to the returned rows; one batched sample read for all candidates |
| 2 | A rated Train session's workout with no heart rate counts twice: muscular (its sets) and cardio (the rating) | should-fix | fixed: a workout of a Train session (`session_id` set, or the session source) adds no rated cardio TRIMP and makes no cardio data day; D308, SCORING.md, API.md |
| 3 | `from=0001-01-01` now 500 (`0000-12-05` as `::date`) | nit | fixed by the clamp |
| 4 | Untested: the drop of a padded workout before the lookback; an api row with `days_with_data ≥ 21` | nit | first: test added; second: covered by the pure tests (left) |
| 5 | Changelog "(`days_with_data` of 21)" ambiguous | nit | fixed |
| 6 | Two short "not the coaching load" warnings dropped (DailyScore row, RPE section) | nit | restored |
| 7 | `shape` annotation on a plain JSON block | nit | left: the same form as the other typed blocks |
| 8 | Test comment says the pure test crosses Berlin's DST end; the pure function is UTC-only | nit | fixed |

Checked and sound per the reviewer: windows, ÷4, rounding and state from rounded values, the 21-day rule, the
data days, the usable-heart-rate rule (no double count), no `sex`, the placeholder `null`, user scoping of all four
queries, error strings without values, the calibration (RPE 3/5/8/10 → 2.9/24.3/115.1/261.9), and the api test's
expected numbers recomputed by hand.
