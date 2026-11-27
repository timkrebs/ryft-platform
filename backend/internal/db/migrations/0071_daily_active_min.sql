-- The migrator runs each file in one transaction. The ALTER below takes
-- ACCESS EXCLUSIVE on daily_scores; fail after 5 s if a reader holds the table
-- so readers and writers do not queue behind a waiting ALTER.
SET LOCAL lock_timeout = '5s';

-- Active time (docs/API.md → DailyScore → active_min) is a scoring result.
-- No default: NULL on rows scored before, until they are rescored. Zero means
-- the day had data but no active minutes. A nullable column without a default
-- rewrites nothing; the CHECK scans the (all NULL) rows once under ACCESS EXCLUSIVE.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS active_min integer
        CHECK (active_min >= 0);

-- No grants needed: scoring's SELECT, INSERT, UPDATE and the api's SELECT, DELETE
-- on daily_scores (0009) are table-wide and cover the new column. Notify's
-- column grant (0015) leaves it out. RLS is per table and stays as it is.
