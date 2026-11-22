-- The app's local reminders (docs/DECISIONS.md D252): the flag notifications.reminders gates the two the phone
-- schedules itself, the morning journal reminder and the "not syncing" fallback. It ships dark, enabled at 0 %,
-- so the allow-list turns it on for testers (as train.import, 0028, and analytics.enabled, 0036). GET /v1/config
-- serves it. Never overwritten: a later change through PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('notifications.reminders', true, 0)
ON CONFLICT (key) DO NOTHING;
