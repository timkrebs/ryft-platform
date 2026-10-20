-- Sources (Beta, docs/DECISIONS.md D82): which app or device writes which kind of
-- data, for User.sources and the no-heart-rate-source card, kept by ingest on every
-- sync; and the app and OS version of each device, for support.
CREATE TABLE IF NOT EXISTS user_sources (
    user_id   uuid NOT NULL REFERENCES users(id),
    name      text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),   -- the HealthKit source name
    kind      text NOT NULL CHECK (kind IN ('energy', 'hr', 'hrv', 'resp', 'sleep', 'spo2', 'steps', 'workout')),
    bundle_id text CHECK (length(bundle_id) <= 255),
    last_seen timestamptz NOT NULL,                                    -- the latest record's end
    PRIMARY KEY (user_id, name, kind)
);

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS app_version text CHECK (length(app_version) <= 64),
    ADD COLUMN IF NOT EXISTS os_version  text CHECK (length(os_version) <= 64);

-- What the last 30 days already show (bundle ids were never sent before).
INSERT INTO user_sources (user_id, name, kind, last_seen)
SELECT user_id, source, CASE type
         WHEN 'heart_rate' THEN 'hr' WHEN 'resting_hr' THEN 'hr' WHEN 'vo2max' THEN 'hr'
         WHEN 'hrv_sdnn' THEN 'hrv' WHEN 'respiratory_rate' THEN 'resp' WHEN 'spo2' THEN 'spo2'
         WHEN 'active_energy' THEN 'energy' WHEN 'steps' THEN 'steps' END, max(end_at)
  FROM samples
 WHERE end_at > now() - interval '30 days' AND source <> '' AND length(source) <= 200
   AND type IN ('heart_rate', 'resting_hr', 'vo2max', 'hrv_sdnn', 'respiratory_rate', 'spo2', 'active_energy', 'steps')
 GROUP BY 1, 2, 3
UNION ALL
SELECT user_id, source, 'sleep', max(end_at) FROM sleep_segments
 WHERE end_at > now() - interval '30 days' AND source <> '' AND length(source) <= 200 GROUP BY 1, 2
UNION ALL
SELECT user_id, source, 'workout', max(end_at) FROM workouts
 WHERE end_at > now() - interval '30 days' AND source <> '' AND length(source) <= 200 GROUP BY 1, 2
ON CONFLICT DO NOTHING;

-- ingest keeps the table, api shows it and deletes it with the account.
GRANT SELECT, INSERT, UPDATE ON user_sources TO ryft_ingest;
GRANT SELECT, DELETE ON user_sources TO ryft_api;

DROP POLICY IF EXISTS user_sources_services ON user_sources;
CREATE POLICY user_sources_services ON user_sources TO ryft_ingest, ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE user_sources ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON user_sources FROM anon, authenticated, service_role;
END $$;
