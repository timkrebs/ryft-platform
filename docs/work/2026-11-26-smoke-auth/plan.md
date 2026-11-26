# Production smoke auth without email sign-in (2026-11-26)

## Why
The user turned the Supabase email provider off on 2026-11-26 (Apple and Google only from now on). The production
smoke signed in by email/password or used a hand-set `SMOKE_TOKEN`; run 45777989628 (main 4bca49d, #249) stopped in
"Verify production smoke authentication" with `SMOKE_TOKEN has expired`. Production still runs api-00044 /
ingest-00044 from 2026-11-25; 0053/0054 are not applied.

## What the API accepts today
`internal/auth/supabase.go`: ES256/RS256 only (no HS256, no `none`) from the project JWKS, `iss`
`https://<ref>.supabase.co/auth/v1`, `aud` `authenticated`, `exp` and `iat` required, `role` `authenticated`, not
anonymous, UUID `sub`. Both api and ingest use it (`auth.FromConfig`). The smoke calls both.

## Options weighed (D286)
| | Works against api-00044 | Who can act as whom | Secret material |
|---|---|---|---|
| A. Mint with an imported Supabase signing key (standby keys are published in the JWKS) | yes | the key holder can mint **any** user, any app_metadata role, and `role: service_role` for the Data API | a long-lived private key (Secret Manager or GitHub) |
| A'. Legacy HS256 secret | no: the API accepts no HS256 | any user, plus the Data API | the project's JWT secret |
| D. Admin `generate_link` + `verify` | unknown with email off; not tested against production | the service-role key: every user and the whole database bypassing RLS | the service-role key in the deploy job |
| E. Supabase `id_token` grant with a service account's Google token | needs a Supabase provider change and a new identity on the smoke user | the smoke user | none, but it depends on GoTrue accepting a service-account token |
| **B. The API accepts a Google ID token of the service account `ryft-smoke` as the smoke user, and only that user** | no: one deploy without the preflight | **only the smoke account** | **none**: Google signs, IAM decides who may mint |

Chosen: **B**. It is the only option where a leaked credential (a 1-hour token) or a misused identity can act as
nobody but the smoke account, and there is no key to store, rotate or leak. It reuses the tested Google OIDC
verifier of the Pub/Sub pushes (D218). Cost: an API change on the auth path (reviewed), Terraform for the account,
and a one-off bootstrap deploy without the auth preflight, because api-00044 does not know the new path.

## Contract (shared by every ticket)
- **Cloud Run env, api and ingest**: `SMOKE_OIDC_SA_EMAIL` (fixed in the templates:
  `ryft-smoke@ryftlab-prod.iam.gserviceaccount.com`), `SMOKE_OIDC_SA_ID` (its unique ID, digits, from config.env
  `SMOKE_SA_ID`), `SMOKE_OIDC_AUDIENCE` (fixed: `ryft-smoke`), `SMOKE_USER_ID` (the smoke account UUID, from
  config.env `SMOKE_USER_ID`). All four set: on. All four empty: off. Anything in between: the service refuses to
  start (config error, no value in the message). Set in apikey mode: refused.
- **Token**: Google-signed ID token from `iamcredentials generateIdToken` with `includeEmail: true`: RS256 from
  Google's JWKS, `iss` Google's, `aud` exactly `SMOKE_OIDC_AUDIENCE`, `email` == `SMOKE_OIDC_SA_EMAIL` with
  `email_verified` true, `sub` == `SMOKE_OIDC_SA_ID`, `exp` required (Google: 1 h).
- **Routing**: the unverified payload's `iss` is Google's and smoke is on → the smoke verifier; anything else → the
  Supabase verifier, as today. Each is then fully verified by its own parser; a wrong route only fails.
- **Principal**: `UserID` = `SMOKE_USER_ID`; `Email`, `Provider`, `Name`, `AppRole`, `AAL` empty, `MFAAt` zero (so
  EnsureUser keeps the stored email/provider and no operator route opens). Failure reasons prefixed `smoke_`.
- **Ready**: the Supabase verifier's only (a Google JWKS blip must not make api unready; smoke requests get 503).
- **config.env**: `SMOKE_USER_ID=FILL_ME_IN`, `SMOKE_SA_ID=FILL_ME_IN`, required (deploy step 1 refuses a
  placeholder; `--lint` in CI lets it pass). Neither is a secret.
- **Client** (`scripts/lib/smoke_auth.py`): by default reads `SMOKE_USER_ID` and `SMOKE_SA_ID` from the env or
  `deploy/run/config.env`, gets an access token from `gcloud auth print-access-token`, calls
  `POST https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/<SA>:generateIdToken`
  `{"audience":"ryft-smoke","includeEmail":true}`, checks the token's claims before any API call, then requires
  `GET /v1/me` to return `SMOKE_USER_ID`. `SMOKE_TOKEN` stays (loopback fixture; remote: a Supabase JWT with `sub`
  == `SMOKE_USER_ID` or a Google token with the claims above). The password path goes.
- **IAM**: new account `ryft-smoke` with no roles; `roles/iam.serviceAccountOpenIdTokenCreator` on it for
  `ryft-deployer` and for the members of a variable (operators, set in the user's tfvars).

## Tickets
| # | Owner | Files | Acceptance |
|---|---|---|---|
| T1 | backend-engineer | `backend/internal/config/**`, `backend/internal/auth/**` | contract above; table tests: valid smoke token → smoke principal; wrong aud/email/sub/unverified email/expired/HS256/none → 401; Supabase tokens unchanged; Google token while off → 401; partial config refused; `go vet`, `go test ./...` green |
| T2 | gitops-engineer | `deploy/run/**`, `scripts/cloudrun_render.py`, `scripts/check_cloudrun.py`, `.github/workflows/deploy.yml`, `scripts/check_workflows.sh`, their tests | env in api.yaml and ingest.yaml; keys in config.env; preflight step passes no secret (no SMOKE_EMAIL/PASSWORD/TOKEN/ANON_KEY); `make cloudrun-check`, `scripts/tests/*` green |
| T3 | infra-engineer | `infra/gcp/**` | `ryft-smoke` account, two bindings, output `smoke_sa_unique_id`; `terraform validate` and `terraform test` green; no apply |
| T4 | test-engineer | `scripts/lib/smoke_auth.py`, `scripts/smoke_prod.sh`, `scripts/tests/smoke_auth_test.sh`, `docs/OPERATIONS.md` (smoke sections) | client per contract; tests extended (mint request shape, claim checks before network, no token in errors, /v1/me binding, loopback fixture kept); OPERATIONS deploy/rollback/rotate and local operator steps |
| — | platform-lead | `docs/DECISIONS.md` D286, `docs/STATUS.md`, this folder, PR | |

## Risks
- The first deploy after the merge cannot verify smoke auth before rollout (api-00044 rejects the new token):
  the push run stops at the preflight, nothing rolls; a dispatch with `smoke=false` rolls; a second dispatch runs
  the full smoke. Rollback: `gcloud run services update-traffic api --to-revisions=api-00044-5lh=100` (and ingest).
- A bug on the auth routing path would affect every app request: covered by tests and the review.

## Needs the user (in order)
1. Terraform plan/apply (`ryft-smoke`, bindings). 2. Commit `SMOKE_USER_ID` and `SMOKE_SA_ID` into config.env on
   the PR branch. 3. Merge. 4. Dispatch `smoke=false`, then a full dispatch. 5. Delete the old secrets.

## Follow-up: independent security review (Codex), 2026-11-26
Each finding was checked in the code and holds:
1. **high.** `DELETE /v1/me` runs `auth.Middleware` without OnAuth (`cmd/api/main.go:296-300`). A smoke principal would reach `deleteMe` for whatever UUID `config.env` names. The admin auth calls `verifier.Verify` directly (`adminauth.go:284`).
2. **medium.** `smoke_auth.py` mints a production Google token for any HTTPS `API_URL`/`INGEST_URL`.
3. **medium.** `smoke=false` skips both the preflight and the post-rollout smoke.
4. **medium.** `NewGoogleOIDC` fetches the JWKS inside the constructor, so a stalled Google endpoint delays every cold start.

### Contract v2
- **Binding (S1, database-engineer).** A new migration adds table `smoke_account`:
  - Columns: `singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton)`; `user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE`; `bound_at timestamptz NOT NULL DEFAULT now()`.
  - Access: api and ingest get SELECT only, and no service role may write it. RLS is on with no policies, per the repo's conventions.
  - The operator binds the account once with `scripts/sql/bind_smoke_account.sql`, which refuses an account with a non-email identity.
- **Server (S2, backend-engineer).**
  - `store.IsSmokeAccount(ctx, id) (bool, error)` lives in `store/smoke.go`.
  - On every smoke request, ensureUser (api, ingest) requires the row to exist and to be bound. Otherwise it answers 401 `smoke_unbound`, failing closed, and a DB error is 503.
  - `auth.Options.SmokeAllow func(*http.Request) bool` is checked in Middleware right after Verify. A nil allowlist or a route not on it gets 403 `smoke_route`, so `DELETE /v1/me`, export, merge, train import and nutrition are rejected.
  - api allows exactly the routes `smoke_prod.sh` calls with the token. ingest allows `/v1/sync`.
  - The admin auth rejects `p.Smoke`.
  - The Google verifier for the smoke is built asynchronously. Until it is ready, smoke tokens get 503 `smoke_jwks_unavailable`, and Supabase tokens never wait on it.
- **Client (S3, test-engineer).** The helper mints and sends Google tokens only when API = `https://api.ryftlab.de` and INGEST = `https://ingest.ryftlab.de`; that includes a loaded session file. Other remote targets need `SMOKE_TOKEN` that is not a Google smoke token.
  - New mode `bootstrap`: mint a token, then require `GET /v1/me` to answer 401, which means the running revision predates D286. A 200 is refused with "bootstrap is over".
- **Deploy (S4, gitops-engineer).**
  - The dispatch input `auth_preflight` (default true) replaces `smoke`. Set to false, it runs `smoke_prod.sh --auth-bootstrap`, which only passes against a pre-D286 revision.
  - Before the rollout, record each service's serving revision. After the rollout, `make smoke-prod` always runs with a freshly minted token. On failure, traffic goes back to the recorded revisions in the order api, ingest, notify, scoring, and the run fails.
  - The render error names the missing key and its source.
