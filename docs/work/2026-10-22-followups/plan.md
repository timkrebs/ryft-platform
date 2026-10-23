# Follow-ups of the web move — plan (2026-10-22)

Brief: the user's "please work on the open points" (STATUS → "Web in this repository: open"). Lead: platform-lead.
User answers (2026-10-22): rollback drill **yes, on the services, after the other fixes**; the external check through
**Grafana Synthetic Monitoring**; `../ROADMAP.md`: **only the 10 brand lines** (milestone names stay).

## State at the start
- `main` = `b306a96`, clean (untracked `.claude/`, `docs/AGENT_FLEET.md` pre-existing). Argo CD: every app Synced/Healthy.
- Pins: services `a9685b5` (backend/ unchanged since; `0fd4256` differs only by two comment lines, all five images
  of both tags in GHCR), web `bfc51b8`. Web runs 2 replicas, no PDB.
- Dependabot: #12 nginx 1.31.7 (mainline; we run stable 1.30), #13 node 26 (Docker), #14 eslint 10, #15 @types/node 26,
  #16 typescript 7 (**web-checks red**), #17 actions group (8, all checks green; main-only jobs untested by the PR run).
- Disk: 12 GB free.

## Tickets

### A — web toolchain (web-engineer, worktree) → PR `deps: …` (title per the verdicts)
Verdict per #12–#16 with the reason (changelog, LTS dates, Next.js support, why #16 fails). Bundle what we take into one
PR (Docker base, `.nvmrc`, `engines`, `@types/node` move together), close the Dependabot PRs it replaces, and add
`ignore` rules in `.github/dependabot.yml` (`/web` entries only) for what we deliberately skip.
- AC: `make web-install web-test web-build web-e2e` green locally; CI green; the built `out/` differs from main's only
  where the toolchain explains it (report the diff summary); no `web/` file outside the toolchain changes.

### B — CI: deploy environment, daily deploy watch, hold.sh (gitops-engineer, worktree) → PR `ci: …`
- `promote` runs in environment `deploy` (secret `RYFT_DEPLOY_APP_KEY` there, branch policy `main`).
- `.github/workflows/deploy-watch.yml`, daily + `workflow_dispatch`, read-only token: fails on a hold older than N days,
  an App deploy PR open longer than N hours, or a group targeted (differs from its pin, not held) on a `main` tip older
  than N hours with no open deploy PR. Logic in a script with tests in `scripts/tests/`.
- `scripts/hold.sh add|rm <group> "<reason>"` (writes the exact format `deploy_targets.sh --check` accepts; tests).
- OPERATIONS → Deploy and roll back: hold.sh, the watch, the environment. #17 reviewed first (verdict, not merged).
- AC: actionlint clean; script tests green; a hold dated 8 days ago fails the watch, today's passes; the promote job's
  only change is `environment: deploy`.

### C — F1 and web PDB (platform-architect → gitops-engineer + backend-engineer) → PR `deploy: …`
Architect picks how api/ingest roll when `PRIVACY_POLICY_VERSION` changes (pod-template annotation via kustomize
`replacements`, hashed `configMapGenerator`, or Deployment env) with the failure modes (Argo prune, rollback, the policy
check's reader). Go default in `internal/config`: fail when unset outside local mode. Web PDB `maxUnavailable: 1`.
- AC: `kustomize build overlays/eks` shows the change; a test proves a version change alters both pod templates;
  `npm run check:policy -- --strict` still reads the version; `go test ./...`; `make k8s-validate`; OPERATIONS → "A new
  policy text" loses the interim restart.

### D — Synthetic Monitoring for ryftapp.de (sre, worktree) → PR `monitoring: …`
Checks as code (the alerts-apply pattern: definitions file + `make synthetics-apply` with a token), `/`, `/privacy`, the
AASA (200, `application/json`, no redirect, the appID), `www` → apex; alert rules in `deploy/grafana/alerts/ryft.yaml`
with tests; OBSERVABILITY and OPERATIONS entries; the user's one-time setup as exact steps.
- AC: `make alerts-check` green; the definitions validate (dry run); free-tier execution budget stated.

### E — `../ROADMAP.md` (lead)
Reword lines 45, 159, 161, 191, 281, 292, 319, 371, 410, 469 (masked scanner; backup first, the file has no git).
- AC: the scanner reports no `hard` hit; milestone names unchanged.

### F — rollback drill (sre checklist, lead runs, user approves each merge) — after A–D are merged
Services from the then-current pin back to the previous one with a `# hold:`, verify (pods' images, migrate/smoke hooks,
`/v1/me`), release the hold, verify the new pin. Answer from Argo CD's docs (not in production) whether a selective
`argocd app sync --resource` skips hooks.

### Added during the work
- **#24** `ci: allow the start-up tests' sentinel API key in gitleaks` (lead): C's new start-up tests hold the test
  sentinel as `API_KEY=…`; PRs are scanned with main's config over their history, so a scoped entry had to land
  first. Merged `36992dd`.
- **H** (gitops-engineer, after C): the security re-review of B found that hidden line breaks (U+2028/2029/0085, CR)
  still work in every other YAML file kustomize and GitHub read (a hidden sidecar in `overlays/eks/events.yaml` passed
  every check) → a byte check over tracked YAML in `deploy/`, `.github/`, `infra/vault/tests/golden/`;
  `check_registry.sh` fails on any rendered image outside the pins; `check_workflows.sh` parsed (yq), no external
  reusable workflows or `secrets: inherit`; hold-text rules in `validate_holds`; `--check` as an allowlist; B's nits.

### G — close (lead) → PR `docs: …`
STATUS (cutover steps 7–10 done, Argo checked, these items), DECISIONS (one entry per judgement call, numbered by the
lead), the drill's record, this plan and `review.md`.

## Order
Parallel: architect (C design), A, B (after its #17 verdict), D, E. Then C's build. Merges one at a time with the
user's approval: #17 → B (after the user's environment steps) → A → C → D (after the user's Grafana steps) → F → G.
Branches merge `main` in, never rebase/force-push. DECISIONS/STATUS only in G (implementers return draft entries).

## Verify
code-reviewer on A–D; security-privacy-reviewer on #17, B (token, environment), C (consent version) and D (probe data);
sre on B and C (rollout). Findings → `review.md`.

## Needs the user
1. Before B merges: create environment `deploy` (branch policy `main`), set `RYFT_DEPLOY_APP_KEY` in it; after B's
   first green promote, delete the repository-level secret. Exact commands in B's PR body.
2. Before D is useful: switch on Synthetic Monitoring in Grafana Cloud, create the token, run `make synthetics-apply`
   and `make alerts-apply`.
3. Approve each merge.

## Risks and rollback
- A changes the web image: rollback = revert the deploy PR + `# hold: web` (OPERATIONS).
- B: an environment without the secret blocks the next promote (no outage): set the secret, re-run the job.
- C rolls api/ingest once on merge; an `internal/config` fail-fast without the env crash-loops the new ReplicaSet only
  (the old one stays, Argo shows Degraded): revert.
- The drill restarts every service pod twice; migrate is a no-op (same migrations).
