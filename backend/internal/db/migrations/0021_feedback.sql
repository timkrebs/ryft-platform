-- Feedback from the app (Beta, docs/DECISIONS.md D83): what the tester wrote and the
-- context to find it in the logs (request ids, versions, diagnostics). Deleted with
-- the account. resolved_at is the operator's: open feedback has none.
CREATE TABLE IF NOT EXISTS feedback (
    id           bigserial PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id),
    category     text NOT NULL DEFAULT 'other' CHECK (category IN ('bug', 'idea', 'data', 'other')),
    message      text NOT NULL CHECK (length(message) BETWEEN 1 AND 4000),
    app_version  text CHECK (length(app_version) <= 64),
    build        text CHECK (length(build) <= 64),
    device_model text CHECK (length(device_model) <= 64),
    os_version   text CHECK (length(os_version) <= 64),
    request_ids  text[] NOT NULL DEFAULT '{}' CHECK (cardinality(request_ids) <= 20),
    diagnostics  jsonb CHECK (diagnostics IS NULL OR jsonb_typeof(diagnostics) = 'object'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);
CREATE INDEX IF NOT EXISTS feedback_user_created ON feedback (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS feedback_created ON feedback (created_at DESC);

-- Only the api: the app writes it, the operator reads and resolves it there.
GRANT SELECT, INSERT, UPDATE, DELETE ON feedback TO ryft_api;
GRANT USAGE ON SEQUENCE feedback_id_seq TO ryft_api;

DROP POLICY IF EXISTS feedback_services ON feedback;
CREATE POLICY feedback_services ON feedback TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE feedback ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON feedback FROM anon, authenticated, service_role;
    REVOKE ALL ON SEQUENCE feedback_id_seq FROM anon, authenticated, service_role;
END $$;
