# #260 — Codex adversarial review (static, vs origin/main): second pass, 2026-11-27

Verdict was needs-attention. All six findings were checked against the code and confirmed. Each one is fixed with a test.

| # | Sev | Finding | Owner | Fix | Tests |
|---|-----|---------|-------|-----|-------|
| 1 | high | Recovery writes commit after a consent revoke | backend-engineer | `PutCheckin`/`PutWorkoutRating` take the admitting grant and revalidate it in the write tx under the consent lock (`lockGrant`/`requireGrant`, `store/healthkit_sync.go`); the handler answers `consent_required` | `TestRecoveryWritesRefuseARevokedGrant`, `TestRecoveryWritesWaitForAnInFlightRevoke` (store, ryft_api); `TestRecoveryWritesRefuseAConsentRevokedAfterAdmission` (cmd/api) |
| 2 | high | Frozen coaching plans bypass the kill switch | scoring-scientist | `daily_energy_states.plan` also stores a `recovery_off` plan when a check-in moved it; flag-off readers and the stored night restore it; capture computes projection, evaluation and calibration with the flag off (no schema change, jsonb) | `TestFrozenPlanDoesNotOutliveTheKillSwitch` |
| 3 | medium | A scoring retry strands a rating | database-engineer | `Store.ApplySessionPass`: attach, session load, rating moves and release in one tx, from the pre-pass mapping | `TestSessionPassRollsBackAndRetries`, `TestSessionPassSwapKeepsBothRatings` |
| 4 | medium | Reconcile discards an accepted rating | scoring-scientist | `wearables.Reconcile` carries a dropped copy's RPE to the kept copy; measured HR/power still wins | `TestReconcileCarriesTheRating` |
| 5 | medium | Scoring timestamp grant too wide | database-engineer | 0065 changed in place (unmerged, unapplied anywhere): the column grant and trigger go; `scoring_follow_session_times(user, workout)` (SECURITY DEFINER, EXECUTE to ryft_scoring only) derives the times from the owner's completed session | `TestMigrateWorkoutDuplicates` (grant, function, rerun), `TestEnsureSessionWorkoutTimesNeedTheCompletedSession` |
| 6 | medium | Rejected recovery requests log raw date and id | backend-engineer | `redactNutritionPath` → `redactPrivatePath`, the outermost wrapper, now also covers the recovery check-in path and `/v1/workouts/{id}/rpe` | `TestPrivateLogPath`, `TestRecoveryRejectionsLogNoDateOrWorkoutID` |

Local: `go vet ./...` and `go test ./...` are green. No Postgres was reachable locally, so the integration tests run in CI's `test` job only.

## Code review of the fix diff
code-reviewer read the fix commit statically and found no blockers. It confirmed the lock order, the chain and cycle logic of the rating moves, single counting in Reconcile, SECURITY DEFINER hygiene, the redaction, and that the CI `test` job sets `RYFT_TEST_POSTGRES_URL` (ci.yml:220).
- should-fix: `coaching_eval.go:128-205`: adherence and availability are measured against the shown plan even with the flag off. Use `recovery_off` there too. → scoring-scientist.
- nit: `store/doc.go:13,18`: the pool note on `ReleaseSessionWorkouts` and the method counts are stale.
- nit: `cmd/api/nutrition.go:56`: `/v1/workouts/{id}/rpe/` (trailing slash) logs the raw id. → backend-engineer.
- nit (left alone, outside the scope): `EnsureSessionWorkout`/`ReleaseSessionWorkouts` are now called only from tests.
