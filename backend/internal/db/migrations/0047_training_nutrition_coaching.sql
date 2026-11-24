-- D271: opt-in coaching observations, forecasts and aggregate evaluation.
SET LOCAL lock_timeout = '5s';

-- These measurements enrich the existing anchored sync and its deletion semantics.
ALTER TABLE public.workouts ADD COLUMN coaching_source_bundle_id text,
    ADD COLUMN coaching_user_entered boolean,
    ADD COLUMN coaching_tz_offset_sec integer;

CREATE TABLE nutrition.user_goal_profiles (
    user_id uuid PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    settings jsonb NOT NULL CHECK (jsonb_typeof(settings) = 'object'),
    goal text NOT NULL CHECK (goal IN ('HYPERTROPHY','MARATHON','CYCLING_RACE','FAT_LOSS_RECOMP')),
    active_from date NOT NULL,
    switched_on date,
    gme double precision NOT NULL DEFAULT .239 CHECK (gme BETWEEN .20 AND .25),
    tissue_density double precision NOT NULL DEFAULT 7700 CHECK (tissue_density BETWEEN 5000 AND 10000),
    caffeine_half_h double precision CHECK (caffeine_half_h BETWEEN 2.5 AND 9),
    alcohol_sensitivity double precision NOT NULL DEFAULT 1 CHECK (alcohol_sensitivity BETWEEN .8 AND 1.5),
    sweat_rate_lph double precision CHECK (sweat_rate_lph BETWEEN .1 AND 4),
    sweat_sodium_mg_l double precision CHECK (sweat_sodium_mg_l BETWEEN 200 AND 2000),
    last_calibrated date
);

CREATE TABLE nutrition.canonical_workouts (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    source_provider text NOT NULL CHECK (source_provider IN ('GARMIN_FIT','SUUNTO_FIT','GARMIN_JSON','SUUNTO_JSON','APPLE_HEALTHKIT','MANUAL')),
    source_priority smallint NOT NULL CHECK (source_priority BETWEEN 1 AND 5),
    external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 192),
    parent_workout_id text,
    start_at timestamptz NOT NULL,
    end_at timestamptz NOT NULL CHECK (end_at > start_at AND end_at <= start_at + interval '48 hours'),
    tz_offset_sec integer NOT NULL CHECK (tz_offset_sec BETWEEN -64800 AND 64800),
    was_user_entered boolean NOT NULL,
    sport_type text NOT NULL,
    duration_sec double precision NOT NULL CHECK (duration_sec > 0),
    active_kcal_raw double precision NOT NULL,
    mechanical_work_kj double precision NOT NULL,
    distance_m double precision NOT NULL,
    avg_hr double precision NOT NULL,
    max_hr double precision NOT NULL,
    normalized_power_w double precision NOT NULL,
    ftp_w double precision NOT NULL,
    rr_intervals_available boolean NOT NULL,
    suunto_plus jsonb CHECK(suunto_plus IS NULL OR jsonb_typeof(suunto_plus)='object'),
    PRIMARY KEY (user_id,source_provider,external_id)
);
CREATE INDEX canonical_workout_range ON nutrition.canonical_workouts(user_id,start_at);

CREATE TABLE nutrition.wearable_sync_cursors (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    device_id uuid NOT NULL,
    stream text NOT NULL CHECK(length(stream) BETWEEN 1 AND 48),
    sequence bigint NOT NULL CHECK(sequence >= 0),
    cursor text NOT NULL CHECK(length(cursor) <= 4096),
    PRIMARY KEY (user_id,device_id,stream)
);

CREATE TABLE nutrition.coaching_feedback (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    day date NOT NULL,
    data jsonb NOT NULL CHECK (jsonb_typeof(data)='object'),
    PRIMARY KEY (user_id,day)
);

CREATE TABLE nutrition.daily_energy_states (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    day date NOT NULL,
    goal text NOT NULL,
    model_version text NOT NULL,
    workout_source text NOT NULL,
    sport text NOT NULL,
    tdee_kcal double precision NOT NULL,
    projected_kcal double precision NOT NULL,
    gme double precision NOT NULL,
    tissue_density double precision NOT NULL,
    plan jsonb NOT NULL CHECK (jsonb_typeof(plan)='object'),
    n2r_projection jsonb CHECK(n2r_projection IS NULL OR jsonb_typeof(n2r_projection)='object'),
    captured_at timestamptz NOT NULL,
    PRIMARY KEY (user_id,day)
);

CREATE TABLE nutrition.model_evaluation_logs (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    day date NOT NULL,
    goal text NOT NULL,
    model_version text NOT NULL,
    workout_source text NOT NULL,
    sport text NOT NULL,
    energy_bias double precision,
    weight_ape double precision,
    predicted_kg double precision,
    actual_kg double precision,
    intake_confidence double precision NOT NULL,
    cycling_power_kcal double precision NOT NULL,
    n2r_score double precision,
    alcohol_penalty double precision,
    caffeine_penalty double precision,
    late_meal_penalty double precision,
    caffeine_exposure double precision,
    alcohol_dose double precision,
    hrv_delta double precision,
    rhr_delta double precision,
    sws_min double precision,
    rem_min double precision,
    red_probability double precision,
    red_outcome boolean,
    confounder_attributed boolean NOT NULL,
    reported_confounder boolean,
    intervention_recovered boolean,
    adherence jsonb NOT NULL CHECK (jsonb_typeof(adherence)='object'),
    PRIMARY KEY (user_id,day)
);
CREATE INDEX model_eval_days ON nutrition.model_evaluation_logs(day);

-- Partner identifiers are provisioned by the operator after linked-account consent.
CREATE TABLE nutrition.wearable_links (
    provider text NOT NULL CHECK(provider IN ('garmin','suunto')),
    provider_user text NOT NULL,
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    PRIMARY KEY (provider,provider_user),
    UNIQUE (provider,user_id)
);

CREATE TABLE nutrition.wearable_inbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL,
    provider_user text NOT NULL,
    digest bytea NOT NULL,
    body jsonb NOT NULL,
    due_at timestamptz NOT NULL DEFAULT now(),
    attempts smallint NOT NULL DEFAULT 0,
    completed_at timestamptz,
    UNIQUE(provider,provider_user,digest),
    FOREIGN KEY(provider,provider_user) REFERENCES nutrition.wearable_links(provider,provider_user) ON DELETE CASCADE
);
CREATE INDEX wearable_inbox_due ON nutrition.wearable_inbox(due_at,id) WHERE completed_at IS NULL;

-- Only genuine RMSSD is stored here. HealthKit's SDNN stays in existing samples.
CREATE TABLE nutrition.wearable_nights (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    provider text NOT NULL CHECK(provider IN ('garmin','suunto')),
    wake_at timestamptz NOT NULL,
    rmssd_ms double precision,
    rhr double precision,
    sws_min double precision,
    rem_min double precision,
    PRIMARY KEY(user_id,provider,wake_at)
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['user_goal_profiles','canonical_workouts','wearable_sync_cursors',
        'coaching_feedback','daily_energy_states','model_evaluation_logs','wearable_nights']
    LOOP
        EXECUTE format('ALTER TABLE nutrition.%I ENABLE ROW LEVEL SECURITY',t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON nutrition.%I TO ryft_api',t);
        EXECUTE format('CREATE POLICY coaching_owner ON nutrition.%I TO ryft_api USING (user_id=nutrition.account()) WITH CHECK (user_id=nutrition.account())',t);
    END LOOP;
END $$;

ALTER TABLE nutrition.wearable_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE nutrition.wearable_inbox ENABLE ROW LEVEL SECURITY;
-- Ingest can commit a cursor for the authorized device without any catalog grants.
CREATE FUNCTION public.record_healthkit_cursor(owner_id uuid,device uuid,kind text,seq bigint,next_cursor text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    INSERT INTO nutrition.wearable_sync_cursors AS stored(user_id,device_id,stream,sequence,cursor)
    SELECT owner_id,device,kind,seq,next_cursor WHERE owner_id=nutrition.account()
    AND EXISTS (SELECT FROM public.devices d WHERE d.id=device AND d.user_id=owner_id)
    ON CONFLICT(user_id,device_id,stream) DO UPDATE SET sequence=excluded.sequence,cursor=excluded.cursor
    WHERE stored.sequence<excluded.sequence
$$;
REVOKE ALL ON FUNCTION public.record_healthkit_cursor(uuid,uuid,text,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.record_healthkit_cursor(uuid,uuid,text,bigint,text) TO ryft_ingest;

-- Webhook enqueue acts only on pre-linked, currently consented accounts.
CREATE FUNCTION nutrition.enqueue_wearable(src text, ref text, checksum bytea, payload jsonb, policy text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    INSERT INTO nutrition.wearable_inbox(provider,provider_user,digest,body)
    SELECT src,ref,checksum,payload FROM nutrition.wearable_links l
    WHERE l.provider=src AND l.provider_user=ref AND EXISTS (SELECT FROM nutrition.n2r_profiles n WHERE n.user_id=l.user_id) AND
        (policy='' OR policy=(SELECT c.policy_version FROM public.consents c
            WHERE c.user_id=l.user_id AND c.kind='health_processing' AND c.revoked_at IS NULL
            ORDER BY c.granted_at DESC LIMIT 1))
    ON CONFLICT DO NOTHING
$$;

CREATE FUNCTION nutrition.claim_wearable(at_time timestamptz, policy text)
RETURNS TABLE(job_id bigint, src text, owner_id uuid, payload jsonb, attempt smallint)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    WITH next AS (
        SELECT i.id,l.user_id FROM nutrition.wearable_inbox i
        JOIN nutrition.wearable_links l USING(provider,provider_user)
        WHERE i.completed_at IS NULL AND i.due_at<=at_time AND i.attempts<5
        AND EXISTS (SELECT FROM nutrition.n2r_profiles n WHERE n.user_id=l.user_id)
        AND (policy='' OR policy=(SELECT c.policy_version FROM public.consents c
            WHERE c.user_id=l.user_id AND c.kind='health_processing' AND c.revoked_at IS NULL
            ORDER BY c.granted_at DESC LIMIT 1))
        ORDER BY i.due_at,i.id LIMIT 1 FOR UPDATE OF i SKIP LOCKED
    )
    UPDATE nutrition.wearable_inbox i SET due_at=at_time+interval '15 minutes',attempts=i.attempts+1
    FROM next WHERE i.id=next.id RETURNING i.id,i.provider,next.user_id,i.body,i.attempts
$$;
CREATE FUNCTION nutrition.finish_wearable(job bigint, attempt smallint, at_time timestamptz, succeeded boolean)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    UPDATE nutrition.wearable_inbox i SET
        completed_at=CASE WHEN succeeded OR i.attempts>=5 THEN at_time END,
        body=CASE WHEN succeeded OR i.attempts>=5 THEN '{}'::jsonb ELSE i.body END
    WHERE i.id=job AND i.attempts=attempt AND i.completed_at IS NULL
$$;

CREATE FUNCTION nutrition.purge_wearable(at_time timestamptz)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    DELETE FROM nutrition.wearable_inbox
    WHERE id IN (
        SELECT i.id FROM nutrition.wearable_inbox i
        WHERE i.completed_at<at_time-interval '31 days'
           OR i.completed_at IS NULL AND (
                i.attempts>=5 AND i.due_at<at_time-interval '1 day'
                OR i.due_at<at_time-interval '7 days')
        ORDER BY i.id LIMIT 100 FOR UPDATE SKIP LOCKED
    )
$$;

CREATE FUNCTION nutrition.coaching_due(at_time timestamptz, policy text)
RETURNS TABLE(owner_id uuid, day date) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
    SELECT p.user_id,(at_time AT TIME ZONE u.tz)::date
    FROM nutrition.user_goal_profiles p
    JOIN public.users u ON u.id=p.user_id
    JOIN nutrition.n2r_profiles n USING(user_id)
    WHERE (policy='' OR policy=(SELECT c.policy_version FROM public.consents c
        WHERE c.user_id=p.user_id AND c.kind='health_processing' AND c.revoked_at IS NULL
        ORDER BY c.granted_at DESC LIMIT 1))
    AND NOT EXISTS (SELECT FROM nutrition.daily_energy_states s WHERE s.user_id=p.user_id AND s.day=(at_time AT TIME ZONE u.tz)::date
        AND s.captured_at>at_time-interval '2 hours')
    ORDER BY p.user_id LIMIT 100
$$;

CREATE FUNCTION nutrition.evaluation_cohorts(since date)
RETURNS TABLE(dimension text,cohort text,accounts bigint,predictions bigint,mae double precision,bias double precision,
    mape double precision,hrv_correlation double precision,brier double precision,attribution_precision double precision,recovery double precision)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
    WITH expanded AS (
        SELECT e.*,c.dimension,c.cohort,
            e.n2r_score-avg(e.n2r_score) FILTER(WHERE e.hrv_delta IS NOT NULL) OVER subject AS score_centered,
            e.hrv_delta-avg(e.hrv_delta) FILTER(WHERE e.n2r_score IS NOT NULL) OVER subject AS hrv_centered,
            count(*) FILTER(WHERE e.n2r_score IS NOT NULL AND e.hrv_delta IS NOT NULL) OVER subject AS paired
        FROM nutrition.model_evaluation_logs e
        CROSS JOIN LATERAL (VALUES ('all','all'),('goal',e.goal),('wearable',e.workout_source),('sport',e.sport),('model',e.model_version)) c(dimension,cohort)
        WHERE e.day>=greatest(since,current_date-90)
        WINDOW subject AS (PARTITION BY e.user_id,c.dimension,c.cohort)
    )
    SELECT e.dimension,e.cohort,count(DISTINCT e.user_id),count(*),
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE energy_bias IS NOT NULL)>=10 THEN avg(abs(energy_bias)) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE energy_bias IS NOT NULL)>=10 THEN avg(energy_bias) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE weight_ape IS NOT NULL)>=10 THEN avg(weight_ape) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE paired>=3 AND n2r_score IS NOT NULL AND hrv_delta IS NOT NULL)>=10
             THEN corr(score_centered,hrv_centered) FILTER(WHERE paired>=3) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE red_probability IS NOT NULL AND red_outcome IS NOT NULL)>=10
             THEN avg(power(red_probability-red_outcome::integer,2)) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE confounder_attributed AND reported_confounder IS NOT NULL)>=10
             THEN avg(reported_confounder::integer) FILTER(WHERE confounder_attributed) END,
        CASE WHEN count(DISTINCT e.user_id) FILTER(WHERE intervention_recovered IS NOT NULL)>=10
             THEN avg(intervention_recovered::integer) END
    FROM expanded e
    GROUP BY e.dimension,e.cohort HAVING count(DISTINCT e.user_id)>=10 ORDER BY e.dimension,e.cohort
$$;

REVOKE ALL ON FUNCTION nutrition.enqueue_wearable(text,text,bytea,jsonb,text),nutrition.claim_wearable(timestamptz,text),
    nutrition.finish_wearable(bigint,smallint,timestamptz,boolean),nutrition.purge_wearable(timestamptz),nutrition.coaching_due(timestamptz,text),
    nutrition.evaluation_cohorts(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.enqueue_wearable(text,text,bytea,jsonb,text),nutrition.claim_wearable(timestamptz,text),
    nutrition.finish_wearable(bigint,smallint,timestamptz,boolean),nutrition.purge_wearable(timestamptz),nutrition.coaching_due(timestamptz,text),
    nutrition.evaluation_cohorts(date) TO ryft_api;

CREATE TABLE nutrition.wearable_weights (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    provider text NOT NULL CHECK(provider IN ('garmin','suunto')),
    at_time timestamptz NOT NULL,
    mass_kg double precision NOT NULL CHECK(mass_kg BETWEEN 25 AND 350),
    PRIMARY KEY(user_id,provider,at_time)
);
CREATE TABLE nutrition.wearable_activity (
    user_id uuid NOT NULL REFERENCES nutrition.user_goal_profiles(user_id) ON DELETE CASCADE,
    provider text NOT NULL CHECK(provider IN ('garmin','suunto')),
    slot timestamptz NOT NULL,
    active_kcal double precision NOT NULL CHECK(active_kcal >= 0),
    PRIMARY KEY(user_id,provider,slot)
);
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['wearable_weights','wearable_activity']
    LOOP
        EXECUTE format('ALTER TABLE nutrition.%I ENABLE ROW LEVEL SECURITY',t);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE ON nutrition.%I TO ryft_api',t);
        EXECUTE format('CREATE POLICY coaching_owner ON nutrition.%I TO ryft_api USING (user_id=nutrition.account()) WITH CHECK (user_id=nutrition.account())',t);
    END LOOP;
END $$;
