# Review: the no-watch recovery implementation, PR #260 (code-reviewer, 2026-11-27)

Reviewed 6a32b51 + 808a65c (`git diff origin/main...HEAD`), read-only, recorded on arrival. The third column says
what the follow-up did.

| # | Rank | Finding | Fix |
| --- | --- | --- | --- |
| 1 | Blocker | `lint`: `store/recovery.go:81` `wearableNight` unused (the rule runs via `wearableNightSQL`). | |
| 2 | Blocker | `web-checks`: `web/content/science/en.mdx` names model `2026.10.3`; the web test ties it to `ModelVersion`. | |
| 3 | Blocker | `pooler` red (logs locked while the run was in progress); `test`/`smoke` pending. | |
| 4 | Should fix | A rating is dropped when scoring recreates a session's own workout on a time edit (`train.go:919-944`, `scoring/strength_day.go:55-65`: DELETE + INSERT under the same id, the FK cascades); and a re-attach from one HealthKit workout to another (or back) leaves the rating behind. Hits exactly the no-watch Train user. | |
| 5 | Should fix | `nutrition_score.go:81`: `NutritionDaily`'s meal-timing score uses check-in bedtimes; the contract puts them into N2R only. | |
| 6 | Should fix | `n2r_calibration.go:111`: `refreshN2RNight` can persist an N2R night computed with a check-in bedtime (HRV night without measured sleep start), which survives the kill switch. | |
| 7 | Should fix | `0063`: ingest gets table-wide SELECT (incl. `session_rpe`) and DELETE; the fold only repoints. Column SELECT, no DELETE. | |
| n1 | Nit | `recovery.go:162` confidence: a correlated HR-hours count per check-in; heavy on a 400-day export. | |
| n2 | Nit | Merged check-ins aren't rescored against the target's history. | |
| n3 | Nit | Races: PUT rpe vs the fold; the move and the delete in `ReleaseSessionWorkouts` outside one transaction. | |
| n4 | Nit | One briefing reads the flag ~4 times. | |

**Fix commit 9bfbe54:** 1 removed; 2 science page 2026.11.1; 3 was `TestIngestRoleDeletesOnlyRawData` (item 7),
`TestRemoteConfig` (the new flag) and `TestSessionRPERoutes` (a reused decode struct); 4 time edit in place, the
re-attach moves from the previous attachment, in one transaction (n3); 5 and 6 read without check-in bedtimes;
7 ingest column SELECT/UPDATE, no DELETE; n1 left (already one query per read); `wearableNightSQL` now also
reads `sleep_segments` (asleep/core/deep/rem), as API.md says.

**Re-verification (code-reviewer, 9bfbe54):** every item above fixed; load columns re-scored in the same pass;
goldens `model_version` only; trace golden right; the segment lookup matches scoring's night window and is one
index probe. Two new should-fix items:

| # | Rank | Finding | Fix |
| --- | --- | --- | --- |
| r1 | Should fix | `0063:62`: scoring's `UPDATE (start_at, end_at) ON workouts` is guarded for `ryft-session` only by the code; 0011 guards scoring's DELETE/INSERT with restrictive row policies. | 7bdfd1f: a BEFORE UPDATE OF start_at, end_at trigger refuses `ryft_scoring` on non-`ryft-session` rows (a restrictive policy would block the load-column updates); test in `migrate_duplicates_test`. |
| r2 | Should fix | `strength_day.go:71` + `train.go:953-972`: `SetSessionLoad` saves the new attachment before the move's transaction; a failed pass that retries sees the new workout as `previous`, and the rating stays behind. | 7bdfd1f: the move also takes ratings off any workout still linked to the session that isn't chosen; `TestReleaseSessionWorkoutsRetryMovesTheRating`. |

Main took D296/D297 during the run (#257, #258): this work's decisions are **D298** and **D299** (merged main in f9c7030).

None found: status codes and gates; nullability/omitempty and closed enums; the kill switch (status, briefing,
readiness, load, N2R bedtime, `n2r_bedtime_applied`); one wearable-night rule; upsert and the locked 14-day
rescore; tz/wake-day validation and 04:00; migrations' idempotency and RLS; the composite FK with merge, fold,
re-sync and deletion; deletion/merge coverage; the trace golden; logging; scoring goldens move in
`model_version` only; TRIMP constants and day strain unchanged.

API.md (api-contract-owner, same pass): markers now "served from 2026-11-27 behind `recovery.checkin`", old
anchor kept, changelog row 2026-11-27, Swift mirror matches. `make check-swift` needs the running stack (not run).
