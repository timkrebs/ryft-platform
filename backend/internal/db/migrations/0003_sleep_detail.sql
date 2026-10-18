-- Typed night summary next to the scores, so the Sleep screen (GET /v1/days/{day}
-- sleep object, GET /v1/sleep) reads columns instead of digging through details.
-- light_min is HealthKit "core". in_bed_min is the effective time in bed
-- (scoring.InBedMinutes). Rows scored before this migration stay NULL until
-- recomputed (POST /v1/recompute).
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS sleep_start      timestamptz,
    ADD COLUMN IF NOT EXISTS sleep_end        timestamptz,
    ADD COLUMN IF NOT EXISTS in_bed_min       int,
    ADD COLUMN IF NOT EXISTS deep_min         int,
    ADD COLUMN IF NOT EXISTS light_min        int,
    ADD COLUMN IF NOT EXISTS rem_min          int,
    ADD COLUMN IF NOT EXISTS awake_min        int,
    ADD COLUMN IF NOT EXISTS sleep_has_stages boolean NOT NULL DEFAULT false;
