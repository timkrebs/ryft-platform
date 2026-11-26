-- Bind the production smoke's one account (D286; migration 0060_smoke_account.sql).
--
-- WHO RUNS IT: the operator, once, in the Supabase SQL editor of the production project as `postgres`
--   (the owner). No service role can write public.smoke_account; this file is the only writer.
-- BEFORE: create the smoke user in the Supabase dashboard (Authentication > Users) with the email
--   smoke@ryftlab.de, a real account that signed in with email, never with Apple or Google. Let it make
--   one request so the app's row exists. Copy its id from that dashboard page and put it in
--   deploy/run/config.env as SMOKE_USER_ID. The id pasted below must be the same. This file looks the
--   account up again by its email, so a UUID copied from the wrong place cannot be bound.
-- HOW: replace the one marked literal (<SMOKE_USER_ID>) with that UUID, paste the WHOLE file, run it once.
--   The editor sends the file as one query, which Postgres runs as one transaction: all or nothing. In
--   psql use `psql --single-transaction -f` (plain `-f` would run each statement on its own).
-- EXPECT: the last statement returns exactly `1 row` (the bound user_id and bound_at). Any refusal
--   raises an error that says why, and nothing is changed.
--
-- The account is bound only when ALL of these hold:
--   1. auth.users has the id;
--   2. exactly one auth.users row has the email smoke@ryftlab.de (v_email below; compared without regard
--      to case), and that row's id is the pasted one. Zero rows, two or more rows (Supabase allows a
--      duplicate email for single sign-on users), or another id each refuse, with a text that says which;
--   3. public.users has the id (the app's row; it is created by the account's first request);
--   4. auth.identities has no provider other than 'email' for it (no apple, google, ...: the smoke account
--      must not be reachable by any sign-in a person could use);
--   5. the id is not in public.deleted_users (a deleted account stays deleted).
--
-- REBINDING (another account takes over, or the first was deleted and re-created): the new account must be
--   the one that holds the email smoke@ryftlab.de (change the old account's email or delete it first),
--   then set v_rebind to true below. The DO block then runs an explicit DELETE of the current binding followed by the INSERT, in
--   the same transaction; if any check fails the old binding stays. With v_rebind false (the default) an
--   existing binding is never replaced: the run is refused and names the bound account.
-- UNBINDING (the smoke is off for good): `delete from public.smoke_account;`. Every smoke request then
--   answers 401 smoke_unbound.
--
-- Deleting the bound account (DELETE /v1/me or an operator delete of public.users) removes the binding too
-- (ON DELETE CASCADE); bind again afterwards.

DO $bind$
DECLARE
    -- >>> REPLACE the text between the quotes with the smoke user's UUID. <<<
    v_id     uuid    := '<SMOKE_USER_ID>';
    -- >>> true only to replace an existing binding (see REBINDING above). <<<
    v_rebind boolean := false;
    -- The smoke account's email, found independently of the pasted UUID (check 2).
    v_email  constant text := 'smoke@ryftlab.de';
    v_other  text;
    v_bound  uuid;
    v_n      integer;
    v_found  uuid;
BEGIN
    IF v_id = '00000000-0000-0000-0000-000000000000' THEN
        RAISE EXCEPTION 'smoke bind refused: the nil UUID is not an account';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM auth.users WHERE id = v_id) THEN
        RAISE EXCEPTION 'smoke bind refused: % is not in auth.users', v_id;
    END IF;
    SELECT count(*), min(id::text)::uuid INTO v_n, v_found
      FROM auth.users WHERE lower(email) = lower(v_email);
    IF v_n = 0 THEN
        RAISE EXCEPTION 'smoke bind refused: no auth.users row has the email % (create the smoke user with that email)', v_email;
    END IF;
    IF v_n > 1 THEN
        RAISE EXCEPTION 'smoke bind refused: % auth.users rows have the email % (it must be exactly one)', v_n, v_email;
    END IF;
    IF v_found <> v_id THEN
        RAISE EXCEPTION 'smoke bind refused: % is not the account with the email % (that is %)', v_id, v_email, v_found;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.users WHERE id = v_id) THEN
        RAISE EXCEPTION 'smoke bind refused: % is not in public.users (the account has not made a request yet)', v_id;
    END IF;
    SELECT string_agg(DISTINCT provider, ', ' ORDER BY provider) INTO v_other
      FROM auth.identities WHERE user_id = v_id AND provider <> 'email';
    IF v_other IS NOT NULL THEN
        RAISE EXCEPTION 'smoke bind refused: % has a sign-in other than email (%)', v_id, v_other;
    END IF;
    IF EXISTS (SELECT 1 FROM public.deleted_users WHERE id = v_id) THEN
        RAISE EXCEPTION 'smoke bind refused: % is in public.deleted_users', v_id;
    END IF;

    SELECT user_id INTO v_bound FROM public.smoke_account;
    IF v_bound IS NOT NULL AND NOT v_rebind THEN
        RAISE EXCEPTION 'smoke bind refused: % is already bound; set v_rebind to true to replace it', v_bound;
    END IF;
    IF v_rebind THEN
        DELETE FROM public.smoke_account;
    END IF;
    INSERT INTO public.smoke_account (user_id) VALUES (v_id);
END
$bind$;

-- What is bound now: one row.
SELECT user_id, bound_at FROM public.smoke_account;
