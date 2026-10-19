-- Sleep upgrades (M4; docs/SCORING.md → Sleep, D65–D68): nap credit, the need term
-- by term, a quality score of its own, consistency, and a baseline that learns.

ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS nap_min               int,       -- naps that day, credited to the next night
    ADD COLUMN IF NOT EXISTS sleep_quality         smallint,
    ADD COLUMN IF NOT EXISTS sleep_consistency     smallint,
    ADD COLUMN IF NOT EXISTS sleep_need_base_min   int,
    ADD COLUMN IF NOT EXISTS sleep_need_strain_min int,
    ADD COLUMN IF NOT EXISTS sleep_need_debt_min   int,
    ADD COLUMN IF NOT EXISTS sleep_need_nap_min    int;       -- the credit this night got

-- The baseline in use is still sleep_base_min. Its source says where it came from;
-- the learned estimate is kept apart so the app can show it while the user overrides.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS sleep_base_manual      boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS sleep_base_source      text NOT NULL DEFAULT 'default'
        CHECK (sleep_base_source IN ('default', 'learned', 'manual')),
    ADD COLUMN IF NOT EXISTS sleep_base_learned_min int,
    ADD COLUMN IF NOT EXISTS sleep_base_learned_at  timestamptz;

-- Whoever already changed the default set it by hand: learning must not overwrite it.
UPDATE users SET sleep_base_manual = true, sleep_base_source = 'manual'
WHERE sleep_base_min <> 460 AND NOT sleep_base_manual;

-- scoring writes the weekly estimate, and the baseline in use when it is not manual;
-- nothing else of users. (The api has UPDATE on users already, for PATCH /v1/me.)
GRANT UPDATE (sleep_base_min, sleep_base_source, sleep_base_learned_min, sleep_base_learned_at) ON users TO ryft_scoring;
