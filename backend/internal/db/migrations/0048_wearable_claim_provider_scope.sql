-- D272: apply the revision's configured providers before locking or attempting a job.
CREATE FUNCTION nutrition.claim_wearable(at_time timestamptz, policy text, allowed_providers text[])
RETURNS TABLE(job_id bigint, src text, owner_id uuid, payload jsonb, attempt smallint)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    WITH next AS (
        SELECT i.id,l.user_id FROM nutrition.wearable_inbox i
        JOIN nutrition.wearable_links l USING(provider,provider_user)
        WHERE i.completed_at IS NULL AND i.due_at<=at_time AND i.attempts<5
        AND i.provider=ANY(allowed_providers)
        AND EXISTS (SELECT FROM nutrition.n2r_profiles n WHERE n.user_id=l.user_id)
        AND (policy='' OR policy=(SELECT c.policy_version FROM public.consents c
            WHERE c.user_id=l.user_id AND c.kind='health_processing' AND c.revoked_at IS NULL
            ORDER BY c.granted_at DESC LIMIT 1))
        ORDER BY i.due_at,i.id LIMIT 1 FOR UPDATE OF i SKIP LOCKED
    )
    UPDATE nutrition.wearable_inbox i SET due_at=at_time+interval '15 minutes',attempts=i.attempts+1
    FROM next WHERE i.id=next.id RETURNING i.id,i.provider,next.user_id,i.body,i.attempts
$$;
REVOKE ALL ON FUNCTION nutrition.claim_wearable(timestamptz,text,text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.claim_wearable(timestamptz,text,text[]) TO ryft_api;

-- Keep the previous call and its behavior for compatibility with older revisions.
CREATE OR REPLACE FUNCTION nutrition.claim_wearable(at_time timestamptz, policy text)
RETURNS TABLE(job_id bigint, src text, owner_id uuid, payload jsonb, attempt smallint)
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    SELECT * FROM nutrition.claim_wearable(at_time,policy,ARRAY['garmin','suunto']::text[])
$$;
