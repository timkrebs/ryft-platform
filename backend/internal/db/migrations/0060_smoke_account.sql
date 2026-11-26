-- The production smoke's one account (docs/DECISIONS.md D286; docs/work/2026-11-26-smoke-auth/plan.md, Contract v2).
-- The smoke signs in with a Google token minted for the service account ryft-smoke; the api and ingest map it to
-- the user id config.env names (SMOKE_USER_ID). That id is configuration, so a UUID copied from a real account
-- would let whoever can mint a smoke token act as that user. This table is the database's own, independent
-- statement of which single account may be the smoke: ensureUser (api, ingest) serves a smoke principal only
-- when its user id is the row here, and answers 401 smoke_unbound otherwise (store.IsSmokeAccount).
--
-- Only an operator writes it: scripts/sql/bind_smoke_account.sql, in the Supabase SQL editor as postgres. It
-- binds an account that exists, has no sign-in other than email and is not tombstoned. No service role has
-- INSERT, UPDATE or DELETE here, and the policy below is SELECT only, so a compromised service cannot bind
-- itself an account. Rebinding is an explicit DELETE and INSERT by the operator.
--
-- Shape: at most one row (singleton is the primary key and must be true), one account (user_id is unique),
-- and the row goes with the account (ON DELETE CASCADE, as app_attest_keys in 0037): DeleteUser and
-- MergeUsers remove the users row, the referential action removes the binding as the table owner, so the
-- service roles need no DELETE. The table is operator metadata, not the user's data: the export leaves it
-- out (it lists no user-visible content, only that this account is the smoke), and it holds no health data.
--
-- Additive: one new table, nothing renamed, retyped or dropped. An image from before this migration never
-- reads it (rollback = re-pin the old images; the table stays). The table is empty after the migration, so
-- every smoke request fails closed (401 smoke_unbound) until the operator binds the account.
--
-- Locks: CREATE TABLE with a foreign key to users takes SHARE ROW EXCLUSIVE on users until the commit (the
-- migrator runs the file in one transaction, db.go): inserts and updates of users wait, reads don't. Nothing
-- else runs under it (the table is new and empty). lock_timeout bounds the wait to get it (D116), written out
-- as in 0027-0037.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS smoke_account (
    singleton boolean     PRIMARY KEY DEFAULT true CONSTRAINT smoke_account_singleton CHECK (singleton),
    user_id   uuid        NOT NULL CONSTRAINT smoke_account_user_id_key UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    bound_at  timestamptz NOT NULL DEFAULT now()
);

-- Read by the two services that take a smoke token. Never written by a service: no INSERT/UPDATE/DELETE.
GRANT SELECT ON smoke_account TO ryft_api, ryft_ingest;

-- On Supabase RLS applies to the service roles (they do not own the tables, 0009), so the read needs a
-- policy; FOR SELECT only, so even a later over-broad grant could not let a service write. On plain Postgres
-- RLS is off and the policy is inert.
-- Named <table>_services like every service policy (0009 on; admin_accounts_services in 0032 is SELECT only too).
DROP POLICY IF EXISTS smoke_account_services ON smoke_account;
CREATE POLICY smoke_account_services ON smoke_account FOR SELECT TO ryft_api, ryft_ingest USING (true);

-- Supabase only, as in 0007, 0017, 0033 and 0037: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE smoke_account ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON smoke_account FROM anon, authenticated, service_role;
END $$;
