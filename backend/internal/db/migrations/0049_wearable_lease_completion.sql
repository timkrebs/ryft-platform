-- Keep the current grant and link for the duration of the enqueue transaction.
CREATE OR REPLACE FUNCTION nutrition.enqueue_wearable(src text, ref text, checksum bytea, payload jsonb, policy text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $$
    INSERT INTO nutrition.wearable_inbox(provider,provider_user,digest,body)
    SELECT linked.provider,linked.provider_user,checksum,payload FROM (
        SELECT l.provider,l.provider_user FROM nutrition.wearable_links l
        WHERE l.provider=src AND l.provider_user=ref
          AND EXISTS (SELECT FROM nutrition.n2r_profiles n WHERE n.user_id=l.user_id)
          AND (policy='' OR policy=(SELECT c.policy_version FROM public.consents c
              WHERE c.user_id=l.user_id AND c.kind='health_processing' AND c.revoked_at IS NULL
              ORDER BY c.granted_at DESC LIMIT 1 FOR SHARE))
        FOR SHARE OF l
    ) linked
    ON CONFLICT DO NOTHING
$$;

-- D275: complete the exact lease and scrub the retained body in the domain write transaction.
CREATE FUNCTION nutrition.complete_wearable_lease(job bigint, attempt smallint, src text, ref text, at_time timestamptz)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
    PERFORM 1 FROM nutrition.wearable_links l
    WHERE l.provider=src AND l.provider_user=ref AND l.user_id=nutrition.account()
    FOR SHARE OF l;
    IF NOT FOUND THEN
        RETURN false;
    END IF;

    UPDATE nutrition.wearable_inbox i SET completed_at=at_time,body='{}'::jsonb
    WHERE i.id=job AND i.provider=src AND i.provider_user=ref
      AND i.attempts=attempt AND i.attempts>0 AND i.due_at>at_time
      AND i.completed_at IS NULL;
    RETURN FOUND;
END;
$$;
REVOKE ALL ON FUNCTION nutrition.complete_wearable_lease(bigint,smallint,text,text,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.complete_wearable_lease(bigint,smallint,text,text,timestamptz) TO ryft_api;

-- Keep replay markers but discard pre-withdrawal payloads before any later regrant.
CREATE FUNCTION nutrition.discard_revoked_wearable() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
    UPDATE nutrition.wearable_inbox i SET completed_at=NEW.revoked_at,body='{}'::jsonb
    FROM nutrition.wearable_links l
    WHERE i.provider=l.provider AND i.provider_user=l.provider_user
      AND l.user_id=NEW.user_id AND i.completed_at IS NULL;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION nutrition.discard_revoked_wearable() FROM PUBLIC;
CREATE TRIGGER discard_revoked_wearable AFTER UPDATE OF revoked_at ON public.consents
FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND NEW.kind='health_processing')
EXECUTE FUNCTION nutrition.discard_revoked_wearable();
