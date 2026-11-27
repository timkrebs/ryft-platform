-- D304, D307: POST /v1/sync stores basal_energy, heart_rate_recovery and body_mass for an account only while
-- this flag is on for it and its consent is to a policy version that names the types (model.NamesProfileBody).
-- That list is empty until the bundled privacy update, so the flag admits nothing yet. Seeded dark: enabled at
-- 0 %, so only the allow-list turns it on (as recovery.checkin, 0066). Never overwritten: a later change through
-- PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('sync.fitness_types', true, 0)
ON CONFLICT (key) DO NOTHING;

-- Ingest needs this flag for the calling account only, as healthkit_body_mass_rollout (0052): no grant on the
-- feature flag table, no other flag, no full allow-list.
CREATE OR REPLACE FUNCTION public.sync_fitness_types_rollout(owner_id uuid)
RETURNS TABLE(allowlisted boolean, rollout_pct smallint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
    SELECT owner_id=ANY(f.allow_user_ids), f.rollout_pct
    FROM public.feature_flags f WHERE f.key='sync.fitness_types' AND f.enabled
$$;
REVOKE ALL ON FUNCTION public.sync_fitness_types_rollout(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.sync_fitness_types_rollout(uuid) TO ryft_ingest;

-- Supabase only, as in 0065: its default privileges give the Data API roles EXECUTE on every new function in
-- public: not on this one.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    REVOKE ALL ON FUNCTION public.sync_fitness_types_rollout(uuid) FROM anon, authenticated, service_role;
END $$;
