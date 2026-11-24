# RYFT on Supabase

Supabase provides two things to the platform: **Postgres** (the datastore, through the
Supavisor pooler) and **Auth** (email + password and Sign in with Apple; the app sends
the user's access token, the services verify it). The services remain the only data API: the app never
talks to Supabase's REST API, and the database is locked down so that nobody else can
either.

## Project checklist (once, by hand)

1. **Project** in region *Central EU (Frankfurt)* (`eu-central-1`, D49; the services run on
   Cloud Run in europe-west1, D217). Note the **project ref** (Project Settings → General; the `<ref>` in
   `https://<ref>.supabase.co`).
2. **Asymmetric JWT signing keys** (Project Settings → JWT Keys): the project must sign
   access tokens with ES256 or RS256 keys published at
   `https://<ref>.supabase.co/auth/v1/.well-known/jwks.json`. Legacy HS256-only projects
   publish an empty key set; the services then answer every request with `503`.
3. **Apple provider** (Authentication → Sign In / Providers → Apple): enable it and put
   the app's bundle ID `dev.timkrebs.ryft` into *Client IDs*; native sign-in
   (`signInWithIdToken`) needs nothing else. A Services ID and the generated secret
   (a JWT from the `.p8`, valid at most six months) are only needed if a web sign-in
   is added later.
   - **Email accounts** (Authentication → Sign In / Providers → Email): enable *Email*,
     turn on *Confirm email* and *Secure email change* (**security-critical** for invites bound to an email:
     with *Confirm email* off Supabase marks a sign-up's address confirmed at once, so anyone with a leaked invite link
     could register the tester's address and redeem the code; the api also reads `mailer_autoconfirm` from the public
     auth settings and refuses bound invites while it is on, D159; record the state in STATUS.md before the first
     bound code). Under *Password*, set the
     minimum length to 10 and require letters and digits. Turn on *Leaked password
     protection* where the plan offers it.
   - **Redirects** (Authentication → URL Configuration): *Site URL*
     `https://ryftlab.de`. Add `ryft://auth/callback` to *Redirect URLs*: the app opens
     confirmation and password-reset links through it.
   - **Emails** (Authentication → Emails): set the sender name to *RYFT* and word every
     template the same way (*Confirm signup*, *Reset password*, *Change email address*,
     *Magic link*). The built-in mailer sends only a few emails an hour, so enter
     an SMTP provider under *SMTP Settings* before inviting people.
   - **Rate limits** (Authentication → Rate Limits): sign-ups and sign-ins per hour and
     IP, and emails per hour. Keep the defaults and lower *sign-ups* for an invite-only
     alpha. Turn on CAPTCHA (Authentication → Attack Protection) once sign-up is open
     to everyone.
   - **Sign in with Apple token revocation** (Apple Developer → Certificates,
     Identifiers & Profiles → Keys → +): create a key with *Sign in with Apple* enabled
     for the primary App ID `dev.timkrebs.ryft`, and download `AuthKey_<key id>.p8`
     (only possible once). The key ID goes into `APPLE_KEY_ID` in `deploy/run/config.env`
     (team ID `M77SKHKG63` and client ID `dev.timkrebs.ryft` are fixed in
     `deploy/run/api.yaml`), and the `.p8` contents into the Secret Manager secret
     `apple-key-p8` (`OPERATIONS.md` → GCP → 5). The same key may
     also sign the Supabase provider's web secret.

   - **Manual identity linking** (Authentication → Settings → *Allow manual linking*):
     on, so a signed-in user can add Sign in with Apple to an email account (or the
     reverse) from the app instead of getting a second account. Supabase links
     identities of the same verified email by itself, but not Apple's "Hide My Email"
     relay address. Two accounts that already exist are merged by the operator
     (`OPERATIONS.md` → Two accounts for one person).

   - **Multi-factor** (Authentication → Multi-Factor; for the operator's page `/admin`,
     **security-critical**): *TOTP* enabled, and *Maximum enrolled factors* (project-wide; the
     Management API's name is believed to be `mfa_max_enrolled_factors`, not verified in the dashboard)
     set to **1**; the app uses no MFA. With one factor allowed, someone who has the admin password but
     not the phone cannot add a factor of their own. Then the check V-MFA-1: an aal1 session of an
     account with a verified factor must be refused a second enrolment. GoTrue checks the factor count
     first, so with the limit at 1 the pass is `422 too_many_enrolled_mfa_factors`, and only with a limit
     of 2 or more `403 insufficient_aal`; `200` means stop, anything else is inconclusive
     (`OPERATIONS.md` → Beta → Admin sign-in has the steps). Under Hooks, no custom access token hook
     may touch `app_metadata.role` (What the services check, below).

   Dashboard labels move between Supabase releases; the settings above are what
   matters.

   **Security-critical for the admin page and for invites bound to an email** (D159, D162): *Confirm
   email* ON and *Secure email change* ON (Email accounts above), *Maximum enrolled factors* 1 and the
   V-MFA-1 check (Multi-factor above), and the Data API off (step 4). Record each state and the date in
   STATUS.md before the role is granted or the first bound code is made. The api checks none of them for
   sign-in; its only reaction is that it refuses to create or redeem a bound invite while the project's
   `mailer_autoconfirm` is true or unknown (D159). Every one of them is yours to check.
4. **Turn off the Data API** (Project Settings → Data API), or at least remove `public`
   from the exposed schemas (**security-critical** for the admin page, D162: the page's
   publishable key is public and its `connect-src` reaches `/auth/v1/` only, but any client can use the key).
   Migration 0007 also revokes every table privilege from
   `anon`, `authenticated` and `service_role` and enables RLS, but nothing should be
   exposed in the first place.
5. **Pooler** (Connect → Transaction pooler, port **6543**, for the services; Session pooler,
   port **5432**, for migrate; D221): both are on one host,
   `aws-<n>-eu-central-1.pooler.supabase.com` (the `aws-<n>` index differs per project). The host
   goes into `SUPABASE_POOLER_HOST` in `deploy/run/config.env`, by a PR (`OPERATIONS.md` → GCP → 7,
   7a), and the database password into the Secret Manager secret `db-password-owner`
   (`OPERATIONS.md` → GCP → 5). `scripts/cloudrun_render.py` builds every connection string from
   the ref and the host, with no password in the URL (Connections, below). The owner's goes only
   to the migrate job; each service connects as `ryft_<service>.<ref>` on 6543. The direct host
   `db.<ref>.supabase.co` is IPv6-only unless you buy the IPv4 add-on, so use the pooler.
6. **Secret key** for account deletion (Project Settings → API Keys → secret key
   `sb_secret_…`, or the legacy `service_role` JWT) into the Secret Manager secret
   `supabase-secret-key` (`OPERATIONS.md` → GCP → 5). Only the api's service account can
   read it (`infra/gcp/secrets.tf`).
7. **Project ref** into `SUPABASE_PROJECT_REF` in `deploy/run/config.env` (the PR of step 5).
   ingest and api get it as plain env from their templates; it is also the suffix of every pooler user name.
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

**Operator claims.** The same verifier also reads, for `/v1/admin/*` only (OPERATIONS → Beta →
Admin sign-in): `app_metadata.role` (the operator role; `app_metadata` is writable only with the
service key, so it is the one place the role is read from, never the top-level `role`, which
stays `authenticated`, and never `user_metadata`, which the user can write), `aal` (`aal2`
after a second factor) and the newest `amr` entry with method `totp` and its `timestamp`. They
are read leniently: a claim of an unexpected shape reads as empty and never makes a token
invalid, or one odd shape would sign every app user out. They grant nothing alone: the account
also needs a row in `admin_accounts` (written only by the owner, in SQL) and the TOTP must be at
most 12 hours old. Supabase must not have a custom access token hook that touches
`app_metadata.role`.

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
still-valid tokens cannot recreate the account) and Supabase's backups (daily, or PITR, per
the plan's retention). Data in
backups ages out; it is not edited. A restore therefore brings deleted accounts back
unless the tombstones are put back after it (`OPERATIONS.md` → Backups).

**Apple token revocation.** Apple asks apps that use Sign in with Apple to revoke the
user's tokens when the account is deleted. Supabase's native sign-in keeps no Apple
token, and the `.p8` key must not ship in the app, so the api does it:

1. After sign-in the app sends the one-time authorization code to
   `POST /v1/me/apple-authorization`.
2. The api exchanges the code at `https://appleid.apple.com/auth/token`. The
   `client_id` is the bundle ID, and the client secret is an ES256 JWT signed with the
   `.p8`.
3. It stores the refresh token encrypted in `apple_tokens` (AES-256-GCM with
   `DATA_ENCRYPTION_KEY` from the Secret Manager secret `data-key`, which only the api
   reads; the user ID is bound into the ciphertext).
4. `DELETE /v1/me` moves the encrypted token to the tombstone, revokes it at
   `https://appleid.apple.com/auth/revoke`, and only then deletes the Supabase user.
   Failed revocations are retried by the reconciler every 15 minutes, as failed
   Supabase deletions are (`ryft_account_deletions_total{step="apple"}`).

Without the `APPLE_*` values the endpoint answers `501` and deletion skips this step;
on Cloud Run `scripts/check_cloudrun.py` requires them in `deploy/run/api.yaml`.

## Direct connections (migration 0042, D257)

A provider (Garmin first, phase 2; a fake in compose) links an account by OAuth on the api and pushes
records to ingest (`docs/work/2026-11-23-connections/design.md` §5). Six tables. Each has RLS on and
`REVOKE ALL` from `anon`, `authenticated` and `service_role` (sequences too), and each has one
`<table>_services` policy:

| Table | Holds | Roles (policy) |
| --- | --- | --- |
| `connections` | one row per account and provider: status, the provider's user id, permissions, times; no secret | `ryft_api` all; `ryft_ingest` reads `user_id`, `provider`, `provider_user_id`, `status`, `status_reason` and updates only the status columns, `last_data_at` and (to clear them on a revocation) `provider_user_id`, `permissions`; the trigger `connections_ingest_puid_guard` refuses ingest any `provider_user_id` other than NULL or unchanged (42501) |
| `connection_tokens` | the live link's tokens, sealed with `DATA_ENCRYPTION_KEY` | `ryft_api` |
| `connection_pending` | a grant between the callback and its confirm (sealed tokens, the ticket's SHA-256) | `ryft_api` |
| `connection_states` | open OAuth flows, 10 minutes: the state's SHA-256 and the sealed PKCE verifier | `ryft_api` (never updated) |
| `connection_deregistrations` | the retry queue for deregistrations at the provider, sealed tokens; **no FK to `users`**, it outlives the account for up to 30 days | `ryft_api` |
| `provider_inbox` | one row per pushed record; the payload only while `pending` or `failed` | `ryft_ingest` all; `ryft_api` reads `id`, `user_id`, `provider`, `state` and deletes |

What the database itself enforces:
- one live link per provider account: a partial unique index on `(provider, provider_user_id)` over
  `connected` and `needs_reauth`;
- a `revoked` row keeps no provider user id and no permissions;
- a confirmed pending grant holds no token;
- a processed or dropped inbox row holds no payload;
- every error column holds a code (`^[a-z_]{1,64}$`), never a provider's or a payload's text.

Existing tables gain:
- `origin` (`healthkit` by default, or `direct`) and `vendor` on `samples` and `sleep_segments`, plus
  `record_id`, and `record_state` on the sleep;
- `origin` on `user_sources`;
- `hrv_metric`, `coverage`, `score_status`, `final_at` and `first_recovery_at` on `daily_scores`.

Their CHECKs are `NOT VALID`: new rows are checked, old rows pass by construction, and nothing was
scanned. Leave them that way. A `VALIDATE CONSTRAINT` on `samples` would scan the table, and it is
not needed. `ryft_ingest` also gains `DELETE` on `user_sources`, for a deregistration notice's
direct source. `ryft_notify`'s column lists are unchanged, so it sees none of the new columns.

Account deletion: `store.DeleteUser` moves every unrevoked connection's tokens, and every
unconfirmed pending grant's, to `connection_deregistrations`. Then it deletes the five per-account
tables. Their foreign keys also cascade from `users`, so an image from before 0042 leaves nothing
behind. The tokens then go without a deregistration call. Export: `GET /v1/me/connections`
carries the connection metadata. Tokens, states, pending grants, the inbox and the queue are
credentials or transient, and are not exported.

Check after the deploy:

```sql
SELECT tablename, rowsecurity FROM pg_tables WHERE schemaname = 'public'
  AND (tablename LIKE 'connection%' OR tablename = 'provider_inbox');            -- all true
SELECT has_table_privilege('anon', 'public.connection_tokens', 'SELECT');        -- false
SELECT key, enabled, rollout_pct FROM feature_flags WHERE key = 'connect.garmin'; -- true, 0
```

## Signing-key rotation

The services re-read the JWKS every **10 minutes** — Supabase's edge caches it for 10
minutes and asks clients not to cache longer — and once more, at most once a minute,
when a token arrives with a key ID they have not seen. So:

- **Rotating** (create a standby key, wait ≥ 20 minutes, rotate): nothing to do; new tokens
  verify after at most one refresh.
- **Revoking** a key: tokens signed with it stop working after the next refresh, within
  10 minutes. On Cloud Run there is no restart outside a deploy (CI owns the revisions,
  D223), so the 10 minutes are the bound; a new instance fetches the keys as it starts.
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
- The services connect as `ryft_ingest`, `ryft_scoring`, `ryft_api` (migration 0009) and
  `ryft_notify` (0015), which hold only the table privileges they need (`ARCHITECTURE.md` →
  Database roles). RLS applies to them, so every table also has a policy `<table>_services`
  that admits them. One table also limits a role by row: on `outbox`, `outbox_services`
  admits ingest, api and scoring to every row (the api deletes any publisher's rows with an
  account), and `outbox_notify` (0034) admits `ryft_notify` only to rows with
  `publisher = 'notify'`, its quiet-hours waits.
  Through the pooler their user names are `<role>.<ref>`, e.g. `ryft_api.<ref>`.
  The migrate job sets their passwords from its environment, sending SCRAM verifiers, not
  plaintext. Check once after the first deploy:
  `SELECT rolname, rolcanlogin, rolbypassrls FROM pg_roles WHERE rolname LIKE 'ryft_%';`
  (→ `true`, `false` for all four).
- Supavisor blocks a user for a while after repeated failed logins (`ECIRCUITBREAKER`
  … `too many authentication failures`). A role password that changes before Postgres
  knows it, or services starting before migrate has set it, trips that. Deploy and
  rotate in the order `OPERATIONS.md` gives. The services back off on their own.

**Restoring a database migrated on plain Postgres into Supabase** (e.g. a dump of the
compose stack's Postgres): migration 0007 is recorded as applied but its Supabase block
never ran there. After the restore, run the `DO $$ … $$` block at the end of
`0007_supabase_auth.sql` once in the SQL editor. A `pg_dump` carries no role and no role
setting, so if the service roles were created anew for the restore, also run
`0035_float_digits.sql` there once, and check `rolconfig` as in **Connections**.

Verify with: `SELECT tablename, rowsecurity FROM pg_tables WHERE schemaname = 'public';`
and `SELECT has_table_privilege('anon', 'public.samples', 'SELECT');` (→ `false`).

## Connections

**On Cloud Run (D221)** the services use Supavisor's **transaction pooler (port 6543)** and the
`migrate` job the **session pooler (5432)**, both on the shared pooler host (IPv4; the direct host
is IPv6-only). `deploy/run/config.env` holds the ref and the host, and `scripts/cloudrun_render.py`
builds the URLs (`ticket-b.md` → Plain env):

```text
services: postgresql://ryft_<svc>.<ref>@<pooler host>:6543/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec
migrate:  postgresql://postgres.<ref>@<pooler host>:5432/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt
```

- **No password in the URL.** It comes as `PGPASSWORD` from the role's secret (`db-password-<svc>`,
  `db-password-owner` for migrate), and pgx reads it. `migrate` sets every role's password from
  `DB_PASSWORD_<SVC>` on each run; rotate with `scripts/rotate_db_password.sh` (OPERATIONS → GCP).
- **`verify-full`:** the Supabase Root 2021 CA (public, valid until 2031-04-26) is baked into every
  image as `/etc/ryft/supabase-ca.crt` from `backend/certs/supabase-ca.crt`; `internal/db`'s
  `ca_test.go` pins its sha256 and fails 90 days before the expiry. It verifies
  `*.pooler.supabase.com` on both ports. The Dashboard's download (Database → Settings → SSL
  Configuration, `prod-ca-2021.crt`) is the same certificate.
- **Transaction mode** gives a client a server connection only for a transaction, so the services
  hold no session state: no session `SET`, `LISTEN`, temp tables, session advisory locks, `PREPARE`
  or `WITH HOLD` cursors (`make session-state-check`, part of `make lint`). The outbox's locks are
  `pg_advisory_xact_lock`. Supavisor doesn't support prepared statements in transaction mode, so pgx
  runs `exec` mode: unnamed statements, one round trip, parameter types from the Go types. A
  `[]byte` therefore goes as `bytea`: pass JSON for a `jsonb` column as a `string`. CI's `pooler`
  job runs the store's tests through PgBouncer in transaction mode with `exec`.
- **Exact floats (D228).** In `exec` mode results come back as text. While `extra_float_digits` is
  0, Postgres prints a float8 with 15 significant digits, so `0.30000000000000004` reads back as
  `0.3`. Supabase's configuration file sets 0 (`SELECT setting, source FROM pg_settings WHERE name =
  'extra_float_digits'` → `0 | configuration file`, on both ports). Migration 0035 sets 1 on the four
  service roles. A role's setting outranks the file and applies when Supavisor opens that role's
  server connection; connections already open keep the old value until they close. The owner keeps
  Supabase's 0: `migrate` reads its results in binary on 5432 and reads no float. To check the roles:
  `SELECT rolname, rolconfig FROM pg_roles WHERE rolname LIKE 'ryft\_%'` → `{extra_float_digits=1}`
  on each. A new service role needs the same setting in its migration, or `internal/db`'s
  `TestServiceRolesReadExactFloats` fails.
- **`db.Open` refuses on Cloud Run** a password in the URL, any `sslmode` but `verify-full`, a
  missing `sslrootcert`, and for a service a port other than 6543, another exec mode, or a startup
  parameter other than `application_name`. It doesn't retry a refused login or `ECIRCUITBREAKER`:
  failed logins trip Supavisor's breaker for the whole role. Wait it out; never retry in a loop.
- **Clients at the maximum:** api 3 × 4 + ingest 2 × 4 + scoring 1 × 2 + notify 1 × 2 + migrate = 26
  (design §4). In transaction mode the pool size (Database → Settings → Connection pooling, default
  15) counts only open transactions. `max-instances` keeps the count bounded.
- **Before the first deploy** (runbook step 7): `make pooler-check` runs `TestPooler`'s read-only
  queries through 6543 as the owner, in a rolled-back transaction, one connection, one attempt. It
  reports the owner's `extra_float_digits 0` and passes. After step 8's migrate, step 9's login as
  `ryft_api` checks the role's setting and an exact float8 (`ryft_api|t|0.30000000000000004`).

## Postgres version

The repo targets **Postgres 17**: `docker-compose.yml` (`postgres:17-alpine`) and the
integration tests (`RYFT_TEST_POSTGRES_URL`, throwaway `postgres:17-alpine`) both use it.
There is no `supabase/config.toml` in this repo (no
`major_version` to read), so the production version is **not confirmed by the repo**:
the hosted project's version is set by Supabase. Confirm it once with
`select version();` in the SQL editor and correct this section if it differs.

## Local development

Compose runs plain Postgres with
`AUTH_MODE=apikey` and `ALLOW_INSECURE_APIKEY_AUTH=true`: the bearer is `API_KEY`, or
`API_KEY:<uuid>` to act as another user. The services refuse to start in that mode
without the flag. Never expose it.

## Moving the project to Frankfurt

Decided (D49): the first project was created in eu-west-1 (Ireland), while the cluster
ran in eu-central-1. It is recreated in Frankfurt before anyone else is invited, while
it holds only test data. A project cannot change region, so this means a new project:

1. **New project** in *Central EU (Frankfurt)*. Work through the checklist above:
   JWT keys, Apple provider, email settings, Data API off, a new smoke-test user. Note
   the new project ref, pooler host (`aws-<n>-eu-central-1.pooler.supabase.com`),
   database password and secret key.
2. **Config and secrets:** a PR sets `SUPABASE_PROJECT_REF` and `SUPABASE_POOLER_HOST` in
   `deploy/run/config.env` to the new values. The new database password and secret key go into
   Secret Manager as new versions of `db-password-owner` and `supabase-secret-key`
   (`OPERATIONS.md` → GCP → 5), and the same PR moves their pins in `deploy/run`. Every
   service's connection string changes, since the pooler user names carry the project ref.
3. **Deploy:** the merge's `deploy.yml` runs the migrate job, which
   builds the empty schema in the new project and sets the role passwords, and the
   services start against it.
4. **App:** switch the Supabase URL and publishable key in the Release config. Accounts
   of the old project do not exist in the new one, so test users sign up again.
5. **Verify:** `make smoke-supabase` against `https://api.ryftlab.de` (`OPERATIONS.md`),
   and the role and RLS queries above.
6. **Retire the old project:** pause it, and delete it after a week with no surprises.

The old project's data is test data and is not migrated. Once real users exist, moving
again would mean exporting `auth.users` and the `public` schema together; avoid it.

