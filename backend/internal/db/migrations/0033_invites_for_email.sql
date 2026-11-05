-- Invites for a person: a code bound to an email, a revoke that keeps the row, and who redeemed it
-- (docs/work/2026-11-05-admin-web/design.md §6.1; D159).
-- Additive: three columns on beta_invite_codes, one new table, nothing renamed, retyped or dropped. An
-- image from before this migration keeps working against the new schema (rollback = re-pin the old
-- images; the columns and the table stay and nothing reads them):
--   - its INSERT into beta_invite_codes names no new column: created_by has a DEFAULT, email and
--     revoked_at are nullable;
--   - its hard DELETE of a code removes the code's beta_invite_uses rows (ON DELETE CASCADE);
--   - its DeleteUser knows nothing of beta_invite_uses, so the rows of the account go with the account
--     (ON DELETE CASCADE on user_id too; without it the erasure of an account that redeemed a code, and
--     the backfill below creates a row for every such account, would fail on the foreign key);
--   - a revoke sets expires_at (cmd/api), so it refuses a revoked code without knowing revoked_at.
--
-- Locks: ADD COLUMN takes ACCESS EXCLUSIVE on beta_invite_codes until the commit (the migrator runs the
-- file in one transaction, db.go). NOT NULL DEFAULT <constant> rewrites nothing (PG >= 11). lock_timeout
-- bounds the wait (D116). The CHECKs are added NOT VALID and validated in the same transaction (as
-- 0031/0032): a short scan of a table of a few dozen rows.
SET LOCAL lock_timeout = '5s';

ALTER TABLE beta_invite_codes
    ADD COLUMN IF NOT EXISTS email      text,                        -- NULL: anyone with the code
    ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT 'key', -- the default is the backfill ('key': the fingerprint was not kept)
                                                                     -- and what an older image's INSERT stores
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz;

-- email: stored lower-cased and printable ASCII (cmd/api validates and normalises before the INSERT;
-- this is the backstop). A code for one email is single use. The purge (PurgeInviteEmails) sets email to
-- NULL, which both CHECKs allow.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'beta_invite_codes_email_form' AND conrelid = 'beta_invite_codes'::regclass) THEN
        ALTER TABLE beta_invite_codes ADD CONSTRAINT beta_invite_codes_email_form
            CHECK (email IS NULL OR (email = lower(email) AND length(email) BETWEEN 3 AND 254
                   AND email ~ '^[!-~]+@[!-~]+\.[!-~]+$' AND email !~ '@.*@')) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'beta_invite_codes_created_by_form' AND conrelid = 'beta_invite_codes'::regclass) THEN
        ALTER TABLE beta_invite_codes ADD CONSTRAINT beta_invite_codes_created_by_form
            CHECK (created_by ~ '^(key|key:[0-9a-f]{8}|user:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$') NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'beta_invite_codes_bound_single_use' AND conrelid = 'beta_invite_codes'::regclass) THEN
        ALTER TABLE beta_invite_codes ADD CONSTRAINT beta_invite_codes_bound_single_use
            CHECK (email IS NULL OR max_uses = 1) NOT VALID;
    END IF;
END $$;
ALTER TABLE beta_invite_codes VALIDATE CONSTRAINT beta_invite_codes_email_form;
ALTER TABLE beta_invite_codes VALIDATE CONSTRAINT beta_invite_codes_created_by_form;
ALTER TABLE beta_invite_codes VALIDATE CONSTRAINT beta_invite_codes_bound_single_use;

-- Who redeemed which code. A code is redeemed at most once per account (the primary key).
CREATE TABLE IF NOT EXISTS beta_invite_uses (
    code    text NOT NULL REFERENCES beta_invite_codes(code) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (code, user_id)
);
CREATE INDEX IF NOT EXISTS beta_invite_uses_user ON beta_invite_uses (user_id);

-- Who redeemed before this migration: users.beta_invite_code holds the stored form (RYFT7K3MQX9P) and
-- beta_access_at the time. A code deleted since has no row to join; an account that got access without a
-- code has no beta_invite_code. Safe to run again.
INSERT INTO beta_invite_uses (code, user_id, at)
SELECT u.beta_invite_code, u.id, COALESCE(u.beta_access_at, now())
FROM users u JOIN beta_invite_codes c ON c.code = u.beta_invite_code
ON CONFLICT DO NOTHING;

-- Only the api sees it, and merge moves rows (UPDATE).
GRANT SELECT, INSERT, UPDATE, DELETE ON beta_invite_uses TO ryft_api;

DROP POLICY IF EXISTS beta_invite_uses_services ON beta_invite_uses;
CREATE POLICY beta_invite_uses_services ON beta_invite_uses TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007 and 0017: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE beta_invite_uses ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON beta_invite_uses FROM anon, authenticated, service_role;
END $$;
