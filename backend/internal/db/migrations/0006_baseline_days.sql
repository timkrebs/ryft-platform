-- Nights of history behind the day's baselines: min(HRV days, resting-HR days)
-- among the 30 scored days before it. calibrating = baseline_days < 7, so the
-- Today screen can show "N nights to go". 0 until the day is (re)scored.
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS baseline_days smallint NOT NULL DEFAULT 0;
