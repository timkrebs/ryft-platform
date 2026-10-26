-- The training import (docs/DECISIONS.md D124-D133, docs/work/2026-10-26-train-import/design.md).
-- Additive: new columns with constant defaults or none, nothing renamed or dropped, so the images
-- from before this migration keep working against the new schema (rollback = re-pin the old images;
-- the columns stay).
--
-- Locks: ADD COLUMN with a constant default (or none) rewrites nothing on Postgres 11+, but it takes
-- ACCESS EXCLUSIVE, and the migrator runs the file in one transaction, so that lock is held to the
-- commit. lock_timeout bounds the wait for it (D116, as in 0027). What keeps the hold short is that no
-- statement below scans a big table: every CHECK is added NOT VALID (a catalog change that still
-- checks each new or updated row), and the rows that exist already satisfy it by construction. A
-- VALIDATE here would scan under that same lock, so only the small sessions table is validated.
-- Order: users first, then sessions, plans, session_sets: the order the services take them in (every
-- request's user row first; an import's lease, then its sessions and plans), so a running request
-- never holds a lock this file waits for while waiting for one this file holds.
SET LOCAL lock_timeout = '5s';

-- one import per user at a time (D129, D132): a lease with an expiry, so a pod that dies releases it.
ALTER TABLE users ADD COLUMN IF NOT EXISTS import_lease_until timestamptz;

-- sessions: where a session came from (D125). Existing rows are 'app'. imported_at is when the import
-- last wrote it: a later updated_at (a PUT or a completion in the app) means the app changed it since,
-- and a resend of the import never overwrites it (newer_on_server, D126). NULL for the app's sessions.
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS origin        text NOT NULL DEFAULT 'app',
    ADD COLUMN IF NOT EXISTS import_source text,
    ADD COLUMN IF NOT EXISTS imported_at   timestamptz;

-- plans: the same mark for an imported plan (D126): edited or archived in the app since, never
-- overwritten. NULL for plans made in the app and the built-ins.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS imported_at timestamptz;

-- sets: the support of an assisted bodyweight set (D127). Nullable, no default: no rewrite.
ALTER TABLE session_sets ADD COLUMN IF NOT EXISTS assist_kg double precision;

-- Constraints have no IF NOT EXISTS: add each once. They are named, closed enums or ranges.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.sessions'::regclass AND conname = 'sessions_origin_check') THEN
        ALTER TABLE sessions ADD CONSTRAINT sessions_origin_check
            CHECK (origin IN ('app', 'import')) NOT VALID;
    END IF;
    -- import_source is set exactly for imported sessions, and is a short slug (never user text).
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.sessions'::regclass AND conname = 'sessions_import_source_check') THEN
        ALTER TABLE sessions ADD CONSTRAINT sessions_import_source_check
            CHECK ((origin = 'import') = (import_source IS NOT NULL)
                   AND (import_source IS NULL OR import_source ~ '^[a-z][a-z0-9_]{0,39}$')) NOT VALID;
    END IF;
    -- Left NOT VALID on purpose: session_sets is the largest train table and every existing row has
    -- assist_kg NULL, which passes. VALIDATE CONSTRAINT needs only SHARE UPDATE EXCLUSIVE (writes go
    -- on) but inside this transaction it would run under the ACCESS EXCLUSIVE lock of the ALTER above.
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.session_sets'::regclass AND conname = 'session_sets_assist_kg_check') THEN
        ALTER TABLE session_sets ADD CONSTRAINT session_sets_assist_kg_check
            CHECK (assist_kg IS NULL OR assist_kg BETWEEN 0 AND 200) NOT VALID;
    END IF;
END $$;

-- Every sessions row is 'app' with a NULL import_source: the scan is one pass over the small table.
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_origin_check;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_import_source_check;

-- The api queues the days of an import itself (D128); until now only ingest wrote score_queue.
-- Per column: it inserts a pending row and re-queues one (queued_at = now(), scored_at = NULL, the
-- statement QueueDaysWith runs); it never moves a row to another user or day. It could set scored_at
-- to a time, which only scoring has reason to; nothing in the api does.
GRANT INSERT (user_id, day, queued_at, scored_at), UPDATE (queued_at, scored_at) ON score_queue TO ryft_api;

-- notify's push guard reads the origin of the session an event names (its grant on sessions is per
-- column, 0015). It never writes sessions.
GRANT SELECT (origin) ON sessions TO ryft_notify;

-- No other grant moves: ryft_api's table-level privileges on sessions, plans, session_sets and users
-- cover the new columns (and the lease is a plain UPDATE of users); scoring reads sessions and session_sets
-- table-wide and writes only its load columns. RLS is per table and stays as it is.

-- The import ships dark: enabled, 0 %, so an allow-list turns it on for testers (as 0014's dark flags).
-- Never overwritten: a later change through PUT /v1/admin/flags/{key} stays.
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('train.import', true, 0)
ON CONFLICT (key) DO NOTHING;
