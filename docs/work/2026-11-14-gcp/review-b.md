# Ticket B: review record (2026-11-14)

One pass on `17d231d` by security-privacy-reviewer and sre, both read-only, then a fix round by the owners
(infra-engineer: `infra/gcp`, OPERATIONS; gitops-engineer: the workflow, scripts, `deploy/run`, alerts). No blockers.

| # | From | Finding | Outcome |
|---|---|---|---|
| 1 | security | The deployer's project-level `run.developer` can create other services and jobs as any runtime SA, and delete mappings | Fixed. Project-level only while `serve = false`; after that, resource-level on the four services and `migrate` |
| 2 | security | `ci-ok` can be forged: a branch workflow with `checks: write` posts a check run of that name on main's SHA | Fixed. The wait reads `ci.yml`'s own push run on main and its `ci-ok` job (`actions: read`) |
| 3 | security | `check_workflows.sh` rule 7 passes `… && always()` | Fixed. `always()`, `cancelled()`, `failure()`, `success()` and `continue-on-error` are refused in deploy.yml |
| 4 | security, sre | An older commit can deploy last (out-of-order `ci-ok`, a re-run) | Fixed. The run checks main's head on every poll and before `migrate`, and ends green as "superseded" otherwise |
| 5 | security | Root-level third-party code runs while the job holds a token: a floating BuildKit image, and Trivy with `docker.sock` | Fixed. No buildx action; the runner's Docker; Trivy reads a tarball, read-only; the sign-in comes after the scan; the pushed image is the scanned one |
| 6 | sre | The startup probe on `/health/ready` keeps every cold instance down during a database outage, so DatabaseDown never fires and failed starts bill | Fixed. The startup probe is `/health/live` (1 s × 20); ticket-b.md's interface is updated |
| 7 | sre | Artifact Registry never deletes tagged versions | Fixed. A DELETE rule for any version older than 30 days; keep-5 wins |
| 8 | sre | Step 11's order pages: `alerts-apply` before A, the site checks before C1 | Fixed. Step 11 is re-lettered by what each part needs (B, A, C1) |
| 9 | sre | The alert routing is never proven | Fixed. Step 11j: preview routing for one rule of each kind, with a fallback |
| 10 | sre | SyncToScoreSlow, APNsFailing and InviteEmailPurgeFailing miss the new-series term | Fixed, with tests |
| 11 | security, note | WIF without `runner_environment` | Fixed (`github-hosted`) |
| 12 | security, note | Token mints aren't in the Data Access logs | Fixed (DATA_READ for `iamcredentials`, `sts`) |
| 13 | security, note | `_member` keeps a hand-added `allUsers` | Fixed. Authoritative `_binding` for `run.invoker` per service |
| 14 | security, note | `enforce_in_transit` | Refused by the lead after the fix round: it would reject the Scheduler's publish and operator pulls through the global endpoint (ticket-b.md) |
| 15 | sre, note | `migrate` `maxRetries: 1` feeds the circuit breaker | Fixed (`0`) |
| 16 | sre, note | ApiDown pages on a single run | Fixed (`[20m]`, SiteDown's shape) |
| 17 | gitops (B3) | Migrate logs `schema current`, not `up to date` | Fixed in the runbook. The design's §11 step 8 wording is wrong |
| 18 | gitops (B3) | `rotate_db_password.sh` said "disable once green" | Fixed: after a day (§4) |
| — | security, note | Base images by tag (`backend/Dockerfile`) | Ticket A's file: pin by digest there |
| — | security, note | Gates can be overridden by workspace variables, `-var` or an untracked `*.auto.tfvars` | Runbook: read the `gates` output at every apply |
| — | sre, note | `deploy_watch.sh` doesn't watch `deploy.yml` | Left for D3, with the EKS deploy watch |
| — | sre, note | The €20 stop line leaves scoring and notify reachable to Pub/Sub | Accepted: without api/ingest traffic and the tick nothing publishes, and both scale to zero |
| — | security | Privacy: Google as processor (events, request logs with IP and user agent, traces), and the DPA acceptance | C2's draft; the Cloud Data Processing Addendum is accepted in the console before testers |

The fixes weren't re-reviewed beyond the lead's own reading. CI is the first run of every check: the local permission
system refused `terraform test`, the script tests, actionlint and promtool for every agent.
