-- Cross-source workout duplicates (docs/work/2026-10-31-data-quality/design.md D-d).
-- Additive: one nullable column, one CHECK and one column grant; nothing renamed or dropped, so the
-- images from before this migration keep working (rollback = re-pin the old images; the column and the
-- grant stay and nothing reads them).
--
-- Locks: ADD COLUMN without a default is a catalog change that rewrites nothing, but it takes ACCESS
-- EXCLUSIVE, and the migrator runs the file in one transaction (db.go), so the lock is held to the
-- commit. lock_timeout bounds the wait (D116, as in 0027/0029). The CHECK is added
-- NOT VALID and then validated, but both run in this same transaction, which still holds the ACCESS
-- EXCLUSIVE lock of ADD COLUMN, so the validating scan of workouts happens under that lock. That is
-- harmless at beta size (one short scan of a small table). To keep the lock short on a big table, the
-- VALIDATE would have to move to a migration of its own.
SET LOCAL lock_timeout = '5s';

-- duplicate_of is the id of the workout this one duplicates (same user, other source), derived by
-- scoring on every pass of the day like strain; NULL = shown on its own. NO foreign key: merge moves
-- workouts with UPDATE user_id (0024), and a primary that moves while its duplicate stays would
-- violate a (user_id, duplicate_of) -> (user_id, id) FK, and Postgres has no ON UPDATE SET NULL (col).
-- Readers resolve it through the primary key (user_id, id) of workouts, so no index (design D-d).
ALTER TABLE workouts ADD COLUMN IF NOT EXISTS duplicate_of uuid;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'workouts_duplicate_of_not_self' AND conrelid = 'workouts'::regclass) THEN
        ALTER TABLE workouts ADD CONSTRAINT workouts_duplicate_of_not_self
            CHECK (duplicate_of <> id) NOT VALID;
    END IF;
END $$;
ALTER TABLE workouts VALIDATE CONSTRAINT workouts_duplicate_of_not_self;

-- Scoring is the one writer (computeDay sets or clears it for the day's workouts). Column-level, as
-- its other workouts grants (0009, 0011): it still cannot change start, end, source or activity.
-- It already holds table-level SELECT (0009) for the UPDATE's WHERE. On Supabase the services policy
-- (0009, USING/WITH CHECK true) admits the UPDATE and the restrictive policies of 0011 and 0029 are
-- for INSERT and DELETE only, so no policy changes. The api and ingest only need SELECT, which they
-- hold at table level (0009); ingest's fold DELETEs rows (0029) and writes no duplicate_of, and the
-- api reads it and writes only user_id (0024).
GRANT UPDATE (duplicate_of) ON workouts TO ryft_scoring;
