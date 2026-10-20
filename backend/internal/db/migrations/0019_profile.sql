-- Profile (Beta, docs/DECISIONS.md D81): the birth year for a max-HR estimate
-- (Tanaka, internal/scoring/maxhr.go) and where max_hr comes from. Only the year:
-- nothing finer is needed, so nothing finer is stored.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS birth_year    smallint CHECK (birth_year BETWEEN 1900 AND 2100),
    ADD COLUMN IF NOT EXISTS max_hr_source text NOT NULL DEFAULT 'default'
        CHECK (max_hr_source IN ('default', 'estimated', 'manual'));

-- An account whose max_hr is not the default 190 set it by hand (PATCH /v1/me was
-- the only way), so an estimate must not replace it.
UPDATE users SET max_hr_source = 'manual' WHERE max_hr <> 190;
