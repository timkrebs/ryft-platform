# D3: remove Kubernetes, Argo CD, Vault, admission, AWS (2026-11-19)

Brief: head-architect, 2026-11-19 (inline). Branch `remove/eks-era` from `origin/main` (`f05cb09`). One PR,
"remove: Kubernetes, Argo CD, Vault, admission, AWS (D3)". The decision is **D246** (D244 is #219's, still open).

## State
- The user deleted RYFT's AWS rest by hand on 2026-11-19 (zone `Z0847310CSDZ20OKMOUQ`, the snapshot bucket, the
  budget, aliases; keys `2960364e…` and `90b5cce6…` PendingDeletion until 2026-11-26, `fb345db3…` until 2026-12-14).
  The old HCP org `tim-krebs-org` is deleted with every old state. D1 (#222) merged; D2 (#223) obsolete.
- Nothing runs on EKS. Production is Cloud Run (`deploy/run`, `deploy.yml`), the site Firebase Hosting.

## Done before the agents (lead, glue)
- Deleted: `infra/vault` (rm -rf; see the risk below), `infra/aws` (tracked files only), `deploy/k8s`,
  `deploy/argocd`, `deploy/rabbitmq`, `deploy/monitoring`, `deploy/grafana`, `docs/REBUILD.md`.
- `.gitignore`: generic rules for `secrets.yaml`, `secrets/`, `terraform.tfvars`, `*.tfstate*`, `.terraform/`, so a
  clone's ignored leftovers in the removed folders stay out of `git add -A`.

## Tickets (disjoint files, all in this run, foreground)
| # | Owner | Files | Acceptance |
|---|---|---|---|
| T1 | gitops-engineer | `.github/**`, `Makefile`, `scripts/**`, `docker-compose.yml`, `deploy/run/config.env` (comments), `.gitleaks.toml` | No workflow, Makefile target or script reads a deleted path; CI jobs `monitoring`, `argocd-luatest`, the EKS terraform matrix roots, their `changes` paths gone; `ci-ok`'s needs match; `check_workflows.sh` and its test agree; `make test-scripts` tests for deleted scripts gone; `TF_ROOTS`/`tf-migrate-hcp`, `k8s-*`, `alerts-*`, `web-image`/`web-serve*`, `monitoring-apply`, `argocd-luatest` targets gone; kept scripts listed with reasons |
| T2 | web-engineer | `web/**` | `web/Dockerfile`, `web/nginx/`, `scripts/container.mjs` and its npm scripts gone; the policy check reads `deploy/run/config.env` (+ compose) only; web docs don't name deleted paths; `make web-test web-build` pass or the CI run is the test |
| T3 | backend-engineer | `backend/**` | `TestCommittedAdminWebOrigins` reads `deploy/run/api.yaml`; no message/comment names a deleted path; `go vet ./... && go test ./...` green; no file added or removed (pkgmap) |
| T4 | sre | `docs/OPERATIONS.md`, `docs/OBSERVABILITY.md` | EKS-era runbooks out; GCP 12–14 → "done by hand on 2026-11-19" with the IDs, 14.3 kept as the last step; any removed runbook without a GCP equivalent listed as a gap |
| T5 | platform-architect | `docs/ARCHITECTURE.md`, `README.md`, `CLAUDE.md`, `docs/SUPABASE.md`, `docs/MESSAGING_DESIGN.md`, `docs/AGENT_FLEET.md` | GCP only; the generated package map block unchanged; README links resolve |
| T6 | lead | `infra/gcp/**` (comments, the D3 cross-check in `tests/dns.tftest.hcl`), `docs/API.md` (Base URLs sentence), `docs/STATUS.md`, `docs/DECISIONS.md` (D246) | `terraform -chdir=infra/gcp test` green; `make check-api-doc` |

Then **code-reviewer** (one): nothing outside the removed areas breaks (CI, Makefile, scripts, doc links, compose).

## History stays
`docs/DECISIONS.md` entries before D246 and `docs/work/**` keep their mentions of the removed paths. The grep
check excludes them.

## Risks
- **`infra/vault` was removed with `rm -rf` before its git-ignored files were checked.** If the clone held
  `infra/vault/terraform.tfvars`, `.terraform/` or a local state there, they are gone from this working tree. The
  siblings in `infra/aws` held `terraform.tfvars` and `.terraform/` but no state. Reported to the user.
- `.gitleaks.toml` allowlists for removed paths stay: the `secrets` job scans the whole history (`--log-opts=HEAD`)
  with main's config, and old commits still hold those files.
- #219 (open) also appends to DECISIONS/STATUS: whichever merges second needs a main merge.

## Rollback
Revert the squash commit; nothing deployed depends on the removed code (no workflow on main reads it after T1).

## Needs the user
- 14.3: the GitHub secrets/variables no workflow reads (guarded paste in the PR body).
- The ignored leftovers in `infra/aws` (two `terraform.tfvars`, `aws.tfplan`, two `.terraform/`): delete by hand.
- Close #223 if `gh pr close` is refused here.
