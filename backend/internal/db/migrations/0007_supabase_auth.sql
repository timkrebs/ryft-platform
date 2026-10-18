-- Multi-user: users come from Supabase Auth (JWT sub) and are created lazily on
-- their first request. Runs on Supabase and on plain Postgres (compose, the
-- local-postgres overlay); the Supabase-only part is guarded at the bottom.

-- Profile fields from the token, refreshed at most every few minutes.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS email                  text,
    ADD COLUMN IF NOT EXISTS provider               text,
    ADD COLUMN IF NOT EXISTS last_seen_at           timestamptz,
    ADD COLUMN IF NOT EXISTS recompute_requested_at timestamptz;
ALTER TABLE users ALTER COLUMN name SET DEFAULT '';

-- Tombstones of deleted accounts (DELETE /v1/me). Only the UUID is kept: a
-- deleted user's access token stays valid until it expires and must not
-- recreate the account. auth_deleted_at is set once Supabase deleted the
-- auth user too.
CREATE TABLE IF NOT EXISTS deleted_users (
    id              uuid PRIMARY KEY,
    deleted_at      timestamptz NOT NULL DEFAULT now(),
    auth_deleted_at timestamptz
);

-- Device and workout IDs come from the phone (install UUID, HealthKit UUID).
-- One iPhone can be signed into by two accounts, so they are only unique per user.
ALTER TABLE devices  DROP CONSTRAINT IF EXISTS devices_pkey;
ALTER TABLE devices  ADD PRIMARY KEY (user_id, id);
ALTER TABLE workouts DROP CONSTRAINT IF EXISTS workouts_pkey;
ALTER TABLE workouts ADD PRIMARY KEY (user_id, id);

-- The scoring timer looks for users who synced recently.
CREATE INDEX IF NOT EXISTS devices_last_sync ON devices (last_sync_at);

-- The seeded single-tenant user (0001) goes away unless it ever received data;
-- in apikey mode it is recreated on its first request.
DO $$
DECLARE seed uuid := '00000000-0000-0000-0000-000000000001';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM samples WHERE user_id = seed)
       AND NOT EXISTS (SELECT 1 FROM sleep_segments WHERE user_id = seed)
       AND NOT EXISTS (SELECT 1 FROM workouts WHERE user_id = seed) THEN
        DELETE FROM daily_scores WHERE user_id = seed;
        DELETE FROM devices WHERE user_id = seed;
        DELETE FROM users WHERE id = seed;
    END IF;
END $$;

-- Supabase only: the services are the sole data API. Row-level security is
-- defence in depth for anyone reaching the tables through the Data API with a
-- user token, and no API role keeps table privileges at all. The services
-- connect as `postgres`, which owns these tables and therefore bypasses RLS
-- (never FORCE it). Skipped on plain Postgres, where auth.uid() and the roles
-- do not exist.
DO $$
DECLARE t text;
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RAISE NOTICE 'not a Supabase database: skipping RLS policies';
        RETURN;
    END IF;

    FOREACH t IN ARRAY ARRAY['devices', 'samples', 'sleep_segments', 'workouts', 'daily_scores'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY %I ON %I FOR ALL TO authenticated
                          USING (user_id = (SELECT auth.uid())) WITH CHECK (user_id = (SELECT auth.uid()))',
                       t || '_own_rows', t);
    END LOOP;
    ALTER TABLE users ENABLE ROW LEVEL SECURITY;
    CREATE POLICY users_own_row ON users FOR ALL TO authenticated
        USING (id = (SELECT auth.uid())) WITH CHECK (id = (SELECT auth.uid()));
    -- No policies: nobody but the owner reads these.
    ALTER TABLE deleted_users ENABLE ROW LEVEL SECURITY;
    ALTER TABLE schema_migrations ENABLE ROW LEVEL SECURITY;

    REVOKE ALL ON ALL TABLES    IN SCHEMA public FROM anon, authenticated, service_role;
    REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM anon, authenticated, service_role;
    REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM anon, authenticated, service_role;
    ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
        REVOKE ALL ON TABLES FROM anon, authenticated, service_role;
    ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
        REVOKE ALL ON SEQUENCES FROM anon, authenticated, service_role;
    ALTER DEFAULT PRIVILEGES FOR ROLE postgres IN SCHEMA public
        REVOKE ALL ON FUNCTIONS FROM anon, authenticated, service_role;
END $$;
