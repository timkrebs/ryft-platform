-- Record the production smoke account's consent for a privacy policy version (D286).
--
-- WHO RUNS IT: the operator, in the Supabase SQL editor of the production project as `postgres` (the owner).
--   The smoke bearer cannot write consent (POST/DELETE /v1/me/consent are not on its allowlist), so this
--   file is the only way the smoke account consents to a new version.
-- BEFORE: the smoke account is bound (scripts/sql/bind_smoke_account.sql). Run this whenever
--   PRIVACY_POLICY_VERSION moves (deploy/run/config.env), before or right after the deploy that ships it:
--   until then the smoke's health-data requests answer with a missing consent.
-- HOW: replace the one marked literal (<PRIVACY_POLICY_VERSION>) with the new version (YYYY-MM-DD, the
--   value of PRIVACY_POLICY_VERSION), paste the WHOLE file, run it. The editor sends the file as one
--   query, which Postgres runs as one transaction: all or nothing. In psql use
--   `psql --single-transaction -f` (plain `-f` would run each statement on its own). A rerun is safe.
-- EXPECT: the last statement returns exactly `1 row`: the account's current consent, with policy_version
--   equal to the version you pasted and revoked_at empty. Any refusal raises an error that says why, and
--   nothing is changed.
--
-- The consent is recorded only when ALL of these hold:
--   1. the version is a real date written YYYY-MM-DD (the placeholder left in is refused);
--   2. public.smoke_account holds a row (an unbound smoke is refused; this file never picks an account);
--   3. that account is in auth.users and its email is smoke@ryftlab.de (v_email below; compared without
--      regard to case): a wrong binding never receives a consent.
-- It acts only on the account in public.smoke_account. It adds one `health_processing` consent row for the
-- version, exactly as the app's own grant does (backend/internal/store/beta.go), under the same per-user
-- lock; when an unrevoked row for that version already exists nothing is added (its granted_at stays).
-- Consents for older versions are left as they are, as the app leaves them.

DO $consent$
DECLARE
    -- >>> REPLACE the text between the quotes with the new PRIVACY_POLICY_VERSION (YYYY-MM-DD). <<<
    v_version text := '<PRIVACY_POLICY_VERSION>';
    v_email   constant text := 'smoke@ryftlab.de';
    v_id      uuid;
    v_mail    text;
    v_n       integer;
BEGIN
    IF v_version !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN
        RAISE EXCEPTION 'smoke consent refused: the version % is not YYYY-MM-DD', v_version;
    END IF;
    BEGIN
        IF to_char(v_version::date, 'YYYY-MM-DD') <> v_version THEN
            RAISE EXCEPTION 'smoke consent refused: the version % is not a real date', v_version;
        END IF;
    EXCEPTION WHEN datetime_field_overflow OR invalid_datetime_format THEN
        RAISE EXCEPTION 'smoke consent refused: the version % is not a real date', v_version;
    END;

    SELECT user_id INTO v_id FROM public.smoke_account;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'smoke consent refused: public.smoke_account is empty, no smoke account is bound (run bind_smoke_account.sql first)';
    END IF;
    SELECT count(*), min(email) INTO v_n, v_mail FROM auth.users WHERE id = v_id;
    IF v_n = 0 THEN
        RAISE EXCEPTION 'smoke consent refused: the bound account % is not in auth.users', v_id;
    END IF;
    IF v_mail IS NULL OR lower(v_mail) <> lower(v_email) THEN
        RAISE EXCEPTION 'smoke consent refused: the bound account % does not have the email % in auth.users', v_id, v_email;
    END IF;

    -- The lock of the app's own GrantConsent (store.lockApply): a concurrent grant waits for this one.
    PERFORM pg_advisory_xact_lock(7419005, hashtext(v_id::text));
    INSERT INTO public.consents (user_id, kind, policy_version, app_version)
    SELECT v_id, 'health_processing', v_version, 'operator-sql'
    WHERE NOT EXISTS (SELECT 1 FROM public.consents WHERE user_id = v_id AND kind = 'health_processing'
                        AND revoked_at IS NULL AND policy_version = v_version);
END
$consent$;

-- The smoke account's current consent: one row.
SELECT c.user_id, c.policy_version, c.granted_at, c.revoked_at, c.app_version
  FROM public.consents c
 WHERE c.user_id = (SELECT user_id FROM public.smoke_account)
   AND c.kind = 'health_processing' AND c.revoked_at IS NULL
 ORDER BY c.granted_at DESC, c.id DESC
 LIMIT 1;
