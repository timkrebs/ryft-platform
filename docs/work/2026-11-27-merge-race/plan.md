# RFT-88: close the account-merge race (2026-11-27)

Branch `fix/merge-race` from `origin/main` 93a7e8d, worktree `.worktrees/merge-race`. One PR, not merged here.

## The race
`MergeUsers` takes only `lockUser` for both accounts. The handler then commits, rescores, publishes and calls
`eraseAccount` → `DeleteUser` in a **second** transaction. In that gap the source account `from` is still a live user
(row present, no tombstone, token valid): `EnsureUser` passes (fast path or slow) and a sync, journal or Train write lands
on `from`, and the erase then deletes it. A manual entry is lost.

A second gap sits inside the merge itself. `copyRows` (sessions and their children, journal days and entries) reads a
snapshot without row locks. `PutJournalDay` on a day that already exists does `INSERT … ON CONFLICT DO UPDATE` +
`DELETE` + `INSERT` of entries. Its FK checks hit `journal_days`, not `users`, so it takes no lock on the `users` row
(an upsert that updates also runs no FK check). An in-flight save of an existing day is not blocked by
`users … FOR UPDATE`. The merge copies the old version, and the new entries are erased with `from`. The same applies to
a session save on an existing session.

## Fix (T1, backend-engineer)
In `store.MergeUsers`, one transaction:
1. `lockUser` for both accounts, then `lockApply` for both, each pair in uuid-string order (as now). `lockApply` waits
   for a sync / HealthKit write / fold / consent change in flight on either account.
2. The existence checks (`ErrFromNotFound` / `ErrToNotFound`) as now.
3. `SELECT 1 FROM users WHERE id = from FOR UPDATE`, for the real run only. It waits for every in-flight insert that
   references `from` (key-share through the FK). A later insert blocks until commit, then fails its FK check (23503).
4. Lock `from`'s rows in the tables the merge **copies** (`sessions`, `session_exercises`, `session_sets`,
   `journal_days`, `journal_entries`) with `SELECT … FOR UPDATE` before reading them, again for the real run only.
   This waits for an in-flight update of an existing row. Read Committed then gives the next statement the committed
   data, and a later update blocks until commit and finds the row gone. Tables moved with `UPDATE … SET user_id`
   already serialise on the row (re-checked after the wait).
5. `m.run` as now.
6. Real run: `DeleteUser`'s erasure (tombstone, Apple token to the tombstone, connection tokens to the deregistration
   queue, every delete), factored out of `DeleteUser` into `eraseUser(ctx, tx, id)`, run in the **same** transaction.
   Then commit. `DeleteUser` = `lockUser` + `eraseUser`, behaviour unchanged.

Handler `cmd/api/merge.go`: after the commit, run the rescore, the records and the publish as now, then only the
post-commit part of `eraseAccount` (cache forget, Apple revocation, Supabase auth user, connection end). Factor it as
`afterErase(ctx, id)`, which `eraseAccount` also uses. The "data is moved, deleting failed (run the merge again)" 500
goes away: the erasure is atomic with the move, and Apple/auth are retried by the reconciler.

## What a late writer sees (no API change)
- Request whose `EnsureUser` runs after the commit: the row is gone, so `EnsureUser` takes the slow path, finds the
  tombstone and returns `ErrDeleted` → `401` (reason `deleted`), as after `DELETE /v1/me`. The 5-minute fast path needs
  the row and cannot recreate the user.
- Request in flight across the commit: an FK violation → `500 internal error`, the same as the `DELETE /v1/me` race
  today. The app retries the 5xx and gets the `401`.
- A HealthKit sync or check-in with a grant waits on `lockApply`. After the commit, `requireGrant` finds the consents
  erased and the app gets `consent_required` (review finding 2). It may show the consent screen briefly before the next
  request's `401`. No data is lost.
- The app sees no new status or `code`, so API.md stays unchanged. Before the fix the late writer got a `2xx` and its
  data was erased.
- A deadlock with scoring's `ApplySessionPass` is possible (review finding 1). Postgres aborts one side; the merge
  retries once on 40P01.

## Acceptance
- New store tests (Postgres): a sync (`UpsertSamples` in an open tx holding `lockApply`) and a journal write
  (`PutJournalDay` on a day `from` already has, held open) racing the merge. Each either commits before the move (and
  its rows are on `to` after) or fails after the commit; never erased. A write after the merge fails (sample, journal
  day). `EnsureUser(from)` after the merge returns `ErrDeleted`, even with a fresh `last_seen_at` before the merge.
- `TestMergeUsers` adapted: from is erased by the merge itself (owned = 0, tombstone present, `EnsureUser` →
  `ErrDeleted`). The dry run still erases nothing.
- Existing delete tests green; `go vet`, `go test ./...`, lint; CI's Postgres job.
- D302 (lock order: lockUser pair, then lockApply pair, uuid order; merge and erase in one tx). No open PR claims D3xx
  (checked 2026-11-27).

## Verify
code-reviewer, one pass on the diff → `review.md`.

## Risks / rollback
- Longer merge transaction (it now also deletes `from`): bounded by `deleteTimeout` (2 min). An admin-only route that
  is rarely used.
- Deadlock with a connection confirm that takes `lockApply` on two accounts in another order: Postgres detects it and
  aborts one transaction. The merge already refuses an account with connections.
- Rollback: revert the PR. No migration.

## Needs the user
Nothing to run. Merge when green. `deploy.yml` rolls `api` (no migration).
