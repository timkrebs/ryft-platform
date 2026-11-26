# Review (code-reviewer, security focus, one pass, 2026-11-26)

There were no blockers. The checks came back sound in these areas:
- Each token route fully verifies the token, and a forged `iss` can only fail.
- The smoke principal opens no admin route, gate or App Attest exemption.
- The config is all-or-nothing, and smoke off behaves as before.
- Pub/Sub push tokens and user tokens stay separate.
- The helper leaks no token, writes its session file 0600, and checks the claims before any network call.
- The deploy smoke steps hold no secret, and `FILL_ME_IN` stops the deploy before migrate.
- `ryft-smoke` has no role and only the ID-token binding.
- The bootstrap order in the docs is correct.

| # | Severity | Finding | Owner | Outcome |
|---|---|---|---|---|
| 1 | should-fix | The `/v1/me` binding only shows that the config matches itself. EnsureUser creates a missing row, so a wrong `SMOKE_USER_ID` would write into another account or create an orphan row. | backend-engineer | The smoke principal never creates a row: a missing or deleted account answers 401 `smoke_unknown_user`. A wrong but existing UUID in config.env is stopped only by PR review (open question). |
| 2 | should-fix | `gcloud auth print-identity-token --impersonate-service-account` probably needs TokenCreator (generateAccessToken), and the operators only get OpenIdTokenCreator. | test-engineer | Replaced by `smoke_auth.py mint <file>` (generateIdToken only). |
| 3 | nit | Rolling back to api-00044 or older breaks the next preflight. | test-engineer | Runbook note: dispatch `smoke=false`, then a full dispatch. |
| 4 | nit | The workflow check covered only the two smoke steps, so job or workflow `env:` or `$GITHUB_ENV` could still carry `SMOKE_TOKEN`. | gitops-engineer | The check now also covers the job and workflow `env:` and `$GITHUB_ENV` writes. |
| 5 | nit | The smoke identity could be configured equal to the push identity or audience. | backend-engineer | `loadSmoke` refuses that. |
| 6 | nit | A leaked token could delete the smoke account. | platform-lead | Stated in D286. |

# Independent security review (Codex, 2026-11-26): needs-attention

| # | Severity | Finding | Verified in code | Outcome |
|---|---|---|---|---|
| 1 | high | A wrong existing UUID lets smoke minters act as that user, and `DELETE /v1/me` skips the user-row check. | Holds: `cmd/api/main.go` DELETE route has no OnAuth; admin auth calls `verifier.Verify`. | Fixed. `smoke_account` binding (0060, operator-only write, checked on every request, 401 `smoke_unbound`). Route allowlist (403 `smoke_route`). Admin refuses the smoke principal. |
| 2 | medium | A URL override sends the production bearer to another host. | Holds: the helper minted for any HTTPS target. | Fixed: Google tokens are minted and sent only to the canonical origins, session file included. 36 lookalike origins tested. |
| 3 | medium | `smoke=false` skips verifying the new revisions. | Holds: the input gated both steps. | Fixed. `auth_preflight` only swaps the preflight for a bootstrap check that passes only against a pre-D286 api. The post-rollout smoke always runs with a fresh token and rolls traffic back on failure. |
| 4 | medium | A slow Google JWKS fetch can fail cold starts. | Holds: `NewGoogleOIDC` blocked for 10 s on a stalled server, measured. | Fixed: built in the background; a smoke token gets 503 until ready; tested with a stalled server (returns in 0.08 s). |
| — | — | Render error for placeholders. | — | It now names the key and its source. |

# Review of the follow-up (code-reviewer, the one pass on c0f44fa)

No blockers.

| # | Severity | Finding | Owner | Outcome |
|---|---|---|---|---|
| 1 | should-fix | The bootstrap passes on any 401, and a D286 api answers 401 for an unbound account too. | gitops-engineer | The bootstrap step also refuses when the serving api revision's env has `SMOKE_OIDC_SA_ID`. |
| 2 | should-fix | A rollout that fails partway is not rolled back. | gitops-engineer | The replace, the smoke and the rollback now run in one step; a failed replace rolls back what moved. |
| 3 | nit | An unbound smoke token touches the user's row before the 401. | backend-engineer | The binding is checked before EnsureUser. |
| 4 | nit | A 503 while the key set is loading fails the smoke and rolls back a good release. | test-engineer | The `/v1/me` check retries a 503; idempotent curl calls get `--retry 3`. |
| 5 | nit | A comment in deploy.yml is misindented. | gitops-engineer | Indented. |

Fixed without a second review pass (the brief allows one).

# Second independent review (Codex, of 609817e): do not ship

The full text is in `../docs/work/2026-11-26-smoke-auth-rollout/codex-review-2.md` (root repo). The lead checked each finding against the code before assigning it.

| # | Severity | Finding | Verified in code | Owner | Outcome |
|---|---|---|---|---|---|
| 1 | high | A failed `services describe` after a successful replace exits the step (bash `-e`) before the service is recorded, the smoke runs, or anything rolls back. | Holds: `rev=$(gcloud … describe …)` at deploy.yml:438 has no guard. | gitops-engineer | _pending_ |
| 2 | high | `TestBindSmokeAccountScript` scans two columns with `pgx.RowTo[string]`, so it fails after a successful bind. CI's `test` job runs it (RYFT_TEST_POSTGRES_URL), and ci-ok blocks every deploy. | Holds. | database-engineer | _pending_ |
| 3 | medium | The smoke bearer can grant and revoke consent (POST/DELETE `/v1/me/consent` are on the allowlist). | Holds: smoke.go:29-30; smoke_prod.sh:100, 150-153. | backend-engineer | _pending_ |
| 4 | medium | The bind SQL takes its UUID from the same source as config.env, so nothing cross-checks it. | Holds. | database-engineer | _pending_ |
| 5 | medium | The automatic rollback can restore a pre-D285 api that erases meal labels. | Holds as a narrow window. The live api is `api-00044-5lh`, from before #249: the #249 deploy (run 45777989628) failed at the preflight. If #248 reaches main before a D285 api is serving, a failed smoke rolls back to -00044. | gitops-engineer | _pending_ |
| 6 | rec. | Replace the deliberately failing bootstrap run with a migrate-only dispatch. | Accepted. | gitops-engineer, lead (docs) | _pending_ |

Hand-backs as received (unverified until the lead's runs below):
- gitops: one `trap on_exit EXIT`, each service on `touched` before its replace; executed cases a–d plus three more. A `record` step with outputs `d286`/`d285` (yes|no). The floor uses git grep and fails closed. `api.yaml` label `ryft-meal-patch: d285`, required by check_cloudrun.py. `auth_preflight` removed, `migrate_only` added; the selection is tested row by row. Its tests passed locally (yq present).
- database: the bind script requires exactly one `auth.users` row with `smoke@ryftlab.de` (lower()) whose id equals the pasted UUID. New `consent_smoke_account.sql` acts only on the bound account. The test now scans user_id and bound_at. NOT RUN (sandbox).
- Lead's local runs: `go vet ./...` and `go test` (cmd, auth, db, config, store; the DB cases skip without Postgres), `smoke_auth_test.sh` (56 tests), `check_workflows_test.sh`, `make cloudrun-check`, `check_cloudrun_test.sh` and `cloudrun_render_test.sh` all passed. No Postgres could be started, so the DB tests run first in CI.
- backend: POST/DELETE consent are off the allowlist and answer 403 (`smoke_route` metric and access reason; the body is the generic `forbidden`). smoke_prod.sh writes no consent and fails with the consent_smoke_account.sql hint. `prepare` checks consent against config.env's PRIVACY_POLICY_VERSION. NOT RUN (sandbox).

# Review of the second-review fixes (code-reviewer, the one pass on 40c29f1)

No blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The consent check wants an exact match with the newest consent. A consent recorded ahead for a bump that is later reverted, or recorded before the merge, blocks every deploy. | Fixed (lead). Only a missing consent fails. Another version warns, since the sync gate takes any unrevoked consent. Tests in smoke_auth_test.sh (python and shell). |
| 2 | should-fix | The bootstrap runbook has no consent check between the bind and the dispatch. | Fixed: a SQL check in OPERATIONS → The bootstrap, plus the consent script's header. |
| 3 | should-fix | `d286` counts an empty `SMOKE_OIDC_SA_ID` as D286. After the route is turned off by emptying it, every preflight answers 401 and no bootstrap is offered. | Fixed: a non-empty value is required. New `rev_d286empty` case in check_workflows_test.sh. |
| 4 | should-fix | The docs say "every api since #249 carries the label". It is since #250. | Fixed in the docs and in the floor's error text. |
| 5 | nit | The consent SQL header says "before or right after the deploy". | Fixed. |
| 6 | nit | errexit inside `on_exit` could skip later rollbacks; the step has no time budget of its own. | `set +e` in the trap. No step timeout: the runner would kill the trap too, and the job limit (60 min) stays. |
| 7 | nit | The floor's grep also counts `testdata/` and read-only queries (safe side). | Documented in OPERATIONS. |

CI on 40c29f1: `TestBindSmokeAccountScript` and `TestConsentSmokeAccountScript` passed (finding 2 is proven). `TestMigrateSupabaseShape` failed ("want a service policy on each of the 43 tables, got 42"), as it already did on 609817e: 0060 named its policy `smoke_account_read`. It was renamed `smoke_account_services`, like every service policy (0032's is SELECT only too). 0060 has never been applied anywhere, so editing it is allowed.
