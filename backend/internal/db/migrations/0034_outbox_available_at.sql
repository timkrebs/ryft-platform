-- The outbox's not-before time, and notify as its fourth publisher (docs/work/2026-11-14-gcp/design.md
-- §2.2, the Deferral row; plan-a1.md → Outbox; D101 as amended by D219). notify acks an event that falls
-- in quiet hours and writes it back as an outbox row of its own (publisher 'notify', for ryft-notify-wait)
-- with available_at set to when it may go out; the tick's Sweep relays the rows that are due. Every other
-- row is due when it is written (the default), so ingest, api and scoring relay as before.
--
-- Additive: one column with a default, a CHECK replaced by a wider one under the same name, one index, and
-- grants and a policy for a role that had none on the table; nothing renamed, retyped or dropped. An image
-- from before this migration keeps working (rollback = re-pin the old images; all of this stays): its INSERT
-- names no available_at and gets now(), its drain and gauges never read the column, and the CHECK refuses
-- nothing it wrote before. No old image relays publisher 'notify', so after a rollback notify's waiting rows
-- stay unsent; account deletion still removes them (the api deletes by user_id, whatever the publisher).
--
-- Locks: ADD COLUMN takes ACCESS EXCLUSIVE on outbox until the commit (the migrator runs the file in one
-- transaction, db.go), and every sync's apply transaction (it inserts its outbox row) and every relay waits
-- for it. lock_timeout bounds the wait to get it (D116); the migrator sets 5 s, written out here as in
-- 0027-0033 so the file says so when run by hand. What runs under the lock once it is held:
--   - ADD COLUMN ... NOT NULL DEFAULT now() rewrites nothing and scans nothing (PG >= 11): now() is stable, so
--     it is evaluated once and stored as the column's missing value. Every existing row reads the migration's
--     transaction time: due at once.
--   - The CHECK is dropped and added NOT VALID (catalog only), then validated: one sequential scan of outbox.
--     Every existing row passed the narrow CHECK, so it passes this one; the scan only proves it.
--   - CREATE INDEX: a second scan of the same rows; only unpublished ones (near none) are indexed.
--   The table holds the unpublished backlog plus a week of published rows (outbox.Keep): tens of thousands of
--   rows at beta size, so both scans take milliseconds. Above ~1M rows (SELECT count(*),
--   pg_size_pretty(pg_total_relation_size('outbox')) FROM outbox; before the PreSync), move the VALIDATE (it then
--   needs only SHARE UPDATE EXCLUSIVE) and the index (CONCURRENTLY, which needs a no-transaction mode the
--   migrator does not have yet, see 0030) to migrations of their own.
-- One table only, so no lock order to keep with the services' transactions.
SET LOCAL lock_timeout = '5s';

-- When the row may be relayed. The drain and the gauges take only due rows (available_at <= now()).
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS available_at timestamptz NOT NULL DEFAULT now();

-- 0026 declared the publisher CHECK inline, so Postgres named it outbox_publisher_check (<table>_<column>_check;
-- migrate_outbox_test pins that name on a database at 0033). A CHECK cannot be altered: drop it while it is the
-- narrow one, add the wide one under the same name. A rerun finds the wide one and changes nothing.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conrelid = 'public.outbox'::regclass AND conname = 'outbox_publisher_check'
                 AND pg_get_constraintdef(oid) NOT LIKE '%''notify''%') THEN
        ALTER TABLE outbox DROP CONSTRAINT outbox_publisher_check;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'public.outbox'::regclass AND conname = 'outbox_publisher_check') THEN
        ALTER TABLE outbox ADD CONSTRAINT outbox_publisher_check
            CHECK (publisher IN ('ingest', 'api', 'scoring', 'notify')) NOT VALID;  -- a closed enum
    END IF;
END $$;
ALTER TABLE outbox VALIDATE CONSTRAINT outbox_publisher_check;

-- The drain: WHERE publisher = $1 AND published_at IS NULL AND attempts < 50 AND available_at <= now()
-- ORDER BY id LIMIT n. outbox_pending (publisher, id) WHERE published_at IS NULL (0026) stays: it is the
-- id-ordered walk that stops after n rows, the right path for ingest, api and scoring, whose rows are due when
-- written, and the one the images from before this migration use. outbox_due adds a range on the not-before
-- time for the rows that are not due yet, which only notify has: its Sweep and the due-only gauges (count, and
-- the oldest due row's age) can reach the due rows without walking past the waits. Partial like outbox_pending,
-- so it holds only the unpublished backlog; its upkeep is one index entry per insert (the relay's mark sets
-- published_at, which both predicates name, so it was never a HOT update and costs nothing more).
CREATE INDEX IF NOT EXISTS outbox_due ON outbox (publisher, available_at) WHERE published_at IS NULL;

-- notify writes its waits and relays them as the other publishers do theirs (0026): it inserts, and its relay
-- reads, marks and cleans its own rows. SELECT ... FOR UPDATE in the drain needs UPDATE on some column, which
-- the column grant gives. No relay may change a row's publisher, payload or not-before time.
GRANT SELECT, INSERT, DELETE ON outbox TO ryft_notify;
GRANT UPDATE (published_at, attempts, last_error) ON outbox TO ryft_notify;
GRANT USAGE ON SEQUENCE outbox_id_seq TO ryft_notify;

-- Where RLS is on (Supabase, since 0026's DO block), notify sees and writes its own rows only: it can neither
-- read another publisher's events nor write a row as one. outbox_services (0026) stays as it is for ingest, api
-- and scoring, so the api's account deletion (DELETE FROM outbox WHERE user_id = $1, any publisher) also removes
-- notify's rows. Created on plain Postgres too, where it is inert while RLS is off (as outbox_services is).
-- Not named <table>_services: those admit every service that uses a table to all of its rows.
DROP POLICY IF EXISTS outbox_notify ON outbox;
CREATE POLICY outbox_notify ON outbox TO ryft_notify USING (publisher = 'notify') WITH CHECK (publisher = 'notify');

-- Nothing for Supabase only: RLS on outbox and the REVOKE from anon, authenticated and service_role are 0026's,
-- and a new column gets no grant of its own.
