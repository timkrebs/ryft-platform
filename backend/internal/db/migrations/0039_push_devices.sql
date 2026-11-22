-- A device's push token follows its newest account (docs/DECISIONS.md D251): each account keeps the installs
-- (the app's X-RYFT-Device-ID) it was the last account on, newest last, at most 8. The api bounds the list, not a
-- CHECK: a ninth entry is a bug the claim trims, not a reason to fail the request's write or a merge.
-- The api's claim looks an install up with push_devices @> ARRAY[$1]::uuid[] (the GIN index) and drops the
-- other accounts' tokens for it with DELETE FROM push_tokens WHERE device_id = $1 (the b-tree; the primary key
-- leads with user_id, so it cannot serve that).
--
-- Grants: none new. ryft_api has SELECT and UPDATE on users and DELETE on push_tokens (0009, 0015); ingest and
-- scoring read users at table level (0009), so userColumns may name the column; notify's column grant on users
-- (0015) leaves it out, and notify never reads it.
--
-- Additive: an image from before this migration never reads or writes the column (rollback = re-pin the old
-- images; the column and the indexes stay). A user's list goes with the users row when DeleteUser removes it.
--
-- Locks: ADD COLUMN with a constant default rewrites nothing (Postgres 11+), so ACCESS EXCLUSIVE on users is
-- held only for the catalog change; each CREATE INDEX then holds SHARE on its table (writes wait, reads go on)
-- for one scan of a small table, both until the commit (the migrator runs the file in one transaction, db.go,
-- so no CONCURRENTLY). lock_timeout bounds the wait to get them (D116), written out as in 0027 and 0037.
-- IF NOT EXISTS makes a rerun a no-op.
SET LOCAL lock_timeout = '5s';

ALTER TABLE users ADD COLUMN IF NOT EXISTS push_devices uuid[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS users_push_devices    ON users USING gin (push_devices);
CREATE INDEX IF NOT EXISTS push_tokens_device_id ON push_tokens (device_id);
