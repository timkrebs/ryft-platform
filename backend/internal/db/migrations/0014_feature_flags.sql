-- Remote config (GET /v1/config, docs/DECISIONS.md D69): one row per flag. A user
-- gets a flag when it is enabled and either lists them in allow_user_ids or puts
-- them in the first rollout_pct buckets (internal/flags). enabled = false turns a
-- flag off for everyone, the allow-list included: the kill switch.
CREATE TABLE IF NOT EXISTS feature_flags (
    key            text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$' AND length(key) <= 64),
    enabled        boolean NOT NULL DEFAULT false,
    rollout_pct    smallint NOT NULL DEFAULT 0 CHECK (rollout_pct BETWEEN 0 AND 100),
    allow_user_ids uuid[] NOT NULL DEFAULT '{}',
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- The app's flags (app/CLAUDE_FOLLOWUP_IOS_STACK.md → Feature flags). What the
-- platform serves is on for everyone; the rest ships dark: enabled, 0 %, so an
-- allow-list turns it on for testers. Never overwritten: a later change through
-- PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES
    ('train.enabled',   true, 100),
    ('sleep.planner',   true, 100),
    ('today.redesign',  true, 0),
    ('coach.enabled',   true, 0),
    ('push.enabled',    true, 0),
    ('physiology.card', true, 0)
ON CONFLICT (key) DO NOTHING;

-- The api evaluates flags for the app and is the admin's write path.
GRANT SELECT, INSERT, UPDATE, DELETE ON feature_flags TO ryft_api;

DROP POLICY IF EXISTS feature_flags_services ON feature_flags;
CREATE POLICY feature_flags_services ON feature_flags TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE feature_flags ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON feature_flags FROM anon, authenticated, service_role;
END $$;
