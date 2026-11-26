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
