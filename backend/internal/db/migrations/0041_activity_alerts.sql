-- ACTIVITY_SCORED, the sender (docs/DECISIONS.md D253-D255; docs/API.md → Push notifications →
-- "ACTIVITY_SCORED: the rules"; docs/work/2026-11-22-notifications-nt/activity-scored-design.md).
-- Notify decides each fresh, scored, visible Apple Health workout once, on the existing refresh event, by a
-- window scan (D253). Two facts the rules need and the data lacked, and one ledger:
--   - workouts.device_id: the install whose sync first stored the row (insert-only, ingest). Backfill is a
--     row stored < 1 h after that device's devices.created_at (D255). NULL for every row stored before
--     this migration, and NULL passes the rule.
--   - workouts.source_bundle_id: the HealthKit source's bundle id, until now input only. RYFT's own bundle
--     (dev.timkrebs.ryft and under it) never alerts. NULL passes. No CHECK: ingest's validate bounds it at
--     255 characters already, and adding a CHECK would scan workouts under the ACCESS EXCLUSIVE lock below.
--   - workout_alerts: one row per decided workout ("decided once" is a row, D254). The row keeps the copy's
--     source, activity and times, so a later copy (a fold's kept copy, a duplicate group's new primary)
--     inherits the mark by the fold rule; that is why workout_id has NO foreign key: the mark outlives a
--     folded or deleted copy. Notify prunes a user's rows 2 days after their end, under its per-user lock
--     7419008. Operational, like notifications_sent: DELETE /v1/me and the merge cover it (internal/store),
--     the app's export does not list it.
--
-- Grants:
--   - ryft_notify: SELECT, INSERT, DELETE and UPDATE (outcome, devices) on workout_alerts; the columns of
--     workouts the scan reads (no avg_hr, max_hr, energy_kcal, distance_m, trimp, zone minutes or load);
--     devices.id and devices.created_at for the backfill join (0015 gave it user_id and last_sync_at).
--     Its new policy workouts_notify_read is permissive, FOR SELECT, TO ryft_notify. The RESTRICTIVE
--     policies on workouts (0011: scoring's INSERT and DELETE; 0029: ingest's DELETE) name ryft_scoring
--     or ryft_ingest and are for INSERT or DELETE, so none of them applies to notify's SELECT.
--     devices_notify_read exists (0015).
--   - ryft_api: SELECT, DELETE and UPDATE (user_id) on workout_alerts: DeleteUser and MergeUsers.
--   - ryft_ingest, ryft_scoring: nothing new. Ingest's table-level INSERT, UPDATE on workouts (0009) covers
--     the new columns; scoring's column-level INSERT (0011, its ryft-session rows) leaves them NULL.
--
-- Additive: two nullable columns, one table, grants and two policies; nothing renamed, retyped or dropped.
-- Rollback = re-pin the old images; the columns and the table stay. The old ingest writes NULL device_id
-- (which passes the backfill rule) and no bundle, the old notify never reads the ledger. user_id cascades
-- on the users row (as 0033 and 0037): an api image from before this migration deletes or merges an account
-- without naming workout_alerts, and a ledger row must not make that fail. No down-migration.
--
-- Locks: the migrator runs the file in one transaction (db.go), so every lock is held to the commit.
-- CREATE TABLE with a foreign key to users takes SHARE ROW EXCLUSIVE on users (writes of users wait,
-- reads don't). ADD COLUMN without a default rewrites nothing but takes ACCESS EXCLUSIVE on workouts, and
-- so does CREATE POLICY on workouts; no statement scans a table (the new table is empty, no CHECK or index
-- is added to workouts). The GRANTs on workouts and devices change only the catalog. lock_timeout bounds
-- each wait (D116), written out as in 0027-0037.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS workout_alerts (
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The decided copy's id. No FK (above): the mark outlives the copy.
    workout_id uuid        NOT NULL,
    -- The copy's identity at decision, for the fold rule (same source and activity, both ends within 120 s).
    source     text        NOT NULL,
    activity   text        NOT NULL,
    start_at   timestamptz NOT NULL,
    end_at     timestamptz NOT NULL,
    -- The local day of start at decision (ryft.day): the cap's day, 3 a day.
    day        date        NOT NULL,
    -- sending: claimed, APNs not answered yet (counts toward the cap); sent, failed: after APNs;
    -- capped, muted, quiet, no_token, backfill, inherited: decided without a send.
    outcome    text        NOT NULL CONSTRAINT workout_alerts_outcome CHECK (outcome IN
                   ('sending', 'sent', 'failed', 'capped', 'muted', 'quiet', 'no_token', 'backfill', 'inherited')),
    -- Tokens APNs accepted the alert for.
    devices    int         NOT NULL DEFAULT 0,
    decided_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workout_id)
);
-- No other index: every statement is per user (the primary key's prefix), and pruning keeps a user's ledger
-- to a few rows.

ALTER TABLE workouts
    ADD COLUMN IF NOT EXISTS device_id        uuid,
    ADD COLUMN IF NOT EXISTS source_bundle_id text;

-- notify: decides, records and prunes; finishes a claim with its outcome and the accepted tokens.
GRANT SELECT, INSERT, DELETE ON workout_alerts TO ryft_notify;
GRANT UPDATE (outcome, devices) ON workout_alerts TO ryft_notify;
GRANT SELECT (user_id, id, activity, start_at, end_at, source, source_bundle_id, device_id, strain, session_id,
              duplicate_of, created_at) ON workouts TO ryft_notify;
GRANT SELECT (id, created_at) ON devices TO ryft_notify;

-- api: erases an account's rows and moves a merged account's (the owner column only).
GRANT SELECT, DELETE ON workout_alerts TO ryft_api;
GRANT UPDATE (user_id) ON workout_alerts TO ryft_api;

DROP POLICY IF EXISTS workout_alerts_services ON workout_alerts;
CREATE POLICY workout_alerts_services ON workout_alerts TO ryft_api, ryft_notify USING (true) WITH CHECK (true);
-- 0015's notify read loop left out workouts (notify read none of it then).
DROP POLICY IF EXISTS workouts_notify_read ON workouts;
CREATE POLICY workouts_notify_read ON workouts FOR SELECT TO ryft_notify USING (true);

-- Supabase only, as in 0007, 0015 and 0037: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE workout_alerts ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON workout_alerts FROM anon, authenticated, service_role;
END $$;
