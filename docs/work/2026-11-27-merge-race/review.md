# Review: RFT-88 (code-reviewer, 2026-11-27, pass 1, uncommitted diff)

No blockers. The reviewer traced the code by reading it; there was no Postgres locally, so no store test has run.

## Should fix
1. **A deadlock with scoring's `ApplySessionPass` is possible** (`train.go:932`). It updates `sessions` first, then
   inserts `workouts`, which takes a key-share on `users`. The merge locks `users` first, then the `sessions` rows:
   each side waits for the other. Postgres detects the cycle and aborts one side, usually the merge (40P01 → 500).
   Nothing is lost. Fix: retry MergeUsers once on 40P01; D302 to say deadlocks are possible and detected.
   → backend-engineer.
2. **The late writer's outcome is misdescribed.** A HealthKit sync or check-in with a grant waits on lockApply. After
   the commit, `requireGrant` finds the consents gone, so the app gets `consent_required`, not a 500 then 401. Fix:
   document it (D302, plan); optionally add a test. → backend-engineer (D302); plan updated by the lead.

## Nits
3. D302: "a connection confirm in the other order" is wrong. The confirm sorts with `bytes.Compare`, the inbox uses
   `ORDER BY user_id`, and both match. → fix the wording.
4. A rescore failure after the commit is a 500 that doesn't say the merge happened, and a rerun answers 404. Same gap
   as before the change. → error text names it.
5. `merge_race_test.go:205`: the name says lockApply but `UpsertSamples` takes none. → rename.
6. Race tests assert only `err != nil`. → assert SQLSTATE 23503.
7. The nutrition and connection checks run outside the merge transaction (true before this change too). The nutrition
   tables cascade on `users` delete, so a nutrition write in the gap is erased. The risk is low: nutrition is dark and
   flag-gated. → **follow-up, not in this PR** (move the checks under the merge's locks).

## Sound (per reviewer)
In-flight saves of existing journal days and sessions; moved tables; `EnsureUser` fast path; lockUser always first;
`eraseUser` = old DeleteUser; response shape; dry run; test 3 polls `pg_stat_activity` instead of sleeping.

## Pass 2
Items 1–6 fixed by backend-engineer. The lead checked the diff: `MergeUsers` retries `mergeUsers` once on 40P01,
and the first transaction's deferred rollback runs before the retry. `TestIsDeadlock` covers the error check; the retry
itself has no test against a real deadlock. D302 rewritten. The logged rescore error names the merge. Late writers
are asserted as 23503. Item 7 left as a follow-up. No Postgres test has run locally; CI is the first run.
