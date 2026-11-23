-- Direct connections, phase 1 (docs/DECISIONS.md D257; docs/work/2026-11-23-connections/design.md §5, §13).
-- A provider (Garmin first, a fake in compose and tests) links an account by OAuth on the api and pushes the
-- account's records to a hook on ingest, which keeps them in an inbox until its own consumer applies them.
--
-- New tables:
--   - connections: one row per account and provider, metadata only. The api writes it; ingest reads the
--     provider-user lookup and writes only the status columns and last_data_at (and clears the provider user id
--     and the permissions when the provider revokes the link).
--   - connection_tokens, connection_pending, connection_states, connection_deregistrations: the api alone, as
--     apple_tokens (D48). Every token and PKCE verifier in them is sealed with DATA_ENCRYPTION_KEY, which only the
--     api holds; states and confirm tickets are kept only as their SHA-256.
--   - provider_inbox: ingest's, one row per provider record, its payload dropped once it is processed or dropped.
-- New columns on samples, sleep_segments, user_sources and daily_scores, and the flag connect.garmin (dark).
--
-- Additive: nothing renamed, retyped or dropped. Rollback = re-pin the old images; the tables and columns stay
-- and nothing old reads them. An old DeleteUser knows none of the new tables, so their per-account rows go with
-- the users row (ON DELETE CASCADE) and an account's tokens go without a deregistration call (accepted, design
-- §13). connection_deregistrations has no foreign key on purpose: its rows outlive the account (up to 30 days).
--
-- Locks (the migrator runs the file in one transaction, db.go, so every lock is held to the commit):
--   - samples, sleep_segments, user_sources, daily_scores, in that order (ingest's write order, and DeleteUser's,
--     which deletes the raw tables before users): ADD COLUMN with a constant default or none rewrites nothing
--     (Postgres 11+), and every CHECK on them is NOT VALID, so nothing scans; each takes ACCESS EXCLUSIVE for a
--     catalog change only. Existing rows pass by construction (origin defaults to 'healthkit', the rest is NULL).
--     They are never validated here: VALIDATE would scan under that lock.
--   - then the new tables, whose foreign keys take SHARE ROW EXCLUSIVE on users (writes of users wait, reads
--     don't). Last, so a DeleteUser in flight, holding samples and then wanting users, never waits on this file
--     while this file waits on it.
--   - No index is built on samples or sleep_segments: the direct paths use samples_user_type_start and
--     sleep_user_end (user_id leading) with time bounds (design §5.2). The new tables are empty, so their
--     indexes and CHECKs scan nothing.
--   - lock_timeout bounds every wait (D116).
SET LOCAL lock_timeout = '5s';

-- ---------------------------------------------------------------------------------------------------------------
-- Existing tables.
-- ---------------------------------------------------------------------------------------------------------------

-- Where a raw row came from. A direct row's source is the reserved 'ryft-direct-<provider>' (the sync drops
-- records with that prefix), its vendor the provider, its record_id '<kind>:<inbox record_id>' of the inbox row
-- that wrote it. vendor is also set on HealthKit rows when the bundle id names one (connect.VendorOfBundle), so
-- scoring can prefer a direct night over the same vendor's copy through Apple Health.
ALTER TABLE samples
    ADD COLUMN IF NOT EXISTS origin    text NOT NULL DEFAULT 'healthkit',
    ADD COLUMN IF NOT EXISTS vendor    text,
    ADD COLUMN IF NOT EXISTS record_id text;

ALTER TABLE sleep_segments
    ADD COLUMN IF NOT EXISTS origin       text NOT NULL DEFAULT 'healthkit',
    ADD COLUMN IF NOT EXISTS vendor       text,
    ADD COLUMN IF NOT EXISTS record_id    text,
    -- the provider's validation state of the night, mapped by the adapter; direct rows only
    ADD COLUMN IF NOT EXISTS record_state text;

ALTER TABLE user_sources ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'healthkit';

-- The metric of hrv_ms, hrv_baseline_ms and baseline_hrv_days (NULL with hrv_ms set means sdnn: every row from
-- before PR 3; the reads COALESCE, no backfill), and the data-quality design's coverage columns (D-e), never
-- built until now, so PR 3 needs no migration. scoring writes them through its table-wide grant.
ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS hrv_metric        text,
    ADD COLUMN IF NOT EXISTS coverage          jsonb,
    ADD COLUMN IF NOT EXISTS score_status      text,
    ADD COLUMN IF NOT EXISTS final_at          timestamptz,
    ADD COLUMN IF NOT EXISTS first_recovery_at timestamptz;

-- Constraints have no IF NOT EXISTS: add each once, NOT VALID (checked on every new or updated row, scans none).
-- record_id: 32 characters of kind, the colon and 128 of the inbox's record_id.
DO $$
DECLARE c record;
BEGIN
    FOR c IN SELECT * FROM (VALUES
        ('samples',        'samples_origin_check',              $c$CHECK (origin IN ('healthkit', 'direct'))$c$),
        ('samples',        'samples_vendor_check',              $c$CHECK (vendor ~ '^[a-z][a-z0-9]{1,31}$')$c$),
        ('samples',        'samples_record_id_check',           $c$CHECK (length(record_id) <= 161)$c$),
        ('sleep_segments', 'sleep_segments_origin_check',       $c$CHECK (origin IN ('healthkit', 'direct'))$c$),
        ('sleep_segments', 'sleep_segments_vendor_check',       $c$CHECK (vendor ~ '^[a-z][a-z0-9]{1,31}$')$c$),
        ('sleep_segments', 'sleep_segments_record_id_check',    $c$CHECK (length(record_id) <= 161)$c$),
        ('sleep_segments', 'sleep_segments_record_state_check', $c$CHECK (record_state IN ('provisional', 'final'))$c$),
        ('user_sources',   'user_sources_origin_check',         $c$CHECK (origin IN ('healthkit', 'direct'))$c$),
        ('daily_scores',   'daily_scores_hrv_metric_check',     $c$CHECK (hrv_metric IN ('sdnn', 'rmssd'))$c$),
        ('daily_scores',   'daily_scores_score_status_check',   $c$CHECK (score_status IN ('provisional', 'final'))$c$)
    ) AS t(tbl, name, def) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = ('public.' || c.tbl)::regclass AND conname = c.name) THEN
            EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I %s NOT VALID', c.tbl, c.name, c.def);
        END IF;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------------------------------------------
-- New tables.
-- ---------------------------------------------------------------------------------------------------------------

-- One per account and provider. A grant waiting for its confirm is not a connection (connection_pending).
-- A revoked row keeps no provider user id and no permissions (review S6): provider, status and times only,
-- until the api's purge 90 days after status_at.
CREATE TABLE IF NOT EXISTS connections (
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         text        NOT NULL CONSTRAINT connections_provider_check CHECK (provider ~ '^[a-z][a-z0-9]{1,31}$'),
    provider_user_id text        CONSTRAINT connections_provider_user_id_check CHECK (length(provider_user_id) BETWEEN 1 AND 128),
    status           text        NOT NULL CONSTRAINT connections_status_check
                                     CHECK (status IN ('connected', 'needs_reauth', 'revoked')),
    status_reason    text        CONSTRAINT connections_status_reason_check
                                     CHECK (status_reason IN ('refresh_failed', 'permissions', 'provider_revoked', 'relinked')),
    -- what the user granted, in the provider's words (an open set)
    permissions      text[]      NOT NULL DEFAULT '{}' CONSTRAINT connections_permissions_check CHECK (cardinality(permissions) <= 32),
    -- the privacy policy version consented to at the confirm (as consents.policy_version)
    policy_version   text        NOT NULL CONSTRAINT connections_policy_version_check CHECK (length(policy_version) BETWEEN 1 AND 32),
    connected_at     timestamptz NOT NULL DEFAULT now(),   -- set again on each confirm
    status_at        timestamptz NOT NULL DEFAULT now(),
    last_data_at     timestamptz,                          -- ingest's consumer, on each stored record
    PRIMARY KEY (user_id, provider),
    -- S6: revoked <=> no provider user id; a revoked row holds no permissions either.
    CONSTRAINT connections_revoked_cleared CHECK ((status = 'revoked') = (provider_user_id IS NULL)
                                                  AND (status <> 'revoked' OR cardinality(permissions) = 0))
);
-- One live link per provider account (B1: pending grants and revoked rows are outside it). Also the hook's
-- lookup: a query must repeat the predicate, status IN ('connected', 'needs_reauth'), to use it.
CREATE UNIQUE INDEX IF NOT EXISTS connections_provider_user ON connections (provider, provider_user_id)
    WHERE status IN ('connected', 'needs_reauth');
-- The cap and the gauges (counts per provider and status).
CREATE INDEX IF NOT EXISTS connections_provider_status ON connections (provider, status);

-- A grant between the callback and its confirm, apart from the live connection, which stays untouched until the
-- swap (B1). One per account and provider: a newer callback replaces it. The confirm moves the tokens (re-sealed
-- under the live ADs) and keeps the row, ticket hash included, until confirm_expires_at, so a repeat confirm
-- answers 200 (S3). Sealing ADs: connect:pending-access:<provider>:<user_id>, connect:pending-refresh:….
CREATE TABLE IF NOT EXISTS connection_pending (
    user_id            uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider           text        NOT NULL CONSTRAINT connection_pending_provider_check CHECK (provider ~ '^[a-z][a-z0-9]{1,31}$'),
    provider_user_id   text        NOT NULL CONSTRAINT connection_pending_provider_user_id_check
                                       CHECK (length(provider_user_id) BETWEEN 1 AND 128),
    permissions        text[]      NOT NULL DEFAULT '{}' CONSTRAINT connection_pending_permissions_check CHECK (cardinality(permissions) <= 32),
    access_enc         bytea,
    refresh_enc        bytea,
    access_expires_at  timestamptz,
    refresh_due_at     timestamptz,
    -- SHA-256 of the ticket; never the ticket
    confirm_hash       bytea       NOT NULL CONSTRAINT connection_pending_confirm_hash_check CHECK (octet_length(confirm_hash) = 32),
    confirm_expires_at timestamptz NOT NULL,               -- callback + 10 min
    confirmed_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, provider),
    -- A confirmed grant's tokens live in connection_tokens; its pending row holds none. A newer callback that
    -- replaces a confirmed row sets confirmed_at back to NULL in the same statement.
    CONSTRAINT connection_pending_confirmed_no_tokens CHECK (confirmed_at IS NULL OR (access_enc IS NULL AND refresh_enc IS NULL))
);
-- The purge (and the cap's count of unconfirmed, unexpired grants: a range of the next 10 minutes).
CREATE INDEX IF NOT EXISTS connection_pending_expires ON connection_pending (confirm_expires_at);

-- A live connection's tokens (D48's own-table pattern). Sealing ADs: connect:access:<provider>:<user_id>,
-- connect:refresh:<provider>:<user_id>. last_error is a code, never the provider's text.
CREATE TABLE IF NOT EXISTS connection_tokens (
    user_id           uuid        NOT NULL,
    provider          text        NOT NULL,
    access_enc        bytea       NOT NULL,
    refresh_enc       bytea,
    access_expires_at timestamptz,
    refresh_due_at    timestamptz,                         -- NULL: never refreshed
    refresh_attempts  integer     NOT NULL DEFAULT 0 CONSTRAINT connection_tokens_refresh_attempts_check CHECK (refresh_attempts >= 0),
    last_error        text        CONSTRAINT connection_tokens_last_error_check CHECK (last_error ~ '^[a-z_]{1,64}$'),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, provider),
    FOREIGN KEY (user_id, provider) REFERENCES connections (user_id, provider) ON DELETE CASCADE
);
-- The api's refresh step: the oldest due first.
CREATE INDEX IF NOT EXISTS connection_tokens_due ON connection_tokens (refresh_due_at) WHERE refresh_due_at IS NOT NULL;

-- Open OAuth flows: 10 minutes, single use (DELETE … RETURNING), at most 3 per account and provider. Only the
-- state's SHA-256 is kept; the PKCE verifier is sealed (AD connect:state:<hex state_hash>). Never updated.
CREATE TABLE IF NOT EXISTS connection_states (
    state_hash     bytea       PRIMARY KEY CONSTRAINT connection_states_state_hash_check CHECK (octet_length(state_hash) = 32),
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider       text        NOT NULL CONSTRAINT connection_states_provider_check CHECK (provider ~ '^[a-z][a-z0-9]{1,31}$'),
    verifier_enc   bytea       NOT NULL,
    policy_version text        NOT NULL CONSTRAINT connection_states_policy_version_check CHECK (length(policy_version) BETWEEN 1 AND 32),
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL                    -- created_at + 10 min
);
CREATE INDEX IF NOT EXISTS connection_states_user ON connection_states (user_id, provider, created_at);  -- the per-account cap of 3
CREATE INDEX IF NOT EXISTS connection_states_provider_expires ON connection_states (provider, expires_at); -- the cap's reservation
CREATE INDEX IF NOT EXISTS connection_states_expires ON connection_states (expires_at);                  -- the purge

-- The retry queue for deregistrations (disconnect, account deletion, expired, capped, under-permissioned or
-- replaced grants): the counterpart of deleted_users.apple_token_enc. No foreign key: a row outlives its account,
-- and user_id stays the sealed tokens' AD. Deleted on success, on "already gone", or 30 days after created_at.
CREATE TABLE IF NOT EXISTS connection_deregistrations (
    id               bigserial   PRIMARY KEY,
    user_id          uuid        NOT NULL,
    provider         text        NOT NULL CONSTRAINT connection_deregistrations_provider_check CHECK (provider ~ '^[a-z][a-z0-9]{1,31}$'),
    provider_user_id text        NOT NULL CONSTRAINT connection_deregistrations_provider_user_id_check
                                     CHECK (length(provider_user_id) BETWEEN 1 AND 128),
    reason           text        NOT NULL CONSTRAINT connection_deregistrations_reason_check
                                     CHECK (reason IN ('disconnect', 'account_deleted', 'confirm_expired', 'cap', 'missing_permission', 'replaced')),
    access_enc       bytea,                                -- moved still sealed, same AD
    refresh_enc      bytea,
    created_at       timestamptz NOT NULL DEFAULT now(),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    attempts         integer     NOT NULL DEFAULT 0 CONSTRAINT connection_deregistrations_attempts_check CHECK (attempts >= 0),
    last_error       text        CONSTRAINT connection_deregistrations_last_error_check CHECK (last_error ~ '^[a-z_]{1,64}$')
);
CREATE INDEX IF NOT EXISTS connection_deregistrations_next ON connection_deregistrations (next_attempt_at);
-- DELETE /v1/me's one bounded attempt over the account's rows, after the users row is gone.
CREATE INDEX IF NOT EXISTS connection_deregistrations_user ON connection_deregistrations (user_id);

-- One row per provider record, resolved to its account at the hook (pushes for nobody are not stored). The
-- payload is the one record as sent, kept while pending and, for a failed row, until its purge (7 days); its
-- SHA-256 stays as the dedupe key (30 days). error is a code, never payload text. The hook's
-- ON CONFLICT (provider, kind, record_id) DO UPDATE is guarded by user_id (S1).
CREATE TABLE IF NOT EXISTS provider_inbox (
    id             bigserial   PRIMARY KEY,
    provider       text        NOT NULL CONSTRAINT provider_inbox_provider_check CHECK (provider ~ '^[a-z][a-z0-9]{1,31}$'),
    kind           text        NOT NULL CONSTRAINT provider_inbox_kind_check CHECK (kind ~ '^[a-z][a-z_]{1,31}$'),
    record_id      text        NOT NULL CONSTRAINT provider_inbox_record_id_check CHECK (length(record_id) BETWEEN 1 AND 128),
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    version        bigint      NOT NULL DEFAULT 0,         -- the provider's ordering token; 0 without one
    revision       integer     NOT NULL DEFAULT 1 CONSTRAINT provider_inbox_revision_check CHECK (revision >= 1),
    payload        jsonb,
    payload_sha256 bytea       NOT NULL CONSTRAINT provider_inbox_payload_sha256_check CHECK (octet_length(payload_sha256) = 32),
    state          text        NOT NULL DEFAULT 'pending' CONSTRAINT provider_inbox_state_check
                                   CHECK (state IN ('pending', 'processed', 'dropped', 'failed')),
    received_at    timestamptz NOT NULL DEFAULT now(),     -- the latest (re)receipt
    processed_at   timestamptz,                            -- when it left pending
    attempts       integer     NOT NULL DEFAULT 0 CONSTRAINT provider_inbox_attempts_check CHECK (attempts >= 0),
    error          text        CONSTRAINT provider_inbox_error_check CHECK (error ~ '^[a-z_]{1,64}$'),
    CONSTRAINT provider_inbox_record UNIQUE (provider, kind, record_id),
    -- a processed or dropped row holds no payload (data minimisation)
    CONSTRAINT provider_inbox_payload_state CHECK (state IN ('pending', 'failed') OR payload IS NULL)
);
CREATE INDEX IF NOT EXISTS provider_inbox_state_received ON provider_inbox (state, received_at); -- sweeps, age gauge, purge
CREATE INDEX IF NOT EXISTS provider_inbox_user ON provider_inbox (user_id);                     -- deletion, disconnect, the 500 cap

-- ---------------------------------------------------------------------------------------------------------------
-- Grants (internal/db/roles_test.go and migrate_connections_test.go pin them).
-- ---------------------------------------------------------------------------------------------------------------

-- api: the OAuth flow, the confirm's swap, refresh, deregistration, disconnect, the purges and the gauges; and
-- an account's deletion. A state is never updated.
GRANT SELECT, INSERT, UPDATE, DELETE ON connections, connection_tokens, connection_pending, connection_deregistrations TO ryft_api;
GRANT SELECT, INSERT, DELETE ON connection_states TO ryft_api;
GRANT USAGE ON SEQUENCE connection_deregistrations_id_seq TO ryft_api;
-- api deletes an account's or a disconnected provider's inbox rows; it never reads a payload.
GRANT SELECT (id, user_id, provider, state), DELETE ON provider_inbox TO ryft_api;

-- ingest: the hook resolves a provider user to an account, the consumer checks the link and marks its status
-- (a permission change, a revocation: provider user id NULL and permissions '{}', which
-- connections_revoked_cleared pins) and when it last stored a record. status_reason is readable so a permission
-- notice restores only a link that a permission change suspended. Never a token, a pending grant or a state:
-- a push for a pending grant is unknown_user.
GRANT SELECT (user_id, provider, provider_user_id, status, status_reason) ON connections TO ryft_ingest;
GRANT UPDATE (status, status_reason, status_at, last_data_at, provider_user_id, permissions) ON connections TO ryft_ingest;
-- A column grant cannot say "only to NULL" (review P2-8): ingest's UPDATE on provider_user_id exists for the
-- revocation alone, so this trigger refuses any other change of it by ingest (42501, as a grant would). Unchanged
-- or NULL passes; api and the owner (the confirm's swap, the relink) are not affected.
CREATE OR REPLACE FUNCTION connections_ingest_puid_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = '' AS $$
BEGIN
    IF current_user = 'ryft_ingest' THEN
        RAISE EXCEPTION 'ryft_ingest may only clear connections.provider_user_id' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION connections_ingest_puid_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS connections_ingest_puid_guard ON connections;
CREATE TRIGGER connections_ingest_puid_guard BEFORE UPDATE ON connections FOR EACH ROW
    WHEN (NEW.provider_user_id IS NOT NULL AND NEW.provider_user_id IS DISTINCT FROM OLD.provider_user_id)
    EXECUTE FUNCTION connections_ingest_puid_guard();
GRANT SELECT, INSERT, UPDATE, DELETE ON provider_inbox TO ryft_ingest;
GRANT USAGE ON SEQUENCE provider_inbox_id_seq TO ryft_ingest;
-- deleteDirectData on a deregistration notice removes the direct user_sources row (S2).
GRANT DELETE ON user_sources TO ryft_ingest;

-- scoring reads the new raw and score columns through its table-wide grants (0009). notify's column lists on
-- samples and daily_scores (0015) stay as they are, so it sees none of the new columns.

DROP POLICY IF EXISTS connections_services ON connections;
CREATE POLICY connections_services ON connections TO ryft_api, ryft_ingest USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS connection_tokens_services ON connection_tokens;
CREATE POLICY connection_tokens_services ON connection_tokens TO ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS connection_pending_services ON connection_pending;
CREATE POLICY connection_pending_services ON connection_pending TO ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS connection_states_services ON connection_states;
CREATE POLICY connection_states_services ON connection_states TO ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS connection_deregistrations_services ON connection_deregistrations;
CREATE POLICY connection_deregistrations_services ON connection_deregistrations TO ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS provider_inbox_services ON provider_inbox;
CREATE POLICY provider_inbox_services ON provider_inbox TO ryft_ingest, ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007, 0026 and 0037: RLS on, nothing for the Data API roles.
DO $$
DECLARE t text;
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    FOREACH t IN ARRAY ARRAY['connections', 'connection_tokens', 'connection_pending', 'connection_states',
                             'connection_deregistrations', 'provider_inbox'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('REVOKE ALL ON %I FROM anon, authenticated, service_role', t);
    END LOOP;
    REVOKE ALL ON SEQUENCE connection_deregistrations_id_seq, provider_inbox_id_seq FROM anon, authenticated, service_role;
    REVOKE ALL ON FUNCTION connections_ingest_puid_guard() FROM anon, authenticated, service_role;
END $$;

-- ---------------------------------------------------------------------------------------------------------------
-- The flag: dark (enabled, 0 %), so the allow-list turns it on for testers (D69). Never overwritten: a later
-- change through PUT /v1/admin/flags/{key} stays. The fake has no flag (CONNECT_FAKE=on, compose only).
-- ---------------------------------------------------------------------------------------------------------------
INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('connect.garmin', true, 0)
ON CONFLICT (key) DO NOTHING;
