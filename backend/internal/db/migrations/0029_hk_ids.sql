-- HealthKit ids and deletions in sync (docs/work/2026-10-31-data-quality/design.md D-a, D-b).
-- Additive: two nullable columns and one grant, nothing renamed or dropped, so the images from before
-- this migration keep working against the new schema (rollback = re-pin the old images; the columns
-- and the grant stay, and nothing reads or uses them).
--
-- Locks: ADD COLUMN without a default is a catalog change that rewrites nothing, but it takes ACCESS
-- EXCLUSIVE, and the migrator runs the file in one transaction (db.go), so that lock is held to the
-- commit. lock_timeout bounds the wait for it (D116, as in 0027/0028); no statement below scans a table.
-- The unique indexes on the new columns are in 0030, a file of their own, because building one scans
-- the table and must not run under the lock of the ALTER below.
-- Order: samples, then sleep_segments (the order ingest writes them in).
SET LOCAL lock_timeout = '5s';

-- hk_id is the HealthKit object's UUID: a deletion handle the latest upload owns, not the record's
-- identity (the natural key stays that, D-a). NULL for every row stored before, and for records from
-- an app that sends no id; nothing here backfills it (an upload with an id adopts the matching row).
ALTER TABLE samples        ADD COLUMN IF NOT EXISTS hk_id uuid;
ALTER TABLE sleep_segments ADD COLUMN IF NOT EXISTS hk_id uuid;

-- Ingest applies the app's deletions (D-b) and the same-source workout fold (D-c). Table-level, as
-- Postgres has no column-level DELETE. Everything else the apply transaction touches is granted
-- already: SELECT on the three tables (the DELETE ... WHERE and RETURNING read user_id, hk_id, id,
-- type, start_at, end_at, source, session_id), INSERT/UPDATE on score_queue (0025) and INSERT on
-- outbox (0026). Ingest does not read sessions (design: the detach is the row's deletion). hk_id is
-- covered by the table-level SELECT/INSERT/UPDATE ingest has on both tables (0009); scoring (SELECT)
-- and the api (SELECT, DELETE, UPDATE (user_id) for the merge, 0024) get no write to it.
GRANT DELETE ON samples, sleep_segments, workouts TO ryft_ingest;

-- Scoring writes the ryft-session workouts, and an app's sync must never delete one: it is
-- the training log's own row (0011), not a copy of Health's. The ingest code filters on source;
-- this restrictive policy says the same to the database (Supabase, where RLS is on; inert on plain
-- Postgres, as the 0011 policies of scoring). Only DELETE: ingest's other statements are unchanged.
DROP POLICY IF EXISTS workouts_ingest_no_session_delete ON workouts;
CREATE POLICY workouts_ingest_no_session_delete ON workouts AS RESTRICTIVE FOR DELETE TO ryft_ingest
    USING (source <> 'ryft-session');
