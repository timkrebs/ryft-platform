-- Sync state the app can trust (docs/DECISIONS.md D91): which days a sync queued for
-- scoring and when scoring finished them (GET /v1/me/sync → pending_days, the
-- sync-to-score latency), and the operator's request for a full re-sync.
CREATE TABLE IF NOT EXISTS score_queue (
    user_id   uuid NOT NULL REFERENCES users(id),
    day       date NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    scored_at timestamptz,                         -- NULL while pending
    PRIMARY KEY (user_id, day)
);
CREATE INDEX IF NOT EXISTS score_queue_scored ON score_queue (scored_at) WHERE scored_at IS NOT NULL;

ALTER TABLE users ADD COLUMN IF NOT EXISTS resync_requested_at timestamptz;

-- ingest queues (and reads the recent latency for its answer), scoring marks done
-- and prunes, the api reads and deletes with the account.
GRANT SELECT, INSERT, UPDATE ON score_queue TO ryft_ingest;
GRANT SELECT, UPDATE (scored_at), DELETE ON score_queue TO ryft_scoring;
GRANT SELECT, DELETE ON score_queue TO ryft_api;

DROP POLICY IF EXISTS score_queue_services ON score_queue;
CREATE POLICY score_queue_services ON score_queue TO ryft_ingest, ryft_scoring, ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE score_queue ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON score_queue FROM anon, authenticated, service_role;
END $$;
