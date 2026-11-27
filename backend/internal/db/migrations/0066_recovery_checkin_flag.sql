-- Recovery without a watch (docs/DECISIONS.md D291–D295): the flag recovery.checkin ships dark, enabled at 0 %, so the
-- allow-list turns it on for testers only (D291: no percentage rollout without a new decision; as train.import,
-- 0028, and analytics.enabled, 0036). While it is off for an account the two writes and GET /v1/recovery/status
-- answer 404, and stored check-ins and ratings count nowhere (the kill switch); the reads and both DELETEs keep
-- working. Never overwritten: a later change through PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('recovery.checkin', true, 0)
ON CONFLICT (key) DO NOTHING;
