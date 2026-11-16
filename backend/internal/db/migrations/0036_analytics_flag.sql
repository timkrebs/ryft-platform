-- The app's product analytics (docs/DECISIONS.md D231): the flag analytics.enabled ships dark, enabled at
-- 0 %, so the allow-list turns it on for testers (as train.import, 0028). GET /v1/config serves it beside
-- analytics (the PostHog host and project key, null until the api has the key); the app combines the two
-- with the user's opt-in. Never overwritten: a later change through PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('analytics.enabled', true, 0)
ON CONFLICT (key) DO NOTHING;
