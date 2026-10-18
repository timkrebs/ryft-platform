-- Strain detail for the Strain screen and GET /v1/workouts/{id}: minutes in the
-- five heart-rate-reserve zones (50-60, 60-70, 70-80, 80-90, 90+ %) and Banister
-- TRIMP, stored at scoring time so reads do not re-integrate heart rate.
-- zone_minutes is a JSON array of five numbers (minutes, one decimal).
ALTER TABLE workouts
    ADD COLUMN IF NOT EXISTS zone_minutes jsonb,
    ADD COLUMN IF NOT EXISTS trimp        double precision;

ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS zone_minutes jsonb,
    ADD COLUMN IF NOT EXISTS trimp        double precision,
    ADD COLUMN IF NOT EXISTS hr_min       int;   -- minutes of usable heart-rate data
