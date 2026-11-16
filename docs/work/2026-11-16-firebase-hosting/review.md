# C1 review (PR #194), 2026-11-16

code-reviewer, read-only, on `git diff origin/main...HEAD` at 52a0ca2. It couldn't run node, `terraform test` or
`check_workflows_test.sh` (refused), so everything but actionlint was desk-checked.

| # | Severity | Where | Finding | Outcome |
| --- | --- | --- | --- | --- |
| 1 | blocker (reviewer) | `infra/gcp/wif.tf:22,57-60`, step 10a, `deploy.yml` `site` | `site-deployer` binds the same principal set as `ryft-deployer`. Code in `site` (firebase-tools and its npx-resolved tree) could mint a token and impersonate the production deployer. | **Accepted risk, to decide before 10f.** The fix is in `infra/gcp` (environment `site`, the condition, both bindings by `attribute.environment`), which the brief excludes, and it needs an apply. Recorded in D233 and OPERATIONS 10a. `SITE_DEPLOY` stays off until the user decides. |
| 2 | should-fix | `deploy.yml` `site`, download-artifact | The artifact was unpacked over the checkout, so it could replace `check-hosting.mjs`, plant `.npmrc` or `node_modules/`. | **Fixed:** unpacked into `$RUNNER_TEMP/site`; anything but `firebase.json` and `out/`, and any link, fails the step; deploy runs from there. |
| 3 | should-fix | `web/scripts/hosting.mjs` | Without `re2` (install scripts off), superstatic's `RegExp` can't parse `(?P<name>…)`. | **Confirmed by CI** (web-e2e: ECONNRESET, then ECONNREFUSED). **Fixed:** pinned CLI in `node_modules/.cache/firebase-tools-<v>`, scripts off, then `npm rebuild re2` alone, and a check that `re2` loads; `firebase-debug.log` printed on failure. web D19 records it as observed. |
| 4 | should-fix | `deploy.yml` `site` | The hold didn't stop a new privacy version reaching `/privacy`, and the live check ran only on a deploy. | **Fixed in part:** while `DEPLOY_HOLD_SERVICES` is set, a deploy that moves `PRIVACY_POLICY_VERSION` is refused (red); the live check runs on every current run. Not covered: a failed migrate in the same run (D233). |
| 5 | nit | OPERATIONS 10g | `//evil.example/` expected "no location", while D19(d) accepts a same-site 301. | **Fixed:** the comment accepts `/evil.example` on ryftlab.de. |
| 6 | nit | OPERATIONS 10a | "before or after C1's merge", but `$FT` reads the version file. | **Fixed:** after C1's merge. |
| 7 | nit | OPERATIONS rollback, `deploy.yml` comment | `SITE_DEPLOY` was turned on before the revert merged. | **Fixed:** merge the revert while it's off, then turn it on and dispatch. |
| 8 | nit | `scripts/check_workflows.sh` | The deploy rule is per-line; setup-node isn't required to set `package-manager-cache: false`. | **Fixed:** `package-manager-cache: false` required, with tests. The per-line limit is recorded in D233. |
| 9 | nit | OPERATIONS EKS sections, `infra/aws/README.md`, `web/docs/STATUS.md` | Stale references to `admission-celtest` and `nginx.test.ts`. | **Fixed:** a "Gone with C1" pointer at the top of each EKS section, and STATUS names the Hosting tests. |

Verified correct by the reviewer:
- the CSP complement and that exactly one rule matches;
- the redirect capture safety;
- `.well-known/` handling end to end;
- check-hosting is built-ins only and counts raw headers;
- the tip/live/concurrency logic;
- the jq guard;
- the TS/ESLint/Prettier desk-check of the tests;
- the CI cleanup;
- the Terraform (validation, counts, test overrides, 10d's plan counts);
- no denylisted brand.
