-- Sign in with Apple refresh tokens (POST /v1/me/apple-authorization), kept only to
-- revoke them when the account is deleted, as Apple requires (docs/DECISIONS.md D48).
-- The api encrypts them (AES-256-GCM, key in its Vault namespace only, the user ID
-- as associated data); the table is its own so that only ryft_api can read it.
CREATE TABLE IF NOT EXISTS apple_tokens (
    user_id    uuid PRIMARY KEY REFERENCES users(id),
    token_enc  bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- On deletion the encrypted token moves to the tombstone until Apple confirms the
-- revocation; the api's deletion reconciler retries from there.
ALTER TABLE deleted_users
    ADD COLUMN IF NOT EXISTS apple_token_enc  bytea,
    ADD COLUMN IF NOT EXISTS apple_revoked_at timestamptz;

GRANT SELECT, INSERT, UPDATE, DELETE ON apple_tokens TO ryft_api;

-- ingest only checks whether an ID is tombstoned: the id column, not the token.
REVOKE SELECT ON deleted_users FROM ryft_ingest;
GRANT SELECT (id) ON deleted_users TO ryft_ingest;

DROP POLICY IF EXISTS apple_tokens_services ON apple_tokens;
CREATE POLICY apple_tokens_services ON apple_tokens TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE apple_tokens ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON apple_tokens FROM anon, authenticated, service_role;
END $$;
