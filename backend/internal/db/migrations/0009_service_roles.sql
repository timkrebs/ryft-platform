-- One least-privilege role per service (docs/DECISIONS.md D42). The tables stay
-- owned by the migration owner (`postgres` on Supabase, `ryft` in compose); only
-- the migrate command connects as the owner. The roles are created NOLOGIN here;
-- `migrate` turns on LOGIN and sets each password from its environment, so no
-- password is ever part of a migration.
--
-- Roles are cluster-wide: creating one that exists (another database on the same
-- server, or a concurrent migrator) is not an error.
DO $$
DECLARE r text;
BEGIN
    FOREACH r IN ARRAY ARRAY['ryft_ingest', 'ryft_scoring', 'ryft_api'] LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            BEGIN
                EXECUTE format('CREATE ROLE %I NOLOGIN', r);
            EXCEPTION WHEN duplicate_object OR unique_violation THEN
                NULL;
            END;
        END IF;
    END LOOP;
END $$;

GRANT USAGE ON SCHEMA public TO ryft_ingest, ryft_scoring, ryft_api;
-- Every service waits at start-up until the schema is current.
GRANT SELECT ON schema_migrations TO ryft_ingest, ryft_scoring, ryft_api;

-- ingest: POST /v1/sync. Creates and refreshes the user row, records the
-- device, upserts raw data. Reads tombstones so a deleted account stays deleted.
GRANT SELECT, INSERT, UPDATE ON users, devices, samples, sleep_segments, workouts TO ryft_ingest;
GRANT SELECT ON deleted_users TO ryft_ingest;
GRANT USAGE ON SEQUENCE samples_id_seq, sleep_segments_id_seq TO ryft_ingest;

-- scoring: reads raw data, writes scores. On workouts it may only set the load
-- it computes, never the data the phone sent.
GRANT SELECT ON users, devices, samples, sleep_segments, workouts TO ryft_scoring;
GRANT UPDATE (strain, trimp, zone_minutes) ON workouts TO ryft_scoring;
GRANT SELECT, INSERT, UPDATE ON daily_scores TO ryft_scoring;

-- api: reads everything for the app, writes settings and the recompute gate,
-- and erases an account (DELETE /v1/me) including its tombstone.
GRANT SELECT, INSERT, UPDATE, DELETE ON users TO ryft_api;
GRANT SELECT, DELETE ON devices, samples, sleep_segments, workouts, daily_scores TO ryft_api;
GRANT SELECT, INSERT, UPDATE ON deleted_users TO ryft_api;

-- Row-level security is on for every table on Supabase (0007) and applies to
-- these roles, which do not own the tables. The services scope every query
-- to the token's user themselves, so the policy admits all rows; the grants
-- above decide what each role may do. On plain Postgres RLS is off and the
-- policies are inert.
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['users', 'devices', 'samples', 'sleep_segments', 'workouts',
                             'daily_scores', 'deleted_users', 'schema_migrations'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_services', t);
        EXECUTE format('CREATE POLICY %I ON %I TO ryft_ingest, ryft_scoring, ryft_api USING (true) WITH CHECK (true)',
                       t || '_services', t);
    END LOOP;
END $$;
