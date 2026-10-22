-- The migrator runs each file in one transaction with no timeouts. The ALTER below
-- takes ACCESS EXCLUSIVE on daily_scores; if that is not granted within 5 s (a long
-- read holds the table), the migration fails, PreSync fails and the old pods keep
-- serving, instead of every reader and writer queueing behind the waiting ALTER.
SET LOCAL lock_timeout = '5s';

-- Why a day is calibrating (docs/API.md → DailyScore → calibrating_reason,
-- docs/SCORING.md → Calibrating and its reason): the two counts behind
-- baseline_days = least(baseline_hrv_days, baseline_rhr_days), and the reason.
-- Results of a score, written by scoring on every row it scores; NULL on rows
-- scored before, until they are rescored, which the api serves as "unknown".
-- No default on purpose: 0 would read as "not one night with an HRV".
-- Adding nullable columns without a default rewrites nothing; the CHECK scans
-- the (all NULL) rows once under ACCESS EXCLUSIVE; lock_timeout bounds the wait.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS baseline_hrv_days  smallint,
    ADD COLUMN IF NOT EXISTS baseline_rhr_days  smallint,
    ADD COLUMN IF NOT EXISTS calibrating_reason text
        CHECK (calibrating_reason IN ('short_history', 'no_hrv', 'no_rhr'));  -- a closed enum

-- No grants needed: scoring's SELECT, INSERT, UPDATE and the api's SELECT, DELETE
-- on daily_scores (0009) are table-wide and cover new columns; notify's column
-- grant (0015) leaves them out, as it should. RLS is per table and stays as it is.
