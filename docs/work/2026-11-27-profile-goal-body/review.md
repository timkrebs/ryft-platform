# Review: `me: goal, sex and weight on the profile` (D306)

One code-reviewer pass on commit 1 of `feat/profile-goal-body` (2026-11-27). No blockers.

| # | Severity | Finding | Outcome |
| --- | --- | --- | --- |
| 1 | should-fix | `{"sex":null}` / `{"weight_kg":null}` need a naming consent too (the contract says so). A user who set them under version V1 and then withdrew, or moved to a V2 that does not name them, cannot clear them without deleting the account (GDPR Art. 7(3), 17). | **Deferred, decide before a policy names the data.** Not reachable today: the set is empty, so nobody can store a value. It changes the contract (relaxing a 403), so it goes to api-contract-owner with the privacy update / PR 3. Options: clears without consent, or `RevokeConsent` clears sex and the manual weight. Recorded in D306. |
| 2 | should-fix | `profileFail` replaces every store error with one string, which leaves no SQLSTATE or constraint name for a 500 on `/v1/me`. | **Fixed:** logs `pgconn.PgError` code and constraint name (they hold no values), otherwise the error type. |
| 3 | should-fix | While no version names the data, the documented goal-step body (`goal`+`sex`+`weight_kg`) is 403 for everyone. The generic `consent_required` handling (show consent, re-send) loops, and `goal` is never saved. | **Fixed in docs:** the PATCH section says the app sends `sex`/`weight_kg` only after the privacy version that names them is current and consented, sends `goal` on its own before that, and must not loop on this 403 when the consent already equals the current version. |
| 4 | nit | `HealthKitWeighIn` does not filter `unit='kg'` and 25–350, unlike `coaching.go`. | **Fixed:** filter added. |
| 5 | nit | API.md still says "the operator sets which versions" and the Swift mirror comments still say "not served yet". | **Fixed.** |
| 6 | nit | Migration 0068 takes ACCESS EXCLUSIVE on `users` briefly (ADD COLUMN with CHECK on nullable columns, so no rewrite); `lock_timeout` 5 s. | **Noted** in the PR body. |
| 7 | nit | No smoke check for the five keys. | **Skipped**: handler tests cover it, and the next smoke change can add it. |
| 8 | nit | The migration rerun test does not assert that no duplicate constraints were created. | **Skipped:** `ADD COLUMN IF NOT EXISTS` skips the constraint with the column. |

The reviewer verified: `user_id` scoping, lock order (no deadlock), the Read Committed re-check, the UPDATE parameters, the grants, the tri-state decoding, and that no other route serves the weight fields.
