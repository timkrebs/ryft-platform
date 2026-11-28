-- The app's new structure (app #155: the new tabs, Profile behind the avatar, Quick log; D314): the flag
-- app.structure_v2 ships dark, enabled at 0 % with an empty allow-list, so it is off for every account until the
-- operator puts testers on the allow-list (as recovery.checkin, 0066). No endpoint checks it; the app does. Never
-- overwritten: a later change through PUT /v1/admin/flags/{key} stays. 0068–0071 are held by open PRs (#266–#271).
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('app.structure_v2', true, 0)
ON CONFLICT (key) DO NOTHING;
