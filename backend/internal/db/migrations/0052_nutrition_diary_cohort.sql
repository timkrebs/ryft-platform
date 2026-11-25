INSERT INTO public.feature_flags(key, enabled, rollout_pct)
VALUES ('nutrition.diary_enabled', false, 0) ON CONFLICT (key) DO NOTHING;

-- Ingest only needs this flag and the calling account's membership. Do not
-- grant it the feature flag table, the other flags, or any full allow-list.
CREATE FUNCTION public.healthkit_body_mass_rollout(owner_id uuid)
RETURNS TABLE(allowlisted boolean, rollout_pct smallint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
    SELECT owner_id=ANY(f.allow_user_ids), f.rollout_pct
    FROM public.feature_flags f WHERE f.key='nutrition.enabled' AND f.enabled
$$;
REVOKE ALL ON FUNCTION public.healthkit_body_mass_rollout(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.healthkit_body_mass_rollout(uuid) TO ryft_ingest;
