# Review: the no-watch recovery contract (code-reviewer, one pass, 2026-11-26)

Reviewed commit 8dff064 (`git diff origin/main...HEAD`). Read-only pass, recorded here when it arrived. The second column says what the follow-up commit did.

| # | Rank | Finding | Fix |
| --- | --- | --- | --- |
| 1 | Blocker | API.md, the tiers: "`DailyScore.recovery` not null → `wearable`/`high`" is wrong. Recovery is computed from resting HR alone (`scoring/compute_steps.go:84-90`, `algorithms.go:148`, `compute_steps.go:384`). A watch worn by day writes resting HR every day, so its user is `wearable`/`high` while `needs_checkin` is true, and every check-in is `superseded` at once. The tiers, `needs_checkin` and readiness used three different rules. | One rule, the **wearable night** (asleep stages or overnight HRV from a wearable for the night ending on the day), now used by the tiers, `needs_checkin`, `superseded` and readiness. A day-only watch is the `medium` tier. |
| 2 | Should fix | "A night typed into Apple Health" can't be detected: `SleepSegment` has no `was_user_entered` (`model/sync.go:85-98`), and strict `/v1/sync` would answer `400`. | Clause dropped; listed as an open question (an additive sync field later). |
| 3 | Should fix | The briefing example can't happen: readiness and load come only with a `plan` (`n2r_guidance.go:90-91`); `n2r` is never null with a profile (:52-53); `acwr` is never null (`coaching.go:20`, ≥ 45 days, `adaptive.go:168`). | The example has a `plan`, an `n2r` and `acwr` 4.0; the text says readiness comes with a plan. |
| 4 | Should fix | D293's 0.28–0.32 TRIMP per AU is wrong: with RPE/10 as reserve it is 0.075·e^(0.1795·RPE), from 0.13 at RPE 3 to 0.32 at 8; a flat 0.30 overstates easy sessions up to 2.3×. | The flat factor is replaced by Banister's TRIMP with `session_rpe / 10` as the reserve fraction and the user's sex constants (`energy.Workout`). 60 min at 8 is 151 (unspecified sex), 143 (male), 157 (female). |
| 5 | Should fix | There is no request-path N2R recompute. The night record is written only by the tick, and only with overnight biometrics (`n2r_calibration.go:106-108`), which a no-watch user doesn't have. | Retroactive N2R is computed live (as the briefing's `yesterday_n2r` already is). `bedtime_source` and `n2r_bedtime_applied` make it explicit. "The existing recompute path" and the reason string are gone. |
| 6 | Should fix | "Every route answers 404 while the flag is off" contradicts "reads and DELETEs keep working". | Reworded: the writes and the status route. |
| 7 | Should fix | A `404` without a `code` on `PUT …/rpe` falls under the 404 row's "drop the local reference", so the app would drop a workout. | The section and the 404 row say: hide the feature; never drop the workout or other local data. |
| 8 | Should fix | API.md says "1.5 SD below the mean"; D292 uses z with an SD floor of 6 and 5 check-ins. | Aligned with D292. |
| 9 | Should fix | The HRV path also yields `UNKNOWN` with HRV but no prior CV (`availability.go:56-58`). | "When the HRV path yields `UNKNOWN`". |
| 10 | Should fix | `bedtime_source` has no value when `N2RScore.bedtime` is null. | Absent then. |
| 11 | Should fix | Whether stored check-ins and ratings still count while the flag is off is unspecified. | They don't (a kill switch); they stay stored and readable. |
| n1 | Nit | Changelog order: the new row sits above a 2026-11-23 row, with another 2026-11-26 row below it. | Left: the out-of-place 2026-11-23 row is older; the new row is newest-first. |
| n2 | Nit | "5 minutes in the future" vs "in the future" in the 422 list. | Aligned. |
| n3 | Nit | No rounding rule for `session_rpe_load`; `DELETE …/rpe` on another user's workout undefined. | Whole minutes × RPE, rounded; `404` when the workout isn't the user's (the app treats it as done). |
| n4 | Nit | Scoring can re-attach `Session.workout_id` (`store/train.go:721`), and the rating stays on the old id. | Outline: the re-attach moves the rating. |
| n5 | Nit | `ON DELETE CASCADE` conflicts with the fold moving the rating. | Outline: move before delete. |

None found: additive (`readiness` was never an enum, and the app switches on a String, `NutritionCoachingView.swift:335`); the Swift key mapping; the doc tests (all new blocks are `http`, `code` cells unchanged); the anchor; the arithmetic, apart from #3's ACWR; wording.
