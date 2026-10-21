-- The transactional outbox (docs/DECISIONS.md D101). An event is written in the same
-- transaction as the write it announces (a sync's queued days, a completed session),
-- or right after it, and the publishing service's relay sends it with the broker's
-- confirm and marks it published. A crash between the commit and the send leaves the
-- row for the next pass, so "stored but never announced" cannot happen; a crash after
-- the send but before the mark sends it twice, which the idempotent handlers absorb
-- (D100). Published rows are kept a week, for looking back, then deleted by the relay.
CREATE TABLE IF NOT EXISTS outbox (
    id           bigserial PRIMARY KEY,
    publisher    text NOT NULL CHECK (publisher IN ('ingest', 'api', 'scoring')),
    user_id      uuid,                        -- from the event, for account deletion
    routing_key  text NOT NULL,               -- the event key (events.Key*)
    message_id   text NOT NULL DEFAULT '',    -- stable for the same trigger
    reason       text NOT NULL DEFAULT '',    -- the ryft_events_published_total label
    payload      jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,                 -- NULL until the broker confirmed it
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (publisher, id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_published ON outbox (publisher, published_at) WHERE published_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS outbox_user ON outbox (user_id);

-- Each publisher writes its rows and its relay drains them (it reads only its own, by
-- the publisher column); the api also deletes them with an account.
GRANT SELECT, INSERT, DELETE ON outbox TO ryft_ingest, ryft_api, ryft_scoring;
GRANT UPDATE (published_at, attempts, last_error) ON outbox TO ryft_ingest, ryft_api, ryft_scoring;
GRANT USAGE ON SEQUENCE outbox_id_seq TO ryft_ingest, ryft_api, ryft_scoring;

DROP POLICY IF EXISTS outbox_services ON outbox;
CREATE POLICY outbox_services ON outbox TO ryft_ingest, ryft_api, ryft_scoring USING (true) WITH CHECK (true);

-- Supabase only, as in 0007: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON outbox FROM anon, authenticated, service_role;
    REVOKE ALL ON SEQUENCE outbox_id_seq FROM anon, authenticated, service_role;
END $$;
