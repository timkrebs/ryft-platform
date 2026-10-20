-- Beta (external TestFlight, docs/DECISIONS.md D78–D79): consent to health-data
-- processing (GDPR Art. 9(2)(a): explicit, recorded, versioned, revocable) and the
-- invite gate.

-- Every grant and every withdrawal stays: a row is one consent to one policy
-- version; withdrawing sets revoked_at. The current consent is the latest row
-- without revoked_at.
CREATE TABLE IF NOT EXISTS consents (
    id             bigserial PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id),
    kind           text NOT NULL CHECK (kind IN ('health_processing')),
    policy_version text NOT NULL CHECK (length(policy_version) BETWEEN 1 AND 32),
    granted_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at     timestamptz,
    app_version    text CHECK (length(app_version) <= 64)
);
CREATE INDEX IF NOT EXISTS consents_current ON consents (user_id, kind, granted_at DESC) WHERE revoked_at IS NULL;

-- Invite codes, stored without dashes in upper case (RYFT7K3MQX9P); shown as
-- RYFT-7K3M-QX9P.
CREATE TABLE IF NOT EXISTS beta_invite_codes (
    code       text PRIMARY KEY CHECK (code ~ '^RYFT[2-9A-HJ-NP-Z]{8}$'),
    note       text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    max_uses   int NOT NULL DEFAULT 1 CHECK (max_uses BETWEEN 1 AND 1000),
    uses       int NOT NULL DEFAULT 0 CHECK (uses >= 0 AND uses <= max_uses),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Access to the beta. Every account that exists now keeps it (the user, the
-- smoke and test accounts); new ones need a code while BETA_GATE is on.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS beta_access_at   timestamptz,
    ADD COLUMN IF NOT EXISTS beta_invite_code text;
UPDATE users SET beta_access_at = now() WHERE beta_access_at IS NULL;

-- api writes consents and redeems codes; ingest checks the consent before a
-- sync; scoring loads the user row (store.GetUser), which reads the current
-- consent too. Nobody but the api sees the codes.
GRANT SELECT, INSERT, UPDATE, DELETE ON consents TO ryft_api;
GRANT USAGE ON SEQUENCE consents_id_seq TO ryft_api;
GRANT SELECT ON consents TO ryft_ingest, ryft_scoring;
GRANT SELECT, INSERT, UPDATE, DELETE ON beta_invite_codes TO ryft_api;

DROP POLICY IF EXISTS consents_services ON consents;
CREATE POLICY consents_services ON consents TO ryft_ingest, ryft_scoring, ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS beta_invite_codes_services ON beta_invite_codes;
CREATE POLICY beta_invite_codes_services ON beta_invite_codes TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE consents ENABLE ROW LEVEL SECURITY;
    ALTER TABLE beta_invite_codes ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON consents, beta_invite_codes FROM anon, authenticated, service_role;
    REVOKE ALL ON SEQUENCE consents_id_seq FROM anon, authenticated, service_role;
END $$;
