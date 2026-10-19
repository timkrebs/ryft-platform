-- Push (M7 core, docs/DECISIONS.md D70–D73): the app's APNs tokens, what notify
-- sent, and each user's notification preferences. The new service notify gets
-- its own least-privilege role, like the others (0009).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ryft_notify') THEN
        BEGIN
            CREATE ROLE ryft_notify NOLOGIN;
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            NULL;
        END;
    END IF;
END $$;
GRANT USAGE ON SCHEMA public TO ryft_notify;
GRANT SELECT ON schema_migrations TO ryft_notify;

-- One token per app install (device) and user. A token belongs to one install,
-- so registering it for another user or device moves it (the api deletes the
-- old row first): a shared iPhone never shows the previous account's scores.
CREATE TABLE IF NOT EXISTS push_tokens (
    user_id    uuid NOT NULL REFERENCES users(id),
    device_id  uuid NOT NULL,
    token      text NOT NULL UNIQUE CHECK (token ~ '^[0-9a-f]{64,200}$'),
    env        text NOT NULL CHECK (env IN ('sandbox', 'production')),
    locale     text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'de')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id)
);

-- One row per user, day and category: notify claims the row before it sends, so
-- a redelivered event or a second pod never sends twice (D72).
CREATE TABLE IF NOT EXISTS notifications_sent (
    user_id  uuid NOT NULL REFERENCES users(id),
    day      date NOT NULL,
    category text NOT NULL,
    sent_at  timestamptz NOT NULL DEFAULT now(),
    devices  int NOT NULL DEFAULT 0, -- tokens APNs accepted it for
    PRIMARY KEY (user_id, day, category)
);

-- Preferences (PATCH /v1/me/notifications): a category missing from the map is
-- on; quiet hours are local times, both or neither, and may wrap midnight.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS notify_categories jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS quiet_from        time,
    ADD COLUMN IF NOT EXISTS quiet_to          time;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_quiet_hours_pair') THEN
        ALTER TABLE users ADD CONSTRAINT users_quiet_hours_pair CHECK ((quiet_from IS NULL) = (quiet_to IS NULL));
    END IF;
END $$;

-- api: registers and removes tokens, erases them and the send log with the account.
GRANT SELECT, INSERT, UPDATE, DELETE ON push_tokens TO ryft_api;
GRANT SELECT, DELETE ON notifications_sent TO ryft_api;

-- notify: reads what a message needs and whom to send it to, claims and records
-- sends, and drops tokens APNs reports as gone. Nothing else of a user.
GRANT SELECT (id, tz, notify_categories, quiet_from, quiet_to) ON users TO ryft_notify;
GRANT SELECT, DELETE ON push_tokens TO ryft_notify;
GRANT SELECT, INSERT, UPDATE, DELETE ON notifications_sent TO ryft_notify;
GRANT SELECT (user_id, last_sync_at) ON devices TO ryft_notify;
GRANT SELECT (user_id, type, start_at) ON samples TO ryft_notify;
GRANT SELECT (user_id, day, recovery, readiness, state, sleep_performance) ON daily_scores TO ryft_notify;
GRANT SELECT (user_id, id, title, started_at, ended_at, status, strain, muscular_trimp) ON sessions TO ryft_notify;
GRANT SELECT ON personal_records TO ryft_notify;
GRANT SELECT (id, name_en, name_de) ON exercises TO ryft_notify;

DROP POLICY IF EXISTS push_tokens_services ON push_tokens;
CREATE POLICY push_tokens_services ON push_tokens TO ryft_api, ryft_notify USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS notifications_sent_services ON notifications_sent;
CREATE POLICY notifications_sent_services ON notifications_sent TO ryft_api, ryft_notify USING (true) WITH CHECK (true);

-- The existing tables' service policies (0009, 0011) name the roles they admit;
-- notify reads through policies of its own (on Supabase RLS is on everywhere).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['users', 'devices', 'samples', 'daily_scores', 'sessions', 'personal_records',
                             'exercises', 'schema_migrations'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_notify_read', t);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT TO ryft_notify USING (true)', t || '_notify_read', t);
    END LOOP;
END $$;

-- Supabase only, as in 0007: RLS on, own rows for the Data API's signed-in
-- users would be wrong here (the app never reads these through it), so nothing.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE push_tokens ENABLE ROW LEVEL SECURITY;
    ALTER TABLE notifications_sent ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON push_tokens, notifications_sent FROM anon, authenticated, service_role;
END $$;
