-- Daily activity totals: steps and active energy summed over the local day
-- after source de-duplication (one source per hour, docs/SCORING.md). NULL
-- when no samples of that type arrived for the day.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS steps       int,
    ADD COLUMN IF NOT EXISTS active_kcal int;
