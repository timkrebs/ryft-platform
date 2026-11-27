-- Profile (D306): the app-wide goal, sex and the latest manual weigh-in.
-- HealthKit weigh-ins are resolved from samples on read. Account erasure
-- already deletes the users row and its samples.
-- Existing table grants cover the API's reads and writes and ingest/scoring's
-- user reads (0009). Notify's column grant (0015) stays limited to push settings.
SET LOCAL lock_timeout = '5s';

-- IF NOT EXISTS skips each column and its constraints on a rerun.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS goal text
        CONSTRAINT users_goal_check CHECK (goal IN ('health', 'endurance', 'strength', 'body_composition', 'performance')),
    ADD COLUMN IF NOT EXISTS sex text
        CONSTRAINT users_sex_check CHECK (sex IN ('female', 'male', 'unspecified')),
    ADD COLUMN IF NOT EXISTS weight_manual_kg numeric(4,1)
        CONSTRAINT users_weight_manual_kg_check CHECK (weight_manual_kg BETWEEN 25.0 AND 350.0),
    ADD COLUMN IF NOT EXISTS weight_manual_at timestamptz
        CONSTRAINT users_weight_manual_pair_check CHECK ((weight_manual_kg IS NULL) = (weight_manual_at IS NULL));
