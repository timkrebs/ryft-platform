-- Deleted accounts leave the flags' allow-lists. From this release DeleteUser removes the id from every
-- feature_flags.allow_user_ids in its transaction; this cleans the lists of the deletions before it.
--
-- What goes is an id that has a tombstone in deleted_users (DeleteUser always writes one), deliberately not
-- "an id without a users row": an operator may allow-list a tester's Supabase id before that user's first
-- API call creates the users row (EnsureUser), and that entry must stay.
--
-- Data only, nothing in the schema changes: an image from before this migration reads and writes the lists
-- as before (rollback = re-pin the old images; the ids removed here stay removed, they named deleted
-- accounts). The order of the ids that stay is kept.
--
-- Safe to run again: the WHERE matches only a list that still holds a tombstoned id, so a second run
-- updates no row (updated_at included). Locks: row locks on the matched flags, a table of a few dozen rows;
-- lock_timeout bounds the wait behind a PUT /v1/admin/flags in flight (D116, as 0033).
SET LOCAL lock_timeout = '5s';

UPDATE feature_flags f
SET allow_user_ids = ARRAY(
        SELECT a.id
        FROM unnest(f.allow_user_ids) WITH ORDINALITY AS a(id, n)
        WHERE NOT EXISTS (SELECT 1 FROM deleted_users d WHERE d.id = a.id)
        ORDER BY a.n),
    updated_at = now()
WHERE EXISTS (SELECT 1 FROM deleted_users d WHERE d.id = ANY (f.allow_user_ids));
