-- Measured basal energy over the local day. Existing table-level grants on
-- daily_scores cover this column, as they do active_kcal.
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS resting_kcal integer;
