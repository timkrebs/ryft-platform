-- Operator actions (docs/DECISIONS.md D88): every authorized /v1/admin call, who made
-- it (the admin key's fingerprint, never the key), what it was (the route pattern),
-- on whom, and how it ended. No foreign key on target_user: the record outlives a
-- deleted or merged account (it holds the pseudonymous id only, no email).
CREATE TABLE IF NOT EXISTS admin_audit (
    id              bigserial PRIMARY KEY,
    at              timestamptz NOT NULL DEFAULT now(),
    key_fingerprint text NOT NULL CHECK (length(key_fingerprint) <= 16),
    action          text NOT NULL CHECK (length(action) <= 120),
    target_user     uuid,
    status          int NOT NULL,
    request_id      text CHECK (length(request_id) <= 64),
    details         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX IF NOT EXISTS admin_audit_at ON admin_audit (at DESC);
CREATE INDEX IF NOT EXISTS admin_audit_target ON admin_audit (target_user, at DESC) WHERE target_user IS NOT NULL;

-- Only the api writes and reads it, append-only: no UPDATE, no DELETE.
GRANT SELECT, INSERT ON admin_audit TO ryft_api;
GRANT USAGE ON SEQUENCE admin_audit_id_seq TO ryft_api;

DROP POLICY IF EXISTS admin_audit_services ON admin_audit;
CREATE POLICY admin_audit_services ON admin_audit TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE admin_audit ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON admin_audit FROM anon, authenticated, service_role;
    REVOKE ALL ON SEQUENCE admin_audit_id_seq FROM anon, authenticated, service_role;
END $$;
