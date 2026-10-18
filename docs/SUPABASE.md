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
5. **Session pooler** (Connect → Session pooler, port **5432**): copy the host
   (`aws-<n>-eu-central-1.pooler.supabase.com`; the `aws-<n>` index differs per project)
   into `database.host`, and the database password into `database_owner_password`, in
   `infra/vault/terraform.tfvars`. Terraform builds every connection string from them.
   The owner URL goes only to the migrate namespace; each service gets
   `postgresql://ryft_<service>.<ref>:<generated>@<host>:5432/postgres?sslmode=require`.
   Session mode, not transaction mode (6543): migrations take advisory locks and pgx
   uses prepared statements. The direct host `db.<ref>.supabase.co` is IPv6-only unless
   you buy the IPv4 add-on; EKS pods use IPv4, so use the pooler.
6. **Secret key** for account deletion (Project Settings → API Keys → secret key
   `sb_secret_…`, or the legacy `service_role` JWT) into
   `service_secrets.api.SUPABASE_SERVICE_ROLE_KEY` in the same tfvars. Only the api
   namespace receives it.
7. **Project ref** into `supabase_project_ref` (tfvars). ingest and api read it from
   their Vault namespace; it is also the suffix of every pooler user name.
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

## Account deletion

`DELETE /v1/me` erases the user's rows (data and profile) and then calls
`DELETE /auth/v1/admin/users/<id>` with `SUPABASE_SERVICE_ROLE_KEY`. That hard-deletes the
auth user with its Apple identity and sessions. A new `sb_secret_…` key goes in the
`apikey` header only; the legacy `service_role` JWT is also sent as the bearer. The api
refuses to start with a publishable or anon key there. Without the key, the data is
still erased, but the auth user stays, and `ryft_account_deletions_total{step="auth",
result="skipped"}` counts it. Failed calls are retried every 15 minutes from
`deleted_users.auth_deleted_at IS NULL`.

What stays: the tombstone (the user ID and when it was deleted, so that the user's
still-valid tokens cannot recreate the account), the platform's own nightly dumps
(14 days) and Supabase's backups (daily, or PITR, per the plan's retention). Data in
backups ages out; it is not edited. A restore therefore brings deleted accounts back
unless the tombstones are put back after it (`OPERATIONS.md` → Restore).

**Not covered yet: Apple token revocation.** Apple's account-deletion guidance asks apps
that use Sign in with Apple to revoke the user's tokens through
`https://appleid.apple.com/auth/revoke`. That needs an Apple refresh or access token,
which you get by exchanging the sign-in's authorization code using a client secret signed
with the `.p8` key. Supabase's native sign-in (`signInWithIdToken`) keeps neither, and
the `.p8` key must not ship in the app. A later version can accept the authorization
code on `DELETE /v1/me` and do the exchange and revocation server-side.

## Signing-key rotation

The services re-read the JWKS every **10 minutes** — Supabase's edge caches it for 10
minutes and asks clients not to cache longer — and once more, at most once a minute,
when a token arrives with a key ID they have not seen. So:

- **Rotating** (create a standby key, wait ≥ 20 minutes, rotate): nothing to do; new tokens
  verify after at most one refresh.
- **Revoking** a key: tokens signed with it stop working after the next refresh, within
  10 minutes. To cut that short, restart the api and ingest pods
  (`kubectl -n ryft-api rollout restart deploy/api; kubectl -n ryft-ingest rollout restart deploy/ingest`).
- If the JWKS is unreachable, requests get `503` (not `401`), so the app keeps its
  session and retries. `/readyz` turns unready while no keys are loaded.

## Database security

Migration `0007_supabase_auth.sql`, Supabase part (skipped on plain Postgres):

- RLS enabled on every table; policies `TO authenticated USING (user_id = (select auth.uid()))`
  (`id = …` on `users`). No policies on `deleted_users` and `schema_migrations`.
- `REVOKE ALL` on tables, sequences and functions in `public` from `anon`,
  `authenticated`, `service_role`, and the same for tables created later
  (`ALTER DEFAULT PRIVILEGES FOR ROLE postgres`).
- Only the migrate job connects as `postgres`, which owns the tables and therefore bypasses
  RLS. Never `ALTER TABLE … FORCE ROW LEVEL SECURITY`.
- The services connect as `ryft_ingest`, `ryft_scoring` and `ryft_api` (migration 0009),
  which hold only the table privileges they need (`ARCHITECTURE.md` → Database roles).
  RLS applies to them, so every table also has a policy `<table>_services` that admits
  them. Through the pooler their user names are `<role>.<ref>`, e.g. `ryft_api.<ref>`.
  The migrate job sets their passwords from its environment, sending SCRAM verifiers, not
  plaintext. Check once after the first deploy:
  `SELECT rolname, rolcanlogin, rolbypassrls FROM pg_roles WHERE rolname LIKE 'ryft_%';`
  (→ `true`, `false` for all three).
- Supavisor blocks a user for a while after repeated failed logins (`ECIRCUITBREAKER`
  … `too many authentication failures`). A role password that changes before Postgres
  knows it, or services starting before migrate has set it, trips that. Deploy and
  rotate in the order `OPERATIONS.md` gives. The services back off on their own.

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
connections, plus 2 while the migrate job runs. Check the project's pool size (Database → Connection pooling) if you raise
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
