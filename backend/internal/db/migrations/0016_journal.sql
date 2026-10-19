-- Journal and behavior impact (M5, docs/DECISIONS.md D74–D77). Behaviors are a
-- catalog like the exercises (built-ins with user_id NULL and fixed ids, loaded by
-- the migrate job from internal/db/seed/behaviors.json; custom ones per user); a
-- user's choice overrides default_on; a journal day exists once it was saved, so a
-- day never saved is unknown, not "no". The impact is computed on request (D76):
-- no table for it.
CREATE TABLE IF NOT EXISTS behaviors (
    id          uuid PRIMARY KEY,
    user_id     uuid REFERENCES users(id),          -- NULL = built-in
    slug        text NOT NULL,
    name_en     text NOT NULL,
    name_de     text NOT NULL,
    category    text NOT NULL CHECK (category IN ('substances', 'sleep', 'recovery', 'supplements', 'health', 'lifestyle', 'custom')),
    kind        text NOT NULL CHECK (kind IN ('yes_no', 'count', 'time')),
    default_on  boolean NOT NULL DEFAULT false,
    sort        int NOT NULL DEFAULT 0,
    archived    boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS behaviors_builtin_slug ON behaviors (slug) WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS behaviors_user_slug ON behaviors (user_id, slug) WHERE user_id IS NOT NULL;

-- What the user turned on or off; without a row a behavior is default_on.
CREATE TABLE IF NOT EXISTS user_behaviors (
    user_id     uuid NOT NULL REFERENCES users(id),
    behavior_id uuid NOT NULL REFERENCES behaviors(id),
    enabled     boolean NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, behavior_id)
);

-- A day the user saved; its entries are exactly what was sent then.
CREATE TABLE IF NOT EXISTS journal_days (
    user_id     uuid NOT NULL REFERENCES users(id),
    day         date NOT NULL,
    answered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day)
);

CREATE TABLE IF NOT EXISTS journal_entries (
    user_id     uuid NOT NULL,
    day         date NOT NULL,
    behavior_id uuid NOT NULL REFERENCES behaviors(id),
    value_bool  boolean NOT NULL,
    value_num   double precision, -- count: how many; time: minutes after local midnight
    logged_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day, behavior_id),
    FOREIGN KEY (user_id, day) REFERENCES journal_days (user_id, day) ON DELETE CASCADE
);

-- api: the app's read and write side; it keeps built-ins read-only in code. No
-- other service reads the journal.
GRANT SELECT, INSERT, UPDATE, DELETE ON behaviors, user_behaviors, journal_days, journal_entries TO ryft_api;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['behaviors', 'user_behaviors', 'journal_days', 'journal_entries'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_services', t);
        EXECUTE format('CREATE POLICY %I ON %I TO ryft_api USING (true) WITH CHECK (true)', t || '_services', t);
    END LOOP;
END $$;

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE behaviors ENABLE ROW LEVEL SECURITY;
    ALTER TABLE user_behaviors ENABLE ROW LEVEL SECURITY;
    ALTER TABLE journal_days ENABLE ROW LEVEL SECURITY;
    ALTER TABLE journal_entries ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON behaviors, user_behaviors, journal_days, journal_entries FROM anon, authenticated, service_role;
END $$;
