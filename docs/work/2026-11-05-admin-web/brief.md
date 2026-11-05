# RYFT Platform: an admin area on ryftapp.de (Supabase sign-in with MFA, invites by email, codes on the phone)

Brief for `platform-lead`, pasted by the user on 2026-11-05 (the file `CLAUDE_FOLLOWUP_ADMIN_WEB.md` was not in the
tree; this is the verbatim brief). Owners: **platform-architect** (the auth design, D), **backend-engineer** (api:
admin auth, CORS, invite changes), **database-engineer** (migration 0032), **web-engineer** (the `/admin` pages),
**security-privacy-reviewer** (a full pass: this opens a browser path to admin routes over health-data accounts),
**test-engineer**, **gitops-engineer** (config and secrets through Vault), **sre** (alerts); **code-reviewer** on every
PR. Four PRs, in order.

## The problem

On Saturday a tester wanted to join and the operator had no `RYFT-…` code at hand. Codes exist only through
`POST /v1/admin/invites` with the long-lived `ADMIN_API_KEY` from a laptop (`OPERATIONS.md` → Beta, `curl`), and a
code is anonymous: nothing ties it to the person it was made for. The operator needs, from a phone, to sign in, type
an email address, get a code and a link to send, and see who has redeemed what.

What exists and stays: `beta_invite_codes` (0017), `POST /v1/admin/invites` (note, max uses, expiry), `GET`/`DELETE`,
`POST /v1/me/invite` (redeem; 5 attempts per hour), the admin audit (`audited`, `AuditSince`), `GET /v1/admin/users`
with grant/revoke, feedback and flags routes, the universal link `https://ryftapp.de/invite/<code>` that opens the app
(nginx `/invite/*`, the AASA), and the site's hard CSP (`connect-src 'none'`, no third parties, no cookies). The web
is a static Next.js export behind nginx: there is no server to hold a session, so the admin area is a client-rendered
page that talks to `api.ryftapp.de` with a Supabase token, exactly as the app does.

Read first: `backend/cmd/api/{audit,beta,config,main}.go` (`adminOnly`, `audited`, `keyFingerprint`, the invite
handlers), `backend/internal/auth/auth.go` (the JWKS verifier and `Claims`), `backend/internal/db/migrations/0017_beta.sql`,
`backend/internal/store` invites, `docs/API.md` → Admin, Onboarding — invite, Errors; `docs/OPERATIONS.md` → Beta
(gates, invites), Apex site (CSP, AASA, nginx), `docs/SUPABASE.md`; `web/nginx/{csp,headers,default}.conf`,
`web/scripts/csp.mjs`, `web/src/views/Beta.tsx`, `web/docs/BRAND_CHECK.md`, `web/e2e`.

## The design (platform-architect records it as `D<n>`)

**Who is an admin.** A Supabase user of the same project whose `app_metadata.role` is `"admin"`. `app_metadata` is
writable only with the service role (dashboard SQL or the service key), never by the user, so the claim is trustworthy
in the JWT. The operator sets it once (user step below). Nothing else changes about that account; it can also be a
normal app account, but recommend a separate one (`admin@ryftapp.de`) so a phone sign-out of the app never touches it.

**How admin routes authenticate.** `adminOnly` accepts two bearers: the existing `ADMIN_API_KEY` (scripts, smoke, the
deploy watch; unchanged) **or** a Supabase access token that verifies against the JWKS like every `/v1/*` token *and*
carries `app_metadata.role == "admin"` *and* `aal == "aal2"` (a second factor was used in this session). Anything else
is `401` (`WWW-Authenticate` as today); a valid admin token without `aal2` is `403 code: mfa_required`, and the page
sends the user to MFA enrolment or challenge. Token lifetime is Supabase's (1 h access, refresh in the browser's
storage, no cookie). The audit row's actor becomes `{"kind": "key", "id": <fingerprint>}` or `{"kind": "user", "id":
<uuid>}`; `AuditSince` and `GET /v1/admin/audit` show it; the log line says `admin_actor`.

**Where the browser may come from.** `ADMIN_WEB_ORIGINS` (config; prod `https://ryftapp.de`, local the dev origin): a
CORS middleware on `/v1/admin/*` only, exact-match origins, `Authorization` and `Content-Type` allowed, methods
`GET, POST, PUT, DELETE`, `Vary: Origin`, preflight cached 10 min, credentials off (there are none). No CORS on any
other route; the app doesn't need it and nothing else should.

**Invites tied to a person.** `beta_invite_codes` gains `email text NULL` (lower-cased, validated), `created_by text
NOT NULL` (the actor), `revoked_at timestamptz NULL`, `redeemed_by uuid[]`-equivalent via a small `beta_invite_uses
(code, user_id, at)` table so the list shows who used a code. When `email` is set, `POST /v1/me/invite` accepts the
code only from an account whose token email matches (case-insensitive); otherwise `422 "invalid or used code"` (the
same message as today, no oracle). Sign in with Apple accounts with a private relay address won't match: the page
says so and offers "anyone with the code" as the alternative, which is today's behaviour. A revoked code redeems
nothing; `DELETE` becomes revoke (keeps the row for the audit); a hard delete is not offered.

**What the page can do (v1, on a phone first).** Sign in (email + password; MFA enrol with a QR code on first use,
TOTP challenge afterwards; sign out). **Invites:** "New invite" with email (optional), note, uses (default 1), expiry
(default 14 days) → the code, the link `https://ryftapp.de/invite/RYFT-XXXX-XXXX`, a **Copy** button for each and a
**Share** button (`navigator.share`, so the operator can send it by iMessage, WhatsApp or Mail from the phone). The
list: code, for whom, note, uses / max, expires, status (open, used, expired, revoked), who redeemed it (email of
the account) and when; revoke. **Users:** the existing list (email, beta access, last sync) with grant/revoke access,
search by email. **Feedback:** the existing list with resolve. Flags are shown read-only in v1 (changing them stays
with the scripts until a second admin exists; D). No charts, no health data anywhere on these pages: the user detail
route (`GET /v1/admin/users/{id}`) is **not** called from the page in v1 (it shows sync details; keep it to `user.sh`).

**Sending the email is v2.** v1 is copy and share: there is no transactional mail sender yet (Google Workspace mail
is pending; SES would need its own DKIM). Record the decision and the v2 shape in `DECISIONS.md` (api sends the link
from `beta@ryftapp.de` via SES or the Workspace SMTP relay; the page gets a "Send" button). Don't build it now.

**Security posture, in one place (security-privacy-reviewer signs off on each):** the admin page is `noindex`, not
linked from the site, and `/admin` is in `robots.txt` disallow; the page ships no secret (the Supabase URL and anon key
are public by design, the API URL is public); Supabase's **email signups must be off** or admin-gated for this project if
they aren't (the app uses Sign in with Apple and email; check `SUPABASE.md` and say what's true; an attacker signing up
gets no `role`, so the gate holds either way, but say it); **MFA (TOTP) enabled** in the Supabase project; the
`/v1/admin/*` rate limit class gets a per-IP limiter (10 req/s burst 20) in addition to the per-user one; the alert
`AdminUnauthorized` (warning: > 20 `unauthorized` admin responses in 10 min) and a daily `info` digest line in the
existing admin audit (`NewFeedback` style) "N admin actions by user <id>". The site's CSP: `connect-src` becomes
`'self' https://api.ryftapp.de https://<project-ref>.supabase.co` for the whole site (one header; nginx has no
per-route CSP without a second location block; **prefer a second `location /admin/` block with its own CSP** so the
marketing pages keep `connect-src 'none'`; `scripts/csp.mjs` learns both). The footer's "nothing loaded from third
parties" stays true: supabase-js is bundled, nothing is fetched from a CDN; the admin page's own connections are named
on the page's footer ("Signs in with Supabase Auth; talks to api.ryftapp.de"). No cookies: supabase-js keeps the session
in `localStorage`; `persistSession` on, `autoRefreshToken` on, `detectSessionInUrl` off (no OAuth redirect flows here).

## PR 1 · `api: admin auth by Supabase token with MFA, CORS for the admin origin, actor in the audit`

- `adminOnly(key, verifier, origins)`: key **or** verified admin token (`role` from `app_metadata`, `aal` claim); the
  three outcomes above; `metrics.AdminRequests{result}` gains `mfa_required`. `auth.Claims` gains `Role` and `AAL`
  (parsed from the token; tests with hand-signed tokens against a test JWKS, as the existing auth tests do).
- CORS middleware in `httpx` (generic: origins, methods, headers), applied in `adminRoutes` only; preflight answers
  `204` without auth; tests: allowed origin, other origin (no CORS headers, request still needs auth), preflight.
- Audit actor; `GET /v1/admin/audit` shape (`actor` object, additive); `API.md` → Admin documents both bearers,
  `mfa_required`, CORS, and that the key stays for scripts.
- Config: `ADMIN_WEB_ORIGINS` (comma-separated), validated at start-up (`https://` only, no trailing slash); `infra/vault`
  `config.env` gets it for api; `docs/OPERATIONS.md` → Beta names it.
- `make smoke-prod` keeps using the key; a new unit-level test proves an app user's token (no role) is `401` on every
  admin route.

## PR 2 · `api/db: invites for a person, revoke, who redeemed`

- Migration `0032_invites_for_email.sql`: `email`, `created_by`, `revoked_at` on `beta_invite_codes`;
  `beta_invite_uses(code, user_id, at)` with a unique `(code, user_id)`; backfill `created_by = 'key'` for existing rows.
  Additive; RLS as for the table today (api role only).
- `BetaInviteInput` gains `email?`; the response and the list gain `email`, `created_by`, `revoked_at`, `link`
  (`https://ryftapp.de/invite/<code>`; the base from config, `PUBLIC_WEB_URL`), `uses_by: [{user_email, at}]`
  (email from `users`, the one field the page needs; no names, no health data). `DELETE /v1/admin/invites/{code}` sets
  `revoked_at`. `POST /v1/me/invite`: the email binding and the revoked check, one `422` message, the 5/h limiter
  unchanged; tests: bound code with the right email, wrong email, relay address, revoked, used, expired, the
  idempotent redeem for an account that already has access.
- `API.md`: fields, `changelog`, Swift mirror untouched (the app's `InvitePost` doesn't change; note it).

## PR 3 · `web: /admin: sign in, invites, users, feedback`

- `web/src/app/(en)/admin/` (and nothing under `[locale]`): client components only (`"use client"`, no server actions,
  static export stays), `@supabase/supabase-js` pinned (licence listed on `/licenses`), `NEXT_PUBLIC_SUPABASE_URL`,
  `NEXT_PUBLIC_SUPABASE_ANON_KEY`, `NEXT_PUBLIC_API_URL` as build args in the Dockerfile and CI (public values; from
  `config/`, not secrets). A tiny API client with the bearer from `supabase.auth.getSession()`, `mfa_required` →
  the MFA screen, `401` → sign-in, every error shown with the request id as the app does.
- Screens: Sign in · MFA enrol (QR + manual secret, verify) · MFA challenge · Invites (list, new, copy, share,
  revoke) · Users (list, search, grant/revoke) · Feedback (list, resolve) · Flags (read-only). Phone-first: 44 pt
  targets, one column, the code in large monospaced type, Copy and Share as the primary buttons. Design tokens and
  components from the site's own system (`/styleguide`); nothing new visually beyond a table and a form; `web-engineer`
  shows screenshots at 390 px and 1280 px in the PR.
- nginx: `location /admin/` with its own CSP (`connect-src 'self' https://api.ryftapp.de https://<ref>.supabase.co`,
  `script-src` hashes as the rest, `frame-ancestors 'none'`), `X-Robots-Tag: noindex`; `robots.ts` disallows `/admin`;
  `sitemap.ts` excludes it; `scripts/csp.mjs` generates both headers; `web/docs/BRAND_CHECK.md` runs over the new pages.
- Tests: Playwright e2e against a mocked API and a mocked Supabase auth (route interception): sign-in, `mfa_required`
  path, create an invite with email, copy, share fallback when `navigator.share` is absent, revoke, grant access;
  axe on every screen; the link check; Lighthouse budget unchanged. `web-e2e` in CI gains the admin pages.

## PR 4 · `docs/ops: the admin area` and the user's steps

- `OPERATIONS.md` → Beta: "Inviting a tester from your phone" (sign in → New invite → email → Share), what a bound
  code does, revoke, and the key path kept for scripts; → Supabase: the admin role, MFA, signups; → Apex site: the
  second CSP. `SUPABASE.md`: the role claim and why `app_metadata`. `API.md` done in PR 1/2. `DECISIONS.md`: the D
  above, the v2 mail decision. `STATUS.md` → the user's steps, in order:
  1. Supabase dashboard → Authentication → MFA: enable TOTP. → Providers: confirm email signups' state (document it).
  2. Create the admin account (email + a long password in the password manager), then in the SQL editor as owner:
     `update auth.users set raw_app_meta_data = coalesce(raw_app_meta_data, '{}'::jsonb) || '{"role":"admin"}' where
     email = '<admin email>';` (the exact statement is in OPERATIONS; nothing else in `auth.users` is touched).
  3. `infra/vault`: `ADMIN_WEB_ORIGINS=https://ryftapp.de`, `PUBLIC_WEB_URL=https://ryftapp.de` in api's `config.env`;
     `terraform apply`; the rollout follows (hashed ConfigMap, D111).
  4. Merge PR 1–3 (CI → promote PRs → Argo CD). Verify: `https://ryftapp.de/admin` → sign in → MFA enrol → New invite
     for your own second account → redeem it in the app via the shared link → the list shows the use.
  5. `make smoke-prod` still green (the key path).
- Alerts (`sre`): `AdminUnauthorized`; the admin audit digest; promtool tests; `OBSERVABILITY.md` rows.

## Done

An admin on a phone signs in with a second factor, creates a code bound to a tester's email, shares the link, and
sees the redemption; an app user's token is refused on every admin route; the marketing pages' CSP is unchanged; the
key still works for scripts and smoke; every admin action names its actor in the audit; nothing on `/admin` shows
health data.

Until this ships: create five single-use codes with notes now (`OPERATIONS.md` → Beta, the `curl`) and keep them in
your password manager, so the next Saturday has a code ready.
