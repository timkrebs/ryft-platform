# ryft-web → `plattform/web` — plan (2026-10-21)

Brief: `CLAUDE_FOLLOWUP_WEB_MONOREPO.md`. Lead: platform-lead. Decision record: D107 (platform-architect).

## State at the start (checked 2026-10-21 ~20:50 local)

| Precondition | State |
| --- | --- |
| Quick switch (Deployment runs `ghcr.io/timkrebs/ryft/web`) | merged as #3 (`2cc55ef`), docs #4; every URL of the brief 200, AASA `application/json` without redirect, `www` → apex. Pod image not read (kubectl login expired). |
| Rollback point | web `f437747` (a ryft-web SHA), services `0fd4256` |
| ryft-web open PRs | #8 docs/go-live (docs, green), #10 legal/answers (legal text, green), #11 ci/promote (obsolete after §3; its one other change, `include-hidden-files: true` for the Lighthouse upload, is carried into the new workflow), #1–#5 Dependabot (obsolete: re-raised under `/web` by this repo's Dependabot) |
| ryft-web local clone | clean, on `docs/go-live`; local `main` is pre-history-rewrite (same subjects, other SHAs) — irrelevant, the import uses a fresh clone of `origin/main`. Local branches `chore/app-drift`, `fix/access-log`, `fix/font-fallback-cls` are merged into `origin/main` under other SHAs. |
| ryft-platform settings | merge commits **allowed** (no settings change for PR A); required checks `test`, `lint`, `manifests`, `smoke`; variables: `RYFT_DEPLOY_APP_CLIENT_ID`; secret `RYFT_DEPLOY_APP_KEY`; no Dependabot, no PR-title check, no gitleaks |
| ryft-web settings | variable `SUPPORT_EMAIL=support@ryftapp.de`; no `TESTFLIGHT_URL`; no `RYFT_DEPLOY_APP_*` (nothing to remove) |
| gitleaks over the platform's history | **24 findings** (test fixtures, examples, dev broker definitions) → the repo-wide job needs a reviewed allowlist |
| GHCR `ryft/web` | created by ryft-web's workflow, so linked to ryft-web: this repo's `GITHUB_TOKEN` cannot push until the package grants ryft-platform write access (UI only) |
| `../ROADMAP.md`, `../README.md` | no mention of the site or ryft-web: nothing to change |

## Tickets

### A — `web: import ryft-web with history` (lead; merge commit)
Fresh clone → `git filter-repo --to-subdirectory-filter web` → `git merge --allow-unrelated-histories --no-ff`.
- AC: `git log --follow web/package.json` shows ryft-web's history; only tracked files; `git check-ignore`
  covers `web/node_modules`, `web/out`, `web/.next`, `web/test-results`, `web/src/fonts/*.woff2`; backend CI green.
- AC: merged with **Create a merge commit** (allowed already); auto-merge off.

### B — `web: paths, hooks and make targets in the monorepo` (web-engineer)
Files: `web/**` except `web/.github/**`; root `.githooks/`, `Makefile` (web-* + hooks), `CLAUDE.md` (web section).
- Paths: `../app` → `../../app` (`RYFT_APP_DIR` override kept), `../plattform` → `..` (`RYFT_PLATFORM_DIR` kept);
  scripts, package.json, tests, docs. Policy check reads this repo's ConfigMaps directly.
- Hooks: no `prepare` → `core.hooksPath`; root `.githooks/pre-commit` runs the brand check on staged `web/**` only,
  `.githooks/commit-msg` on every message; `make hooks` installs; `check-brand.mjs --staged` scoped to `web/`.
- `web/CLAUDE.md` without "write only in web/" / "never edit ../plattform"; Dockerfile `image.source` → ryft-platform.
- `make web-install web-test web-build web-image web-serve` (+ `web-e2e`, `hooks`).
- AC: from a fresh clone `make web-install web-test web-build` pass with `../app` next to `plattform/`; `npm run e2e`
  green; a staged backend file with a denylisted word is **not** blocked by pre-commit, a staged `web/` file is; a
  commit message with it is blocked anywhere; `git grep -nE '\.\./plattform|ryft-web' web/` only historical mentions.

### C — `ci: web checks and image in this repository, one promote` (gitops-engineer)
Files: `.github/**`, `web/.github/**` (deleted), `.gitleaks.toml`, `scripts/promote.sh`, `scripts/pin_images.sh`,
`scripts/tests/*`, `deploy/k8s/**` (comments only).
- One `ci.yml`, a `changes` job (`dorny/paths-filter`, SHA-pinned), job-level `if:`; all of ryft-web's checks
  (typecheck, ESLint, Prettier, brand, tokens drift, licences, messages parity, Vitest, build, output tests,
  E2E/axe/links/Lighthouse on the nginx image, Trivy), gitleaks repo-wide, PR-title check repo-wide (+ brand on
  title and the PR's commit messages), `check:policy --strict` whenever web or the api/ingest ConfigMaps change.
- Image `ghcr.io/timkrebs/ryft/web:<this repo's short sha>`, multi-arch, provenance + SBOM, **Trivy before push**,
  no `latest`; built on `main` only when `web/` differs from the running web pin.
- Promote: one "deploy: images <sha>" PR pins every group that differs from its running pin (services when
  `backend/` differs, web when `web/` differs or its pin is not in this history); the `deploy/web-*` exception goes.
- AC (scripts/tests): web-only push → only web moves; both → all six; web push A unmerged, then backend push B →
  B's PR pins services **and** web to B and closes A's (no lost web change); docs/manifests-only → no images, no PR;
  the merged pin commit promotes nothing; running web pin `f437747` (not in history) → web pinned.
- AC (CI): on this PR every job runs and is green; a backend-only and a web-only test PR show the other side's
  required checks as skipped and mergeable; actionlint clean.

### D — `docs: web lives in plattform/web` (lead; D107 from platform-architect)
OPERATIONS (Apex site, Deploy, "A new policy text"), ARCHITECTURE (layout), STATUS (move, SHAs, app-session list),
DECISIONS D107 (monorepo, filter-repo, ryft-web archived; supersedes D106's "Who pins web"), this plan + review.

### E — brand hits outside `web/` (lead → user decision)
List the internal hits in this repo (README.md:3 first), propose wording, ask; a separate `docs:` PR if wanted.

## Order
A (needs the user's call on ryft-web #8/#10) → B and C built in parallel on top of A (B in the main checkout,
C in a worktree) → push B after A merges → push C after B merges → user settings → merge C → first web image +
promote PR → Argo CD → live check → D, branch protection, archive ryft-web.

## Verify
code-reviewer on B, C, D; security-privacy-reviewer on C (Actions, tokens, gitleaks allowlist, supply chain) and the
hooks in B; sre on C (rollout, promote, rollback). Findings → `review.md`.

## Needs the user (exact commands in STATUS at the end)
1. ryft-web #8/#10: merge there first or port (asked).
2. Before merging C: `gh variable set SUPPORT_EMAIL -R timkrebs/ryft-platform --body support@ryftapp.de`; GHCR
   package `ryft/web` → Settings → Manage Actions access → add `ryft-platform`, role Write.
3. After C's first green run: add the new required checks to `main`'s protection.
4. After live check: last README commit in ryft-web, `gh repo archive timkrebs/ryft-web`; `mv ../web ../web.old`.

## Risks and rollback
- Squash of A loses the history → merged by `gh pr merge --merge`, body says so. Undo: revert the merge commit.
- First platform-built web image differs from `f437747` (Node/Next same lockfile; legal answers of #10 if merged)
  → the promote PR lists it; rollback = revert the "deploy: images" merge → `f437747` (stays in GHCR).
- GHCR write access missing → web-image job 403, no promote; fix the package setting, re-run the job.
- A required check that never reports (matrix skipped by `if:`) → keep matrix jobs out of the required set.
