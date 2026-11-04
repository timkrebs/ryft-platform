-- Unique handle per user for the HealthKit ids of 0029 (design D-a). Its own file: building an index
-- scans the table, and the migrator runs each file in one transaction (db.go), so it must not share
-- one with the ALTER of 0029 that holds ACCESS EXCLUSIVE.
--
-- Locks and size: a plain CREATE INDEX takes SHARE on the table, which blocks writes (ingest's
-- upserts wait, reads go on) for the length of the scan. At beta size that is seconds. ABOVE ~5M ROWS
-- IN samples THIS MUST NOT RUN AS IT IS: build it CONCURRENTLY, which cannot run inside a transaction,
-- so the migrator first needs a no-transaction mode (a "-- migrate:no-transaction" marker, not built
-- yet); measure SELECT count(*) / pg_total_relation_size('samples') before the PreSync that applies it.
-- The partial predicate keeps the index empty until the app sends ids and out of the way of old rows.
-- lock_timeout bounds the wait to get the SHARE lock (a long write holds the table), not the scan:
-- a build that cannot start fails PreSync and the old pods keep serving (as 0027).
--
-- Rollback = re-pin the images; the indexes stay, and older images never write hk_id, so they never
-- hit them. IF NOT EXISTS makes a rerun a no-op.
SET LOCAL lock_timeout = '5s';

CREATE UNIQUE INDEX IF NOT EXISTS samples_user_hk        ON samples (user_id, hk_id)        WHERE hk_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS sleep_segments_user_hk ON sleep_segments (user_id, hk_id) WHERE hk_id IS NOT NULL;
