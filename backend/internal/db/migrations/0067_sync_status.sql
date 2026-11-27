-- The sync status (docs/DECISIONS.md D297, D301; docs/work/2026-11-27-sync-status/backend.md). GET /v1/me/sync
-- tells the app per source whether the account's data is current, and whether the scores are still pending.
-- Two things it needs from the database:
--   - The api reads when a provider's delivery arrived (provider_inbox.received_at of a row still pending), so
--     it can say "a delivery is waiting since ..." without ever reading a payload or a record id.
--   - Scoring closes the queue rows of an account without current consent (it will not score them, D78): it
--     sets scored_at and a reason, so the app is not told "scores pending" for days that will never be scored.
--
-- Changes:
--   - GRANT SELECT (received_at) ON provider_inbox TO ryft_api. Column grants add up: the api reads id, user_id,
--     provider and state (0042) and now when a row arrived; payload and record_id stay unreadable to it.
--   - provider_inbox_user_pending (user_id, provider, received_at) WHERE state = 'pending': the api's probe
--     "the earliest pending delivery of this account and provider" is one index descent per connection. Only the
--     pending rows are in it (the inbox's other states are most of the table, 30 days of dedupe keys), so it is
--     small and stays small: a row leaves it when it leaves 'pending'.
--   - score_queue.closed_reason, NULL or 'no_consent' (a closed set; a new reason is a new migration). It means
--     something only together with scored_at: set with scored_at when scoring closes a row without scoring it,
--     cleared by MarkScored when a day is really scored. A re-queue (scored_at = NULL) may leave a stale value on
--     a pending row, so every reader asks scored_at IS NULL first; that is why ingest's and the api's queue
--     upsert do not change (no new grant for them, no change of the golden trace).
--   - GRANT UPDATE (closed_reason) ON score_queue TO ryft_scoring, next to its UPDATE (scored_at) of 0025.
--
-- Nothing new for RLS: provider_inbox_services (0042) and score_queue_services (0025) cover the roles and
-- commands already, and both tables have RLS on in the Supabase shape. No new table, so nothing new for
-- DELETE /v1/me (score_queue's rows are deleted with the account as before; the column is one more value of the
-- same row) or for the export (the queue is bookkeeping, not user content) or a retention (score_queue rows are
-- pruned a day after scoring, as before).
--
-- Additive: one column, one index, two grants; nothing renamed, retyped or dropped. Rollback = re-pin the old
-- images; the column, the index and the grants stay and nothing old reads or writes them. An old scoring image
-- never sets closed_reason, an old api never reads received_at.
--
-- Locks (the migrator runs the file in one transaction, db.go, so every lock is held to the commit):
--   - provider_inbox: CREATE INDEX (not CONCURRENTLY, which a transaction forbids) takes SHARE: ingest's writes to
--     the inbox wait while it builds. The inbox holds rows only for accounts with a direct connection (dark behind
--     connect.garmin, rollout 0 % at the time of writing), so the build scans next to nothing. lock_timeout bounds
--     the wait to get the lock (D116).
--   - score_queue: ADD COLUMN with no default is a catalog change under a short ACCESS EXCLUSIVE; the CHECK on
--     the new, all-NULL column passes by construction, and the table holds only the unscored rows and the last
--     day's scored ones (pruned), so even a scan to prove it would be small.
SET LOCAL lock_timeout = '5s';

-- ---------------------------------------------------------------------------------------------------------------
-- provider_inbox: when a delivery arrived, readable by the api.
-- ---------------------------------------------------------------------------------------------------------------
GRANT SELECT (received_at) ON provider_inbox TO ryft_api;

CREATE INDEX IF NOT EXISTS provider_inbox_user_pending ON provider_inbox (user_id, provider, received_at)
    WHERE state = 'pending';

-- ---------------------------------------------------------------------------------------------------------------
-- score_queue: why a row was closed without being scored.
-- ---------------------------------------------------------------------------------------------------------------
-- IF NOT EXISTS skips the whole clause on a rerun, constraint included.
ALTER TABLE score_queue
    ADD COLUMN IF NOT EXISTS closed_reason text
        CONSTRAINT score_queue_closed_reason_check CHECK (closed_reason IN ('no_consent'));

GRANT UPDATE (closed_reason) ON score_queue TO ryft_scoring;
