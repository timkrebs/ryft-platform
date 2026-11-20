-- App Attest, the server (docs/DECISIONS.md D245 as the design, D247 as built; docs/API.md → Conventions →
-- App Attest → "What the server keeps"; docs/work/2026-11-20-app-attest-server/plan.md, decisions 5 and 13).
-- Two tables the api alone reads and writes:
--   - app_attest_keys: per account and app install, the key Apple attested, kept until the account is
--     deleted, the key is replaced (the eleventh evicts the least recently used), or 90 days after its last use
--     (store.PurgeAppAttest on the api's tick);
--   - app_attest_challenges: the open challenges, 5 minutes, at most 5 per account, single use.
-- Nothing else is kept: no receipt, no device model, no address, no token (the token is checked without a
-- database read). Neither table holds a value in clear that would let its reader act as the app: the challenge
-- is stored only as its SHA-256 (what the app hashes as clientDataHash, so the store can match it and nobody
-- reading the table can answer with it), and the key id is Apple's identifier of the key (by Apple's
-- definition SHA-256 of the public key next to it), useless without the device's private key, which never
-- leaves the Secure Enclave.
--
-- Additive: two new tables, nothing renamed, retyped or dropped. An image from before this migration never
-- reads them (rollback = re-pin the old images; the tables stay). Its DeleteUser knows nothing of them, so the
-- rows of an account go with its users row (ON DELETE CASCADE, as beta_invite_uses in 0033); the current
-- DeleteUser and MergeUsers also delete them by name (internal/store).
--
-- Locks: CREATE TABLE with a foreign key to users takes SHARE ROW EXCLUSIVE on users until the commit (the
-- migrator runs the file in one transaction, db.go): inserts and updates of users (EnsureUser's write path,
-- last_seen_at) wait for it, reads don't. Nothing else runs under it: the tables are new and empty, so the
-- CHECKs and indexes scan nothing. lock_timeout bounds the wait to get it (D116), written out as in 0027-0034.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS app_attest_keys (
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The key id as generateKey() returned it, decoded from standard base64: 32 bytes.
    key_id       bytea       NOT NULL CONSTRAINT app_attest_keys_key_id_len CHECK (octet_length(key_id) = 32),
    -- The attested credential's public key, uncompressed P-256 (0x04 || X || Y): what an assertion's
    -- signature is checked against.
    public_key   bytea       NOT NULL CONSTRAINT app_attest_keys_public_key_form
                                 CHECK (octet_length(public_key) = 65 AND substring(public_key FROM 1 FOR 1) = decode('04', 'hex')),
    -- The attestation's environment (the AAGUID). Production only (plan decision 3): a development
    -- attestation is refused before it is stored.
    environment  text        NOT NULL CONSTRAINT app_attest_keys_environment CHECK (environment = 'production'),
    -- The last assertion's sign counter, a uint32 in Apple's authenticator data; 0 after the attestation.
    -- A token needs a counter above it (store.AdvanceAttestCounter, a compare-and-set).
    counter      bigint      NOT NULL DEFAULT 0 CONSTRAINT app_attest_keys_counter_range CHECK (counter BETWEEN 0 AND 4294967295),
    -- When the key was attested (POST /v1/app-attest/keys' 10 a day counts these) and last used for a token
    -- (the eviction order, and the 90-day purge).
    attested_at  timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key_id)
);

CREATE TABLE IF NOT EXISTS app_attest_challenges (
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- SHA-256 of the challenge string's UTF-8 bytes (appattest.ClientDataHash); never the challenge.
    hash       bytea       NOT NULL CONSTRAINT app_attest_challenges_hash_len CHECK (octet_length(hash) = 32),
    -- Consumed only before this time (5 minutes after it was issued).
    expires_at timestamptz NOT NULL,
    -- The order of the cap: a sixth open challenge cancels the oldest.
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, hash)
);

-- Per-account statements (the caps, the eviction, a lookup, the 24-hour count, the deletion and the merge) use
-- the primary keys' user_id prefix: at most 10 and 5 rows per account. The purges are the only statements
-- across accounts: DELETE ... WHERE last_used_at < $1 and DELETE ... WHERE expires_at <= $1.
CREATE INDEX IF NOT EXISTS app_attest_keys_last_used ON app_attest_keys (last_used_at);
CREATE INDEX IF NOT EXISTS app_attest_challenges_expires ON app_attest_challenges (expires_at);

-- Only the api: it issues and consumes challenges, stores, advances, evicts and purges keys, and deletes both
-- with an account or a merged account. ingest checks the token without the database and gets nothing; scoring
-- and notify have no business here. A challenge is never updated, so no UPDATE on it.
GRANT SELECT, INSERT, UPDATE, DELETE ON app_attest_keys TO ryft_api;
GRANT SELECT, INSERT, DELETE ON app_attest_challenges TO ryft_api;

DROP POLICY IF EXISTS app_attest_keys_services ON app_attest_keys;
CREATE POLICY app_attest_keys_services ON app_attest_keys TO ryft_api USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS app_attest_challenges_services ON app_attest_challenges;
CREATE POLICY app_attest_challenges_services ON app_attest_challenges TO ryft_api USING (true) WITH CHECK (true);

-- Supabase only, as in 0007, 0017 and 0033: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE app_attest_keys ENABLE ROW LEVEL SECURITY;
    ALTER TABLE app_attest_challenges ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON app_attest_keys FROM anon, authenticated, service_role;
    REVOKE ALL ON app_attest_challenges FROM anon, authenticated, service_role;
END $$;
