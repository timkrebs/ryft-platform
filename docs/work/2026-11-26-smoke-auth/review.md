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
