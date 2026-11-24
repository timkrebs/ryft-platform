-- D268: opt-in account settings, nightly attribution and a dimensionless catalog measurement.
ALTER TABLE nutrition.nutrients DROP CONSTRAINT nutrients_unit_check;
ALTER TABLE nutrition.nutrients ADD CONSTRAINT nutrients_unit_check
    CHECK (unit IN ('g', 'mg', 'ug', 'kcal', 'kJ', 'index'));
INSERT INTO nutrition.nutrients (code, name, unit, nutrient_group, description)
    VALUES ('glycemic_index', 'Glycemic index', 'index', 'other', 'Measured index relative to glucose=100');

CREATE TABLE nutrition.n2r_profiles (
    user_id uuid PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    body_mass_kg double precision NOT NULL CHECK (body_mass_kg BETWEEN 25 AND 350),
    sex text NOT NULL CHECK (sex IN ('female', 'male', 'unspecified')),
    sweat_rate_l_per_h double precision CHECK (sweat_rate_l_per_h BETWEEN 0 AND 5),
    sweat_na_mg_per_l double precision CHECK (sweat_na_mg_per_l BETWEEN 0 AND 5000),
    caffeine_half_life_h double precision CHECK (caffeine_half_life_h BETWEEN 1 AND 12),
    expected_bedtime time CHECK (expected_bedtime < '24:00'),
    next_workout_start_at timestamptz,
    calibrated_half_life_h double precision CHECK (calibrated_half_life_h BETWEEN 1 AND 7.5),
    last_calibrated_day date,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nutrition.daily_biometric_baselines (
    user_id uuid NOT NULL REFERENCES nutrition.n2r_profiles(user_id) ON DELETE CASCADE,
    wake_date date NOT NULL,
    ln_rmssd_7d_mean double precision,
    ln_rmssd_7d_sd double precision,
    ln_rmssd_7d_cv double precision,
    swc double precision,
    ln_rmssd_delta double precision,
    rhr_delta double precision,
    change text NOT NULL CHECK (change IN ('unavailable','stable','minor','moderate','major')),
    alcohol_g_kg double precision NOT NULL,
    caffeine_bed_mg double precision,
    caffeine_calibration_mg double precision,
    late_meal_penalty double precision NOT NULL,
    n2r_score double precision,
    effective_caffeine_half_life_h double precision NOT NULL,
    n2r_score_breakdown jsonb NOT NULL CHECK (jsonb_typeof(n2r_score_breakdown) = 'object'),
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, wake_date)
);

ALTER TABLE nutrition.n2r_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE nutrition.daily_biometric_baselines ENABLE ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON nutrition.n2r_profiles TO ryft_api;
GRANT SELECT, INSERT, UPDATE ON nutrition.daily_biometric_baselines TO ryft_api;
CREATE POLICY owner_n2r_profile ON nutrition.n2r_profiles TO ryft_api
    USING (user_id = nutrition.account()) WITH CHECK (user_id = nutrition.account());
CREATE POLICY owner_n2r_baseline ON nutrition.daily_biometric_baselines TO ryft_api
    USING (user_id = nutrition.account()) WITH CHECK (user_id = nutrition.account());

-- Enumerate work without returning any values from meals, settings or biometric records.
CREATE FUNCTION nutrition.n2r_due(at_time timestamptz, policy_version text, after_id uuid, after_day date, batch_size integer)
RETURNS TABLE(account_id uuid, wake_date date)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT p.user_id, d.day
    FROM nutrition.n2r_profiles p
    JOIN public.users u ON u.id=p.user_id
    JOIN pg_timezone_names zone ON zone.name=u.tz
    JOIN public.daily_scores d ON d.user_id=p.user_id
      AND d.day BETWEEN (at_time AT TIME ZONE zone.name)::date-1 AND (at_time AT TIME ZONE zone.name)::date
    LEFT JOIN nutrition.daily_biometric_baselines b ON b.user_id=p.user_id AND b.wake_date=d.day
    WHERE (p.user_id,d.day) > (after_id,after_day)
      AND (to_jsonb(d)->>'hrv_metric'='rmssd' AND d.hrv_ms IS NOT NULL OR d.rhr IS NOT NULL OR d.sleep_actual_min > 0)
      AND (d.sleep_end IS NULL OR d.sleep_end <= at_time)
      AND (b.processed_at IS NULL OR b.processed_at < greatest(d.computed_at,p.updated_at)
           OR b.processed_at < at_time - interval '6 hours')
      AND (policy_version = '' OR policy_version = (
          SELECT c.policy_version FROM public.consents c WHERE c.user_id=p.user_id
              AND c.kind='health_processing' AND c.revoked_at IS NULL ORDER BY c.granted_at DESC LIMIT 1))
    ORDER BY p.user_id,d.day LIMIT least(greatest(batch_size, 1), 100)
$$;
REVOKE ALL ON FUNCTION nutrition.n2r_due(timestamptz, text, uuid, date, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.n2r_due(timestamptz, text, uuid, date, integer) TO ryft_api;
