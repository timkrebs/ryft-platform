-- Why a day is calibrating (docs/API.md → DailyScore → calibrating_reason,
-- docs/SCORING.md → Calibrating and its reason): the two counts behind
-- baseline_days = least(baseline_hrv_days, baseline_rhr_days), and the reason.
-- Results of a score, written by scoring on every row it scores; NULL on rows
-- scored before, until they are rescored, which the api serves as "unknown".
-- No default on purpose: 0 would read as "not one night with an HRV".
-- Adding nullable columns without a default rewrites nothing; the CHECK scans
-- the (all NULL) rows once.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS baseline_hrv_days  smallint,
    ADD COLUMN IF NOT EXISTS baseline_rhr_days  smallint,
    ADD COLUMN IF NOT EXISTS calibrating_reason text
        CHECK (calibrating_reason IN ('short_history', 'no_hrv', 'no_rhr'));  -- a closed enum

-- No grants needed: scoring's SELECT, INSERT, UPDATE and the api's SELECT, DELETE
-- on daily_scores (0009) are table-wide and cover new columns; notify's column
-- grant (0015) leaves them out, as it should. RLS is per table and stays as it is.
