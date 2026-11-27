-- Recovery without a watch: the morning check-in (docs/DECISIONS.md D291–D295; docs/API.md → Recovery without a
-- watch). One row per account and wake day: what the user reported (bedtime, wake time, the two 1–10 ratings,
-- soreness) and what the api computed from it when it was saved (D292): the sleep duration, the 0–100 estimate, its
-- components, the acute-drop flag with its reason and the confounders the evening before may explain it with.
--
-- Not stored, on purpose: `confidence` (it depends on the day before's heart rate, D291) and `superseded` (it
-- depends on later syncs and the flag) are derived on read, as is `n2r_bedtime_applied`. A check-in's score
-- depends on the check-ins before it (D292), so the api rescores the later ones within 14 days in the same
-- transaction; model_version says which model computed the row, and a newer model rescores it.
--
-- Written and read by the api alone, on its request path, like the journal (0016): no other service reads it, so
-- no other role gets a grant. The account's deletion and merge (DeleteUser, MergeUsers) and the export read it from
-- the store; ON DELETE CASCADE is the safety net for the users row, not the deletion path.
--
-- Additive: a new table, nothing renamed or dropped. Rollback = re-pin the old images; the table stays and nothing
-- old reads it. The migrator runs the file in one transaction, so the foreign key's lock on users (SHARE ROW
-- EXCLUSIVE, writes of users wait) is held to the commit; the table is new and empty, so nothing is scanned, and
-- lock_timeout bounds the wait (D116).
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS recovery_checkins (
    user_id                 uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- the wake day: the night that ends on this date belongs to it (the account's calendar, as everywhere in v1)
    wake_date               date        NOT NULL,
    -- as the user reports them (UTC instants)
    bedtime                 timestamptz NOT NULL,
    wake_time               timestamptz NOT NULL,
    -- the UTC offset of the clock the user saw at wake_time: wake_date is the date of wake_time + tz_offset_sec
    tz_offset_sec           integer     NOT NULL CONSTRAINT recovery_checkins_tz_offset_check
                                            CHECK (tz_offset_sec BETWEEN -50400 AND 50400 AND tz_offset_sec % 60 = 0),
    perceived_sleep_quality smallint    NOT NULL CONSTRAINT recovery_checkins_quality_check
                                            CHECK (perceived_sleep_quality BETWEEN 1 AND 10),
    perceived_freshness     smallint    NOT NULL CONSTRAINT recovery_checkins_freshness_check
                                            CHECK (perceived_freshness BETWEEN 1 AND 10),
    -- captured with zero weight (D292): stored, shown, exported, kept for a later calibration
    soreness                boolean     NOT NULL,
    -- computed by the api when saved (internal/scoring, D292)
    -- wake_time - bedtime in whole minutes; the api's 422 keeps it within 60–960, the bound here is wider on purpose
    sleep_duration_min      integer     NOT NULL CONSTRAINT recovery_checkins_duration_check
                                            CHECK (sleep_duration_min BETWEEN 0 AND 1440),
    recovery_score          smallint    NOT NULL CONSTRAINT recovery_checkins_score_check
                                            CHECK (recovery_score BETWEEN 0 AND 100),
    -- RecoveryComponents: wellness, wellness_absolute, wellness_personal, duration, regularity, n2r (a null part is left out)
    components              jsonb       NOT NULL CONSTRAINT recovery_checkins_components_check
                                            CHECK (jsonb_typeof(components) = 'object'),
    acute_drop              boolean     NOT NULL DEFAULT false,
    -- the readiness reason of a drop (D294); closed set, NULL without acute_drop
    drop_reason             text        CONSTRAINT recovery_checkins_drop_reason_check
                                            CHECK (drop_reason IN ('checkin_below_usual', 'checkin_low_quality_and_freshness')),
    -- what the evening before may explain the drop with (D294): an open set of tags, [] without acute_drop
    confounders             text[]      NOT NULL DEFAULT '{}' CONSTRAINT recovery_checkins_confounders_check
                                            CHECK (cardinality(confounders) <= 16),
    model_version           text        NOT NULL CONSTRAINT recovery_checkins_model_version_check
                                            CHECK (length(model_version) BETWEEN 1 AND 32),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    -- the one range read (GET /v1/recovery/checkins, the 14-day history, the export) is this key: user, then date
    PRIMARY KEY (user_id, wake_date),
    CONSTRAINT recovery_checkins_wake_after_bed CHECK (wake_time > bedtime),
    CONSTRAINT recovery_checkins_drop_consistent CHECK (acute_drop OR (drop_reason IS NULL AND cardinality(confounders) = 0))
);

-- api: the app's read and write side, the account's deletion and the merge. No other service reads a check-in.
GRANT SELECT, INSERT, UPDATE, DELETE ON recovery_checkins TO ryft_api;

DROP POLICY IF EXISTS recovery_checkins_services ON recovery_checkins;
CREATE POLICY recovery_checkins_services ON recovery_checkins TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007 and 0016: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE recovery_checkins ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON recovery_checkins FROM anon, authenticated, service_role;
END $$;
