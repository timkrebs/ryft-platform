# No-watch recovery: the implementation PR (platform-lead, 2026-11-27)

Brief: the head-architect's "No-watch recovery: the backend, against the merged contract (#255)". Contract:
`docs/API.md` → Recovery without a watch; decisions D291–D295; outline: `implementation.md` (this folder).
Branch `feat/no-watch-recovery` from `origin/main` 7f096b4, worktree `.worktrees/recovery-backend`.

**Numbers.** Migrations from **0062** (`0061` reserved for the BLS import). Decisions for judgement calls made
while implementing: **D296–D299** (D290 is #252's; nothing open takes D296+). Model version **2026.11.1**
(main has 2026.10.3; the contract's examples use 2026.11.1).

## Tickets

| # | Owner | Files (one writer each) | Done when |
| --- | --- | --- | --- |
| A | scoring-scientist | `internal/scoring/checkin*.go`, `version.go`, `changelog.json`; `internal/energy` (sRPE load); `internal/periodization` (check-in readiness, `LOW_RECOVERY_UNEXPLAINED`, the protective plan for it and for `journal_sick`; `readiness_display`); `docs/SCORING.md` | the pinned cases (29, 93, z −1.85/32, 4 check-ins → no personal part, duration curve, regularity, renormalisation, RPE 151/143/157, 23, readiness table, soreness only a reason) pass; golden files don't move except what the version bump forces (the guard's rule) |
| B | database-engineer (parallel with A) | `internal/db/migrations/0062–0064`, `internal/db` tests | `recovery_checkins`, `workout_ratings`, the flag seed; RLS and grants like `journal_entries`; migrate-up test green on Postgres |
| C | backend-engineer (after A and B) | `internal/model`, `internal/store`, `cmd/api`, `docs/API.md` (http → typed json blocks only), `scripts/smoke.sh`, `scripts/check_api.py` | the routes, flag/consent gates, wearable-night rule, precedence, N2R `bedtime_source`, confounders, readiness wiring, load, deletion/export/merge, fold/re-attach move ratings; handler and store tests for the brief's cases; `make test` and the doc/golden checks green |
| R | code-reviewer (read-only, after C) | | findings in `review-impl.md`; blockers fixed by their owner; re-verified |

## Risks
- The golden guard: a `ModelVersion` bump requires the manifest's version to match → the files get regenerated
  with only `model_version` moving (`GOLDEN_MODE`/the guard's version-only path). Needs `RYFT_TEST_POSTGRES_URL`.
- The flag as a kill switch touches every read (status, readiness, load, N2R bedtime): one helper in store.
- `workout_ratings` FK `ON DELETE CASCADE`: the fold and the session re-attach move the rating before they delete.

## On merge (`deploy.yml`)
The migrate job runs 0062–0064; the api and scoring roll (model version). The flag is at 0 %: nothing changes.

## Needs the user (after merge, not before a published privacy version names the check-in)
Allow-list a tester for `recovery.checkin` (OPERATIONS.md → Feature flags). Rollback: the flag off.
