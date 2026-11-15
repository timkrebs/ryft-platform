-- Exact floats through the transaction pooler (docs/DECISIONS.md D228, amends D221). On Cloud Run the
-- services read results as text (pgx's exec mode), and Postgres prints a float8 with 15 significant digits
-- while extra_float_digits is 0: 0.30000000000000004 reads back as 0.3. Supabase's configuration file sets 0
-- (pg_settings: `0 | configuration file`, on 5432 and 6543, 2026-11-15). A role's setting outranks the file
-- and applies when a pooler opens that role's server connection. 1, Postgres's default since 12, prints the
-- shortest text that reads back as the same float8.
--
-- The four service roles only: they are the clients in exec mode. migrate logs in as the owner on the session
-- pooler in pgx's default mode, which reads results in binary, and it reads no float; the owner's defaults are
-- also those of Supabase's dashboard, which is not ours to change.
--
-- The owner may do this on Supabase: it created the roles (0009, 0015), so it holds ADMIN OPTION on them
-- (PG >= 16), the right migrate's ALTER ROLE ... PASSWORD already uses on every deploy.
--
-- Roles are cluster-wide, and the integration tests migrate many databases on one server at once. Two
-- ALTER ROLE ... SET of one role race: the loser fails with unique_violation (both inserted the role's row) or
-- "tuple concurrently updated" (internal_error, XX000), after the winner has committed the same value. A role
-- that has the setting is skipped, a lost race is caught, and the check at the end fails the migration if any
-- role still lacks the setting, naming the last error caught.
--
-- Additive: no table is touched. The setting reaches server connections opened after the commit; a pooler's
-- open ones keep the old value until they close. To undo: ALTER ROLE <role> RESET extra_float_digits.
DO $$
DECLARE
    roles  text[] := ARRAY['ryft_ingest', 'ryft_scoring', 'ryft_api', 'ryft_notify'];
    r      text;
    caught text;
    lacks  text[];
BEGIN
    FOREACH r IN ARRAY roles LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r AND 'extra_float_digits=1' = ANY (rolconfig)) THEN
            BEGIN
                EXECUTE format('ALTER ROLE %I SET extra_float_digits = 1', r);
            EXCEPTION WHEN unique_violation OR internal_error THEN
                caught := SQLERRM; -- another migrator set it at the same moment; checked below
            END;
        END IF;
    END LOOP;
    SELECT array_agg(x) INTO lacks FROM unnest(roles) x
     WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = x AND 'extra_float_digits=1' = ANY (rolconfig));
    IF lacks IS NOT NULL THEN
        RAISE EXCEPTION 'roles % lack extra_float_digits=1 (last error caught: %)', lacks, coalesce(caught, 'none');
    END IF;
END $$;
