Fixes RFT-88: an account merge could erase writes made to the source account during the merge.

## The race
`store.MergeUsers` took only `lockUser`. The handler then erased the source account (`from`) in a **second**
transaction, after the rescore and the publish. In that gap `from` was still a live account, so a sync, journal or
Train write could land on it, return 2xx, and be erased with the account. Manual entries were lost.

Inside the merge, the copy of sessions and journal days read without row locks. A save of an existing journal day or
session takes no lock on the `users` row, so the merge could copy the old version.

## The fix
- `MergeUsers` locks in this order: the `lockUser` pair, then the `lockApply` pair (uuid-string order), then from's
  `users` row and its rows in `sessions`, `session_exercises`, `session_sets`, `journal_days` and `journal_entries`
  `FOR UPDATE` (real run only).
- `DeleteUser`'s body is now `eraseUser(ctx, tx, id)`. The merge runs it in the **same** transaction as the move.
  `DeleteUser` behaves as before.
- Handler: after the commit, `afterErase` (cache, Apple revocation, Supabase auth user, provider grants), then the
  rescore and the publish. The "data is moved, deleting failed" 500 is gone.
- A deadlock with scoring's session pass (it updates `sessions`, then inserts a workout) is possible. Postgres aborts
  one side, and the merge retries once on 40P01.
- D302.

## What a late writer gets (no API change)
- A request whose auth runs after the commit: `401` (reason `deleted`), as after `DELETE /v1/me`. `EnsureUser`'s
  5-minute fast path needs the user row, so it cannot recreate `from`.
- A write in flight across the commit: an FK violation → `500`, then `401` on retry (the same as the `DELETE /v1/me`
  race).
- A HealthKit sync or check-in with a grant: it waits on `lockApply`, then gets `consent_required`, because the
  consents are erased. The app may show the consent screen briefly before the next `401`.

No new status or `code`, so `docs/API.md` is unchanged.

## Tests
New, in `backend/internal/store/merge_race_test.go`: `TestMergeWaitsForASyncInFlight`,
`TestMergeWaitsForAJournalSaveInFlight` (a save of an existing day), `TestMergeFailsAWriterThatWaitsForIt` (an
existing-day save, a new-day save and a sample write; each must fail with 23503 and leave nothing),
`TestMergeLeavesFromDeleted`, `TestIsDeadlock`. Adapted: `TestMergeUsers`, `TestAdminMerge`.

Locally: `go vet`, `go test ./...`, `-race` on `cmd/api` and `internal/store`, `make lint` all green. There was no
local Postgres, so the store tests were skipped. **CI's Postgres job is their first run.**

Review: `docs/work/2026-11-27-merge-race/review.md`. One code-reviewer pass, no blockers; findings 1–6 fixed.
Follow-up (not here): the nutrition and connection checks run before the merge transaction (finding 7).

## On merge
Once `ci-ok` is green on main, `deploy.yml` (unless `DEPLOY_HOLD_SERVICES` is set) does this:
1. It builds and scans the images.
2. It runs the migrate job. There is no new migration, so this is a no-op.
3. It replaces scoring, notify, ingest and api, in that order.
4. It runs `make smoke-prod`. A failure rolls the services back automatically.

The behaviour change is in api alone. `web/` is unchanged, so the `site` job deploys nothing.

## Needs you
Nothing to run.
