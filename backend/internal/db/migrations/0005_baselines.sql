-- The 30-day baselines each day's recovery was scored against: HRV as the
-- geometric mean (exp of the mean ln HRV, the space recovery compares in) and
-- the mean resting HR, over up to 30 scored days before the day. Read by
-- GET /v1/trends and the insight rules; NULL until the day is (re)scored.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS hrv_baseline_ms double precision,
    ADD COLUMN IF NOT EXISTS rhr_baseline    double precision;
