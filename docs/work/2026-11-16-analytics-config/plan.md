# 2026-11-16: `config: analytics` and the backend dependencies of #176

The brief: the head-architect's follow-up of 2026-11-16 (two PRs, in this order). The product plan is
`../docs/work/2026-11-16-analytics-posthog/plan.md` in the root folder (decisions 4–6, "Order" step 1, approved
by the user on 2026-11-16). That file lies outside this repository and could not be read in this session; the
contract below follows the brief.

## PR 1: `config: analytics` (branch `config/analytics`, D231)

The app's product analytics go to PostHog EU Cloud: opt-in, pseudonymous, a fixed event list, no IP. Both repos
are public, so the project key is not compiled into the app: the api serves it.

| # | Ticket | Acceptance | Owner |
| --- | --- | --- | --- |
| 1 | Contract: `GET /v1/config` → `analytics` (`{host, project_key}` or `null`), flag `analytics.enabled` (enabled, 0 %) | API.md: field and flag tables, the example (type-checked by `make check-api-doc`), the Swift mirror's optional `analytics`, a 2026-11-16 changelog entry with what the app must do; additive only | lead |
| 2 | `config.LoadAnalytics`: `POSTHOG_PROJECT_KEY` (empty → off), `POSTHOG_HOST` (default `https://eu.i.posthog.com`, https origin only) | a non-https or non-origin host stops the api at start-up; a value that is not a project key (`phc_…`) stops it too; no error quotes either value | lead |
| 3 | The api: load it before connecting, serve it in `GET /v1/config` | `analytics: null` without the key, the object with it; the key in no log line of the start or of the request | lead |
| 4 | Migration 0036: `analytics.enabled` enabled at 0 % | seeded once, never overwritten, rerunnable; `TestRemoteConfig` lists it | lead |
| 5 | `infra/gcp`: secret `posthog-project-key`, accessor `ryft-api` only | `terraform test` (iam, guards) with 12 containers; no version in Terraform | lead |
| 6 | `deploy/run`: the pin comes later (deploy safety, below); `check_cloudrun.py` knows the secret and accepts its pin in api.yaml only | `make test-scripts` covers: no pin ok, pin by number ok, `latest` refused, in another service refused | lead |
| 7 | Docs: OPERATIONS (Feature flags, GCP step 5, the pin), DECISIONS D231, STATUS | the user's commands in order | lead |
| 8 | Review | code-reviewer (both PRs) and security-privacy-reviewer (PR 1), read-only; findings in `review.md` | reviewers |

**Deploy safety (the merge must not break the deploy while the secret has no version).** Cloud Run refuses a
revision that reads a secret version that does not exist, and `deploy.yml` replaces api last: a pin in this PR,
merged before the user's `apply` and `put`, leaves every deploy red (api on its old revision) until the value is
there. The deploy has no check of its own that could catch it (the deployer cannot read Secret Manager; its roles
are pinned by `tests/iam.tftest.hcl`), and a `FILL_ME_IN` in `config.env` would stop every deploy, PR 2's
included. So this PR ships **without the pin**: the api reads `POSTHOG_PROJECT_KEY` when it is set and serves
`analytics: null` until then. After the merge, in any order with the other PRs: `apply`, `put`, then a one-line
pin PR (OPERATIONS → GCP → Another secret's pin), whose deploy is the first that reads the secret. The flag is
dark (0 %) either way, and the app sends nothing while `analytics` is `null`.

**Rollback:** revert the merge (the migration's row stays; the flag can be turned off with the admin route).
The secret container has `prevent_destroy`; an unwanted value is disabled, not destroyed.

## PR 2: the backend dependencies of #176 (branch `deps/backend-2026-11`, D232)

`TestExecModeEncoding/[]string,_quoted_where_needed` fails on #176: pgx 5.12 sends `{a,"b c",…}` where 5.9.2
sent `{a,b c,…}`. Find what changed, whether Postgres reads both the same, and which production queries pass a
`[]string`. Then take all 7 updates and fix the test (if the encoding is correct), or keep pgx 5.9.2 with a
Dependabot `ignore` for `>=5.12` (if not). `vulns` stays green.

## Needs the user
1. Merge PR 1 and PR 2 (either order; #189 too).
2. PR 1: `terraform -chdir=infra/gcp plan` / `apply` (one secret, one binding), the hidden-prompt `put`, then the
   pin PR. Exact commands in the PR body and OPERATIONS.
3. PR 1: the tester allow-list for `analytics.enabled` once the app build is out (Feature flags).
