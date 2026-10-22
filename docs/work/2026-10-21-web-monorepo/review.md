# Reviews — ryft-web → `plattform/web` (2026-10-21)

Findings by reviewer and what happened to them. Blockers go back to the owner and are re-verified.

## Before building

| Reviewer | Finding | Disposition |
| --- | --- | --- |
| security-privacy-reviewer | gitleaks over this repo's history: 24 findings, none real (test fixtures, placeholders, RabbitMQ routing keys); `gitleaks dir` also flags `infra/vault/variables.tf`'s validation regexes | `.gitleaks.toml` scoped per rule + path + exact value, verified with real fakes in the same files (still caught); committed in C |
| platform-architect | design.md: rollback by revert is re-promoted by the next push (services today, web worse); per-job required checks can all be "skipped" if `changes` fails; push-filtering answers the wrong question; title rule too narrow; `SUPPORT_EMAIL` unset ships silently; F1 (a ConfigMap-only change of `PRIVACY_POLICY_VERSION` rolls no pod) | all accepted into C (hold, `ci-ok` gate, filters on PRs only, push on main only, generic title + brand on commits, guard); F1 → own ticket (STATUS) |

## A — import (#5 → #7) and E — brand cleanup (#6)

| Finding | Disposition |
| --- | --- |
| should-fix: #6's body claimed closing its deploy PR avoids the pod roll; promote compares with the running pin, so the next push re-opens it; #5's "no promote" holds only if it merges first | #6 body corrected; #7 says "merge before #6" |
| nit: imported subjects "(#8)/(#9)/(#10)" (and "ryft-app #7") would link to this repo's PRs | import redone with `--replace-message` → #7 (`e8e6c7c`, same tree `258ace1`), #5 closed |
| nit: provenance only in the PR body | in the merge commit's body and `gh pr merge --body` |
| nits on #6 wording (README stutter, "in development", D5 reason, D58 repetition) | `d202613` |
| info: `../ROADMAP.md` has 10 more hard hits + the M3 name | user's file outside the repo: listed in STATUS |

## B — web paths, hooks, make targets

| Reviewer | Finding | Disposition |
| --- | --- | --- |
| code | should-fix: `--staged` scans the whole repo in a linked worktree (`GIT_DIR` exported to hooks) | back to web-engineer |
| code + security | should-fix: `--message` scans git's comment block / `commit -v` diff → false positives on platform commits | back to web-engineer |
| security | should-fix: `commit-msg` fails open without `node` (GUI commits); a message is permanent | probe + fail closed — web-engineer |
| security | should-fix: an old consented privacy version can be renamed away (strict passes, `/privacy/<old>` 404s) | committed version list, en+de each — web-engineer; append-only against the base branch in C |
| security | should-fix: `web/CLAUDE.md` reads as if root rules stop at `web/`; the web agent may bump the policy version | reworded; "new version only on the user's decision" — web-engineer |
| security | should-fix: nothing in CI enforces B until C merges | merge C right after B (order in plan) |
| both | nits: type changes skipped, no `pre-merge-commit`, shell layer untested, `RYFT_PLATFORM_DIR` unused, non-date privacy files, Makefile `-j` order, Playwright install, stale STATUS/REQUIRE_SIBLINGS text, dead legal test, `.dockerignore`, floating syntax line, untrusted-checkout hooks note | all taken — web-engineer |
| both | info: docker-compose and the Go default still pin the version; the deploy window between ConfigMap and page; `PRIVACY_POLICY_URL` could be versioned | compose compared by the check (B); Go default + window → F1 / OPERATIONS (D); URL → user decision (STATUS) |

## C — CI, image, promote

| Reviewer | Finding | Disposition |
| --- | --- | --- |
| lead | `promote.sh` force-pushed its own `deploy/<sha>` on re-runs | replaced: reuse an open PR, reuse an identical orphan branch, else fail (`11cba52`) |
| code | **blocker:** `web-image` greps `TODO(user)` on `/support`, which always has it → web never builds → nothing ever deploys; `mailto:` there is hard-coded | check `/beta` for the configured address — gitops-engineer |
| code | should-fix: `git log | head -100` under pipefail kills promote past ~100 commits | `git log -n 100` + 200-commit test |
| code | should-fix: `ci-ok` with `!cancelled()` is *skipped* on a cancelled run (= passing) | `always()` |
| code | should-fix: `manifests` filter misses the topology code that `k8s-validate` checks | filter extended |
| code | should-fix: gitleaks `git` mode scans `--all` refs | `--log-opts=HEAD` |
| code | should-fix: an open deploy PR's pins are not compared (a `force=web` run is lost) | fast-forward commit on that branch |
| security | should-fix: deploy PRs found by branch name only (fork or human `deploy/*` PRs closed/merged) | filter: same repo, author the App, `^deploy/[0-9a-f]{7}$`, `--limit 200` |
| security + sre + code | should-fix: a mistyped hold is silently ignored | dated format, fail closed, checked in `manifests`, annotate hold changes, fail a PR that moves a held group's `newTag` |
| security | should-fix: the site build reads a GHA cache other main jobs can write; `npx --yes @lhci/cli` resolved fresh | no cache in `web-image`; lhci pinned in B |
| sre | should-fix: a re-run of an old run reads that commit's overlay and can close a newer deploy PR | pins/holds from `origin/main`, never close newer deploy PRs |
| sre | should-fix: no external signal for a healthy-but-wrong site; no stale-hold/stuck-PR alert; red `web-image` blocks a services hotfix; web rollback behind migrate | verification block + hold escape hatch + migrate note in OPERATIONS (D); probe, daily check, hold helper, web PDB → STATUS follow-ups |
| security | nits: title newlines/echo, merge-commit messages, base-branch denylist, CODEOWNERS, orphan-branch tree check, `permissions: {}`, `persist-credentials`, no token for Trivy | taken |
| code | nits: rev-list fail-open, revert titles, closed deploy PRs reopened, docs (pin usage, hotfix escape) | taken (docs in D) |
| security | nits: App key in a `deploy` environment; Dependabot for `docker run`/service images | STATUS follow-ups (needs a settings change) |

### C, second round and the first real runs

| Source | Finding | Disposition |
| --- | --- | --- |
| PR #9's first run | **blocker:** `manifests` couldn't install actionlint (needs Go ≥ 1.25, the job had 1.24 with `GOTOOLCHAIN=local`) | `GOTOOLCHAIN=auto` for that step |
| PR #9's first run | **blocker:** `check_pr_test` inherited the runner's `GITHUB_BASE_REF` | `check_pr.sh` keys off an explicit `BRAND_BASE` from `pr.yml` |
| PR #9's second run | `smoke`: the runner's Node 22 can't compile the denylist's regex modifier groups | `setup-node` with `web/.nvmrc` in `smoke` |
| code | should-fix: the close-on-hold path never ran (promote required a target) | promote runs on `services`, `web` or `held` |
| code | should-fix: the held-pin rule blocked a second rollback by hand | only on the App's `deploy/<sha>` branches |
| security | low: `digest:`/`newName:` could change what runs unseen; a trailing `# hold` was ignored; a PR's `.gitleaksignore`/`.gitleaks.toml` counted | image entries must be name + quoted 7-hex tag; any stray hold fails; base branch's gitleaks config and ignore file on PRs (proven: a PR's own fingerprint no longer silences its secret) |
| code | nits: remedy text, fast-forward union, `gh` failures inside listings | taken |
| code / security re-verify | all fixed, no regressions; promote harmless when nothing is targeted; `--check` passes on main's overlay | — |
| main run `bfc51b8` | `web-image` 403 `write_package` at the push (the package's Actions access was Read) — the predicted failure mode: nothing pushed, promote skipped, site unchanged | role set to Write by the user, failed jobs re-run: green, #18 |
