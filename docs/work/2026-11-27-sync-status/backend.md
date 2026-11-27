# The sync status: the backend PR (D297, D298)

Branch `feat/sync-status`, worktree `.worktrees/sync-status-backend`, from `main` at #258. The outline is
`implementation.md`; this file fixes what the outline left open and the interfaces the tickets share.

## User answers (2026-11-27)
1. N2R morning baseline, coaching briefing, pilot partner deliveries: **not tracked** (as the contract says).
2. Withdrawn consent: scoring **closes** the queue rows of accounts without current consent, with a reason.
3. Offline is the app's concern.

## Migration `0065_sync_status.sql` (database-engineer)
Main ends at 0061; #259 holds 0062/0063, #260 holds 0062–0064, so this PR takes 0065.
- `GRANT SELECT (received_at) ON provider_inbox TO ryft_api;` (the api still cannot read `payload`, `record_id`)
- `CREATE INDEX IF NOT EXISTS provider_inbox_user_pending ON provider_inbox (user_id, provider, received_at) WHERE state = 'pending';`
- `ALTER TABLE score_queue ADD COLUMN IF NOT EXISTS closed_reason text CONSTRAINT score_queue_closed_reason_check CHECK (closed_reason IN ('no_consent'));`
  Meaningful only together with `scored_at`: set with `scored_at` when scoring closes a row without scoring it,
  cleared by `MarkScored` when a day is really scored. A re-queue (`scored_at = NULL`) may leave a stale value
  on a pending row; every reader asks `scored_at IS NULL` first. So ingest's and the api's queue upsert do not
  change (no grant for them, no golden trace change).
- `GRANT UPDATE (closed_reason) ON score_queue TO ryft_scoring;`

## Interfaces (backend-engineer builds, test-engineer tests)
`internal/model/sync.go`:
- `SyncSource{Source string "source"; LastSyncAt *time.Time "last_sync_at"; Status string "status"; Reason *string "reason"}`
- `SyncStatus` gains `Sources []SyncSource "sources"`, `Freshness string "freshness"`, `ScoresPendingSince *time.Time "scores_pending_since"`
- consts `SyncSourceHealthKit = "healthkit"`, `SyncSourceOK/Pending/Error`, `FreshnessCurrent = "current"`, `FreshnessScoresPending = "scores_pending"`, `SyncReasonNeedsReauth = "needs_reauth"` (consent/invite reuse `CodeConsentRequired`/`CodeInviteRequired`)

`internal/store/syncstate.go`:
- `type PendingDay struct{ Day string; QueuedAt time.Time }`; `SyncState.Queued []PendingDay` filled by the same query as `Pending` (which stays `[]string`)
- `type SyncConnection struct{ Provider, Status string; LastDataAt, PendingSince *time.Time }`
- `(*Store).SyncConnections(ctx, userID) ([]SyncConnection, error)`: the outline's query, `connected`/`needs_reauth`, by provider
- `const QueueClosedNoConsent = "no_consent"`; `(*Store).CloseQueue(ctx, userID, reason string) (int64, error)`: `UPDATE score_queue SET scored_at = now(), closed_reason = $2 WHERE user_id = $1 AND scored_at IS NULL`
- `MarkScored` also sets `closed_reason = NULL`; `ExpectedScoreSec` counts only `closed_reason IS NULL`

`cmd/api/syncstatus.go`:
- `syncFreshness(days []store.PendingDay, today string, now time.Time) (pending bool, since *time.Time)`: pure; a day
  pending when `day ∈ {today, yesterday}` (any age) or (`day ≥ today − 30` and `queued_at > now − 10 min`)
- `(h *handler) syncSources(ctx, u, st) ([]model.SyncSource, *time.Time, error)`: healthkit first, then the
  providers that `h.conn.served(ctx, provider, u.ID)` admits (no providers when `h.conn` is nil or not serving); returns the earliest pending `received_at`
- `freshness = scores_pending` ⇔ days pending ∨ a source `pending`; since = the earliest of both; `null` when `current`

`cmd/scoring/main.go`: on `ErrNoConsent` in `handleEvent`, and for a non-admitted user in `refreshActive`,
`CloseQueue(ctx, user, store.QueueClosedNoConsent)`; a failure is logged, the event still acked (D78).

## Cost per uncached call
Before: 4 queries. After: 5 (`SyncConnections`: the `connections` primary key, one probe of
`provider_inbox_user_pending` per listed connection, at most 2 today). `PendingDays`' query gains a column on
the same `score_queue` primary-key range (the account's unscored rows, pruned a day after scoring). Behind the
10 s per-user cache, unchanged.
