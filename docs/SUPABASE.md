# RYFT on Supabase

Supabase provides two things to the platform: **Postgres** (the datastore, through the
Supavisor pooler) and **Auth** (Sign in with Apple; the app sends the user's access
token, the services verify it). The services remain the only data API: the app never
talks to Supabase's REST API, and the database is locked down so that nobody else can
either.

## Project checklist (once, by hand)

1. **Project** in region *Central EU (Frankfurt)* (`eu-central-1`), the same as the EKS
   cluster. Note the **project ref** (Project Settings → General; the `<ref>` in
   `https://<ref>.supabase.co`).
2. **Asymmetric JWT signing keys** (Project Settings → JWT Keys): the project must sign
   access tokens with ES256 or RS256 keys published at
   `https://<ref>.supabase.co/auth/v1/.well-known/jwks.json`. Legacy HS256-only projects
   publish an empty key set; the services then answer every request with `503`.
3. **Apple provider** (Authentication → Sign In / Providers → Apple): Services ID, Team ID,
   Key ID and the `.p8` key from the Apple developer account; add the app's bundle ID.
4. **Turn off the Data API** (Project Settings → Data API), or at least remove `public`
   from the exposed schemas. Migration 0007 also revokes every table privilege from
   `anon`, `authenticated` and `service_role` and enables RLS, but nothing should be
   exposed in the first place.
5. **Pooler connection string** (Connect → Session pooler, port **5432**) into
   `DATABASE_URL` (`deploy/k8s/secrets.example.yaml`, or Vault `secret/ryft`):
   `postgresql://postgres.<ref>:<db-password>@aws-<n>-eu-central-1.pooler.supabase.com:5432/postgres?sslmode=require`.
   Session mode, not transaction mode (6543): migrations take advisory locks and pgx
   uses prepared statements. Copy the host from the dashboard; the `aws-<n>` index
   differs per project. The direct host `db.<ref>.supabase.co` is IPv6-only unless you
   buy the IPv4 add-on; EKS pods use IPv4, so use the pooler.
6. **Secret key** for account deletion (Project Settings → API Keys → secret key
   `sb_secret_…`, or the legacy `service_role` JWT) into `SUPABASE_SERVICE_ROLE_KEY`
   (Secret `ryft-supabase-admin`, read by the api pods only; Vault `secret/ryft-admin`).
7. **Project ref** into `deploy/k8s/base/configmap.yaml` (`SUPABASE_PROJECT_REF`).
8. **Smoke-test user**: Authentication → Users → Add user (email + password, auto-confirm).
   `make smoke-supabase` signs in with it (`SUPABASE_URL`, `SUPABASE_ANON_KEY`,
   `SMOKE_EMAIL`, `SMOKE_PASSWORD`) and writes synthetic days into it — keep it separate
   from real accounts.

## What the services check

`internal/auth` accepts a bearer only if it is an access token for a signed-in user of
this project:

| Check | Value |
| --- | --- |
| Signature | ES256 or RS256, key from the project JWKS (no HS256, no `none`) |
| `iss` | `https://<ref>.supabase.co/auth/v1` (override with `SUPABASE_ISSUER` for a custom domain) |
| `aud` | contains `authenticated` |
| `exp`, `iat` | required, 30 s clock skew allowed |
| `role` | `authenticated` (rejects the anon key and `service_role` tokens) |
| `is_anonymous` | not true |
| `sub` | a UUID — the user ID everything is scoped to |

The user row is created on the first request (`store.EnsureUser`) with `email`,
`app_metadata.provider` and `user_metadata.full_name` (where the app stores the Apple
name), refreshed at most every five minutes. There is no foreign key to `auth.users`, so
the same migrations run on plain Postgres.

## Signing-key rotation

The services re-read the JWKS every **10 minutes** — Supabase's edge caches it for 10
minutes and asks clients not to cache longer — and once more, at most once a minute,
when a token arrives with a key ID they have not seen. So:

- **Rotating** (create a standby key, wait ≥ 20 minutes, rotate): nothing to do; new tokens
  verify after at most one refresh.
- **Revoking** a key: tokens signed with it stop working after the next refresh, within
  10 minutes. To cut that short, restart the api and ingest pods
  (`kubectl -n ryft rollout restart deploy/api deploy/ingest`).
- If the JWKS is unreachable, requests get `503` (not `401`), so the app keeps its
  session and retries. `/readyz` turns unready while no keys are loaded.

## Database security

Migration `0007_supabase_auth.sql`, Supabase part (skipped on plain Postgres):

- RLS enabled on every table; policies `TO authenticated USING (user_id = (select auth.uid()))`
  (`id = …` on `users`). No policies on `deleted_users` and `schema_migrations`.
- `REVOKE ALL` on tables, sequences and functions in `public` from `anon`,
  `authenticated`, `service_role`, and the same for tables created later
  (`ALTER DEFAULT PRIVILEGES FOR ROLE postgres`).
- The services connect as `postgres`, which owns the tables and therefore bypasses RLS.
  Never `ALTER TABLE … FORCE ROW LEVEL SECURITY`. Check the role once:
  `SELECT rolname, rolbypassrls FROM pg_roles WHERE rolname = 'postgres';`

**Restoring a database migrated on plain Postgres into Supabase** (e.g. moving off the
`local-postgres` overlay): migration 0007 is recorded as applied but its Supabase block
never ran there. After the restore, run the `DO $$ … $$` block at the end of
`0007_supabase_auth.sql` once in the SQL editor.

Verify with: `SELECT tablename, rowsecurity FROM pg_tables WHERE schemaname = 'public';`
and `SELECT has_table_privilege('anon', 'public.samples', 'SELECT');` (→ `false`).

## Connections

Supavisor session mode holds one server connection per client connection. Each pod
keeps a small pool (`DB_MAX_CONNS`, default 4 for api/ingest, 2 for scoring), recycled
every 30 minutes: at the HPA maximum (4 api + 4 ingest + 1 scoring) that is 34
connections. Check the project's pool size (Database → Connection pooling) if you raise
replicas or `DB_MAX_CONNS`.

`sslmode=require` encrypts but does not verify the server certificate. For
`verify-full`, download Supabase's CA certificate (Database settings → SSL), mount it
into the pods and add `sslrootcert=/path/to/prod-ca-2021.crt&sslmode=verify-full` to
`DATABASE_URL` (the backup job's `pg_dump` understands the same parameters).

## Local development

Compose and `deploy/k8s/overlays/local-postgres` run plain Postgres with
`AUTH_MODE=apikey` and `ALLOW_INSECURE_APIKEY_AUTH=true`: the bearer is `API_KEY`, or
`API_KEY:<uuid>` to act as another user. The services refuse to start in that mode
without the flag. Never expose it.
