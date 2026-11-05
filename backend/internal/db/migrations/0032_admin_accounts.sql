-- Admin sign-in by Supabase token (docs/work/2026-11-05-admin-web/design.md §3.3, §4.4; D153, D157).
-- Additive: one new table and one nullable column with a CHECK that every row an image from before
-- this migration writes already satisfies; nothing is renamed, retyped or dropped, so rollback is
-- re-pinning the old images (the table and the column stay and nothing reads them).
--
-- Locks: ADD COLUMN without a default is a catalog change that rewrites nothing, but it takes ACCESS
-- EXCLUSIVE on admin_audit until the commit (the migrator runs the file in one transaction, db.go).
-- lock_timeout bounds the wait (D116). The CHECK is added NOT VALID and validated in the same
-- transaction (as 0031): a short scan of a small table.
SET LOCAL lock_timeout = '5s';

-- The allow-list of operator accounts. A Supabase access token carrying app_metadata.role "admin"
-- opens /v1/admin/* only for a user with a row here (and no tombstone in deleted_users), so a
-- revocation takes effect on the next request instead of when the token expires. Operator
-- configuration, not user data: no foreign key (an operator account need not have a users row), and
-- deliberately not part of account deletion; the api's tombstone check covers a deleted operator.
-- Only the owner writes it (the SQL editor, as postgres); the api can read it and nothing else.
CREATE TABLE IF NOT EXISTS admin_accounts (
    user_id  uuid PRIMARY KEY,
    note     text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    added_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT ON admin_accounts TO ryft_api;

DROP POLICY IF EXISTS admin_accounts_services ON admin_accounts;
CREATE POLICY admin_accounts_services ON admin_accounts FOR SELECT TO ryft_api USING (true);

-- Who made an admin call. key_fingerprint stays NOT NULL (an image from before this migration scans
-- it into a Go string, and a NULL would fail GET /v1/admin/audit after a rollback): an admin-token
-- call stores '' there and its user id in actor_user, a key call the other way round. Rows from
-- before have a fingerprint and no actor_user, so they satisfy the CHECK.
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS actor_user uuid;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'admin_audit_one_actor' AND conrelid = 'admin_audit'::regclass) THEN
        ALTER TABLE admin_audit ADD CONSTRAINT admin_audit_one_actor
            CHECK ((actor_user IS NULL) = (key_fingerprint <> '')) NOT VALID;
    END IF;
END $$;
ALTER TABLE admin_audit VALIDATE CONSTRAINT admin_audit_one_actor;

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles. (admin_audit has both since
-- 0022; its grants for ryft_api, SELECT and INSERT, cover the new column.)
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE admin_accounts ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON admin_accounts FROM anon, authenticated, service_role;
END $$;
