# Follow-ups of the web move — review (2026-10-22)

Findings per ticket and their disposition. Ticket letters as in `plan.md`.

## A — web toolchain (#19)
code-reviewer: no blocker; ESLint 10 sound (0 problems on the same 95 files; probe rules report; callers pass no
removed flags). Should-fix: (1) the Docker `node` ignore `versions: ['25.x', …]` does nothing (the docker ecosystem
uses Gem::Requirement without `.x` conversion) → `update-types: semver-major` for `node` and `@types/node`; (2) the
policy pointer needs the D-entry (lead, in G) and STATUS. Nits: (3) unrelated `napi-postinstall` 0.3.5 → 0.3.7 in the
lockfile, published 7 h earlier, runs at install → restore 0.3.5; (4) ESLint 10 support for eslint-plugin-react/-import
relies on eslint-config-next's `fixupPluginRules` shim (record in the D-entry); (5) Docker patches arrive only while the
`-alpine3.24` suffix stays. Also from #17's security review: Dependabot `cooldown` 7 days. → back to web-engineer.
Fixed in `0870df9` (semver-major rules for `node`/`@types/node`, `napi-postinstall` 0.3.5 restored byte-for-byte,
cooldown 7 days — GitHub's reference confirms cooldown applies to version updates only — and the Alpine-suffix note).
Merged as `332c252` (user-approved). Main's run: every job green, `web-image` (the first build with #17's action
versions: buildx 4, build-push 7, metadata 6, login 4, qemu 4) and `promote` (checkout 7 with the App token) succeeded;
deploy PR #23. Dependabot closed #12, #13, #15, #16 under the new rules; #14 closed as superseded.
#23 merged itself at 09:42:21 UTC (`9bc81cb`, only the overlay); web pods on `332c252` 2/2 at 09:44:33; Argo
Synced/Healthy/Succeeded at `9bc81cb`, PreSync migrate and PostSync smoke Succeeded; live: the eight pages 200
`text/html`, AASA 200 `application/json` without redirect with the app ID, `www` 301 → apex, `api /v1/me` 401.

## #17 — the actions group (security-privacy-reviewer)
Verdict **merge, then watch the first main run that builds** (`images`, `web-image`, `promote` were skipped on the PR).
All 8 new SHAs are the upstream release commits (annotated tags dereferenced), signed, in the default branch's
history; no transfer, no advisory; `action.yml` diffs read. Relevant: checkout v6+ keeps the persisted token in a
`$RUNNER_TEMP` file included from `.git/config` (promote's App token; `promote.sh` runs git from the checkout root, so
the include matches); build-push 7.4.0 fixes a workflow-command injection through unescaped metadata; login-action's
GHCR path unchanged; metadata-action's short-SHA tag unchanged. Merging #17 alone builds nothing (no `backend/`/`web/`
change); the first exercise is A's merge (`web-image`, `promote`), the services' `images` job the next backend change.

Pre-existing, not caused by #17 (→ STATUS open items):
- medium: `tonistiigi/binfmt:latest` (privileged) and `moby/buildkit:buildx-stable-1` are not pinned by digest in the
  two image jobs (ci.yml ~479, ~524).
- medium: the services' `images` job pushes without a Trivy scan (only `web-image` scans before push).
- low: public provenance (`mode=max`) carries the push event payload (commit messages, noreply addresses only today);
  `provenance-add-gha=false` would drop it.
- low: no Dependabot `cooldown` for github-actions.

gitops-engineer's own review of #17: merge as is (same SHA verification; majors raise only the runner minimum).
Merged 2026-10-22 as `bdbcd11` (user-approved).

## B — deploy environment, watch, hold.sh (#21)
sre: no blocker; tests, shellcheck, actionlint clean; every Verify command in the new runbook works against the cluster
as written; `deploy_watch.sh` live → OK. Should-fix: (1) "a rollback runs the OLD release's hooks" holds for a services
rollback only; (2) runbook step 2 still says "close it to stop the deploy" — a closed deploy PR makes the watch fail
daily → "close it and `hold.sh add` the group"; (3) no preconditions/abort criteria in the runbook (they live in the
drill), and say web has no PDB yet. Nits: warn on a `deploy/<7hex>` PR by another author (renamed App); daily cadence
(fine for now); email-only alert path is enough (no dead-man; check the watch's runs weekly); the watch table's
"environment without its secret" falls back, it doesn't skip; the undo can deploy a new SHA if `backend/` moved;
"the rollback merge's run" is the first proof; RS listing via custom-columns; hold.sh trailing whitespace.
drill.md updated by the lead (hold.sh, `fix/rollback-services`, the watch in 1.1 and 3).

security-privacy-reviewer: no blocker; the `main`-only environment keeps the key from PR, fork, branch and tag runs
once the repository copy is gone. Should-fix: (1) **fix before merge**: Unicode line breaks (U+2028/2029/0085, and a CR
in any overlay comment) pass `hold.sh`'s `[[:cntrl:]]` guard and `deploy_targets.sh --check`, and go-yaml treats them
as new lines — reproduced: a hold reason injected an `images:` entry and `kubectl kustomize` rendered an attacker's
image (also passes `check_registry.sh`, which accepts any `ghcr.io/`) → printable ASCII only in hold.sh, the overlay
ASCII-only in `validate_holds`, ryft images only from `ghcr.io/timkrebs/ryft/`, tests; (2) the repository-level secret
is readable by any same-repo run until deleted, and once the environment has the secret it is never read anyway →
rotate (new key into `deploy`, delete the repo secret at once, delete the old key in the App); (3) STATUS:391 still
documents the repository-level `gh secret set` and a zsh glob that can concatenate two keys (lead, G); (4) .pem file
name, delete the file, one key in the App. Nits: a check that only `promote` names `environment: deploy` and no
workflow uses `pull_request_target`/`workflow_run`; `can_admins_bypass: false` and fuller verify commands; hold reasons
are public and land in the deploy PR body (mentions, closing keywords) → refuse `@` and closing keywords, document;
`iso_epoch` exit inside a substitution.

code-reviewer: no blocker. Should-fix: (1) a hold dated in the future never fires; (2) `exit 2` inside `$(…)` plus an
arithmetic error silently ends the watch as OK/exit 0 (reproduced: fractional `createdAt`, `DEPLOY_PR_MAX_HOURS=08`);
(3) five hand-edited `newTag`s: a missed one goes unnoticed (held groups skip the "pins disagree" branch) → use
`pin_images.sh <old> services`, `hold.sh add services` refuses disagreeing pins; (4) the documented `.pem` path doesn't
exist (GitHub dates the file name). Nits: tests leak into the real job summary; deploy-watch's checkout pin is v5.1.0
after #17; "a missing pin" wording; shallow clone; committer date as push time; `hold.sh` space rule vs `hold_ok`;
CRLF message; duplicated deploy-PR filter; promote.sh's PR body and the overlay comment still describe a hand-written
hold (→ the F1 PR, which owns those files); step 4 services-only; hour rounding.
→ all three reviews' items back to gitops-engineer in one list (key rotation instead of moving it).
Fixed in `c1ae754` (+ `89aaf53` file modes) after merging main: hold reasons printable ASCII only, no `@` or closing
keywords; `validate_holds` fails closed on any non-ASCII byte in the overlay (the LS injection now exits 2);
`check_registry.sh` only `ghcr.io/timkrebs/ryft/` and rendered tags equal the line-parsed pins; future-dated holds,
unreadable timestamps and `08` settings handled; rollback via `pin_images.sh`, `hold.sh add services` refuses
disagreeing tags; the environment section as a key rotation (repo secret deleted right after the merge: until then the
old promote still reads it); `scripts/check_workflows.sh` in `lint` (only promote names `environment: deploy`; no
`pull_request_target`/`workflow_run`); the nits. Text for promote.sh's PR body and the overlay comment → #22.

security re-verify (89aaf53): the must-fix is closed — 14 hidden-break variants and 10 visible-YAML variants against
the overlay are caught by `--check` and/or `check_registry.sh` (both in the required `manifests` job; confirmed in run
39757836152). Rotation runbook done; the order (delete the repo secret right after the merge) is acceptable. Merge
condition: the user creates the environment and sets the new key **before** the merge (otherwise GitHub auto-creates
`deploy` without a branch policy) and revokes the old key in the same sitting; merge by hand. New: **N1 (high,
pre-existing):** the same hidden-line-break trick works in every other YAML kustomize/GitHub reads (a sidecar with
`envFrom: ryft-secrets` hidden in a comment of `overlays/eks/events.yaml` passes every check) → follow-up PR H: a byte
check over tracked YAML in `deploy/`, `.github/`, `infra/vault/tests/golden/` (LS/PS/NEL/CR/C0/DEL/bidi/BOM/invalid
UTF-8; zero hits today) and `check_registry` failing on any rendered image outside the pinned refs. N2 `check_workflows`
bypassable by YAML shapes (→ yq, forbid external reusable workflows and `secrets: inherit`); N3 the hold-text rules
only in hold.sh (→ `validate_holds`); N4 `--check` is a denylist (→ allowlist).

code re-verify (89aaf53): every item done; no blocker. Trial merges of #21 + #22 (also on today's main) are clean
apart from the overlay's web pin (#23), and green (12 scripts tests, k8s-validate, actionlint, check_workflows).
Cross-PR: a web rollback after both merge would set the in-force version from HEAD (= #22's S1, being fixed there);
#22 must merge after #21 (hold.sh); #21's "web has no PDB yet" sentence → fix in #22. Nits for H: one name for the
render test hook (and forbid the hooks in workflows, or fake binaries on PATH); `check_workflows` could live in
`manifests`; it is a tripwire (quoted keys, `secrets: inherit`); `can_admins_bypass` is not in GitHub's REST schema →
confirm "admin bypass off" in the UI; the watch table's missing-key row; `web:<old>` in "Before you start"; closing
keywords with issue URLs in hold.sh.

Cutover (2026-10-22): the lead created the environment (PUT with `can_admins_bypass: false`, POST branch policy
`main`; read back: custom policy, `["main","branch"]`, bypass false); the user generated a new App key and set it in
`deploy` (14:40 UTC); #21 merged as `e9a0139` (user-approved); the repository-level `RYFT_DEPLOY_APP_KEY` deleted right
after (user-approved; only the environment holds it). The user revoked the old key in the App and removed the file
(reported 2026-10-22, not readable by API from here). First promote in the environment: 15:58 UTC (F1's deploy, #25).

## C — F1 and the web PDB (#22)
Integrated by the lead from three branches (gitops `355fa11`, backend `f05c788`, web `601a8bd`) on `fix/config-rolls`;
on the integrated branch: `go vet`, `go test ./...`, `make test-scripts`, `make k8s-validate` (405 valid), the strict web
policy check, `check_policy_in_force.sh`, web Vitest (181) and lint all green.

sre: no blocker. PruneLast in Argo CD v3.5.6 defers the prune until the new ReplicaSets are healthy (also for retries
and self-heal); migrate's plain ConfigMap is outside the rename; with 2 replicas maxUnavailable rounds to 0, the
single-replica scoring/notify surge first; a crash-looping fail-fast api/ingest leaves the old pods serving; no alert
should fire during the two rolls. Should-fix (docs, in "Deploy and roll back", after B merges): which commit to revert
for a config problem vs an image problem (revert C's commit, keep the pin commits); "do not use `kubectl rollout undo`"
where on-call reads it. Nit: scoring runs two consumers for 10–30 s during a surge — confirm its timer is safe or use
`maxSurge: 0` for scoring (→ STATUS open item). Watch-after-merge commands → the PR body.

security-privacy-reviewer: the automatic path is right (promote pins web and the base version of the same commit; the
deploy PR runs the in-force check before auto-merge). **Blocker B1:** `check_policy_in_force.sh` checks that the
in-force version's page *exists* in the pinned web commit, not that it is the page `/privacy` serves (the newest one):
a hand edit (which the runbook asks for after a rollback) can keep an older version in force under a newer text →
compare with the newest page per language. Should-fix: S1 `pin_images.sh <old> web` writes HEAD's base version, not
`<old>`'s; the rollback steps don't name the in-force lines; an admin push skips the check. S2 the check reads a
stand-in: a container `env` entry overrides `envFrom` (bypass reproduced), the namespace regex can be fooled by a data
key, a `PRIVACY_POLICY_VERSION` key in Vault's `ryft-secrets` would win (Terraform validation → user). S3 a `draft`
page now goes into force automatically → fail when the in-force version changes to a draft page (the lead adopts this;
2026-10-20 unchanged stays allowed). S4 a partly failed sync can leave page and version apart → OPERATIONS response.
Nits: `[0-9]` not `\d` in pin_images.sh (Unicode digits); real-date validation; `/privacy` cached 300 s by nginx →
`no-cache`; hold.sh only after B (merge B first); D-entry; OBSERVABILITY's `LOG_LEVEL` sentence; checks only at merge
time. Not caused here: the in-force 2026-10-20 text is still a draft without the controller's name/address and the
supervisory authority (`TODO(user)`); once in force, a text change must be a new version.

code-reviewer: rendered data identical to main's; Jobs/CronJobs next to a generator get the hashed name (none exist
there today); env-file traps fail closed in all three readers; `go test -race`, every scripts test, k8s-validate
green. **Blocker:** the tests hard-code `2026-10-20` / `2026-11-02`, so the first deploy PR that moves the pin turns
`manifests` and `smoke` red and blocks every later promote (reproduced) → read the current values, a never-real date.
Should-fix: setting the pin back is undone by the next web deploy (only a new version or a web hold is durable);
`pin_images.sh <old> web` reads HEAD (= S1); `hold.sh` referenced before #21 exists (merge #21 first; a trial merge
of #21 into this branch is conflict-free and green); calendar-invalid dates pass every check (Go refuses → crash-loop;
the bad date stays in the append-only list); D-entry/STATUS (G); docs still naming the ConfigMaps (OPERATIONS 561/966,
OBSERVABILITY 257, PR template, Self-hosted). Nits: the in-force check isn't in k8s-validate as its header says;
"unchanged at A"; the re-exec tests don't clear `DB_MAX_CONNS`/`USER_ID`; the reader's key prefix.
→ C-G: B1, the tests, S1/S2, the draft guard (S3, adopted by the lead), OPERATIONS; C-W: calendar dates, exact key,
`/privacy` `no-cache`; C-B: the test env. Deploy-and-roll-back text after #21 merges.

Fixes: C-G `e1708ee` (newest-page check, no fixed dates in tests, pin from `<sha>`, `[0-9]` and calendar dates, env
override/namespace/configMapRef checks, the draft guard against `origin/$BASE_REF`, the OPERATIONS paragraphs and doc
spots), C-W `68da281` (calendar dates incl. the versions list, exact key, `/privacy` and `/de/privacy` `no-cache` —
checked by hand on the built image; Playwright's desktop project couldn't run locally), C-B `e48a32b` (explicit child
env). Re-integrated as `1715edb` with main (#20, #23): `go test`, 9 scripts tests, k8s-validate, strict web check,
in-force check (`332c252` serves 2026-10-20), `--check`, Vitest 184, lint all green. Re-verify by security and code.

security re-verify (1715edb): B1, S1, S2(a–c), the nits done; the base-ref wiring reaches the App's deploy PR. No
blocker; fix before merge: (1) the draft guard fails open on CRLF/BOM/quoted-key frontmatter → allow a change only for
`status: published` (normalised), like `Legal.tsx`; (2) it refuses rolling back to a version that was in force before
as a draft (today's 2026-10-20) → allow versions that were in force on base before; (3) `pin_images.sh <pre-#22 sha>
web` fails (no `config.env` there) → derive the version from the newest page at `<sha>`, the rule the check enforces.
Low: an ambiguous short SHA is treated as foreign and passes → allow-list `f437747`, fail on anything else that doesn't
resolve; the guard skips silently when the base ref or literal doesn't parse → err; a draft version on main stalls
every deploy → name `hold.sh add web` in OPERATIONS. The Vault override (`service_secrets` is merged after the
ConfigMap) → STATUS, the user's Terraform.

code re-verify (1715edb): **the test blocker is back in another form** — `promote_test`'s `changed_pins` counts every
added overlay line, so once base's version isn't the first listed one, case 6 counts the two literals too (reproduced
with 2026-12-01 and with the 2026-11-02 policy PR: `make test-scripts` red → smoke → no images) → count `newTag` lines
only (verified). Also: the pre-#22 sha rollback, the draft guard spellings and the withdrawal path (same as security);
the guard should make the person's policy PR red rather than the bot's deploy PR; #21 first; the merged runbook must say
a web rollback moves the version in force; the guard skips silently on an unreadable base. A trial merge of #21 into
1715edb: no conflicts, 13 scripts tests and every check green. → all to C-G in one list; C-G merges #21's branch in and
now owns the F1 text in "Deploy and roll back".

Round two fixed in `abe77b2` (with #21's branch merged in), re-integrated as `767f19d`: all green locally. Final
code re-verify: every claimed fix done (27 frontmatter variants against gray-matter; draft policy PR red in either
language; rollback via `332c252`/`f437747` green; unknown shas refused). **CI blocker the lead had missed:** `secrets`
(gitleaks) was red on all three rounds — the start-up tests' child env holds the sentinel as `API_KEY=0123456789abcdef`,
which `generic-api-key` reports with the prefix; PRs are scanned with main's config over their whole history → a
scoped entry lands on main first (#24; checked with gitleaks 8.30.1: 4 leaks → none). Should-fix: the draft guard
reads only the label, so a web rollback past an in-place publish brings the draft text back → compare with the page at
the base's web pin. Nits: hard-code the foreign pin's version; quote stripping; docs; header; published-form tests.
→ C-G, third round.

Third round `45821b4` (base-ref comparison: a page published at the base's web pin must stay published at the new pin;
no un-publish in place; `FOREIGN_VERSION`; one-pair quote stripping; docs; tests), integrated as `881abc1` with main
(#21, #24): all local checks and gitleaks over the PR's commits green. Re-verify: no blocker; the in-place-publish
rollback and un-publish scenarios fail as they should; normal paths green; the first deploy PR after the merge is not
blocked (332c252's page is a draft). Two nits moved to H: a status continuation after a blank line; fail closed when
the base overlay has a web entry the awk can't read.

Merged as `d6596fe` (15:35:36 UTC, user-approved; GitHub CI green on 881abc1). Rollout, watched read-only:
- 15:38:28 Argo synced `d6596fe` (Synced/Healthy/Succeeded, PreSync migrate and PostSync smoke Succeeded): api, ingest,
  scoring, notify on `a9685b5` with `ryft-config-2ffhbtm6t9` (api, ingest), `-67b5hbhggh` (scoring), `-md7676f8dg`
  (notify); the plain ConfigMaps pruned there, migrate's kept; 0 restarts; `/v1/me` 401.
- Main's run (39806296367): every job green; five service images and web built; `promote` ran in environment
  `deploy` (deployment record `env=deploy sha=d6596fe` 15:58:45) — the rotated key works; deploy PR #25 ("privacy
  policy in force: unchanged at 2026-10-20") merged itself 16:00:59.
- 16:06:35 Argo synced `3230f68`: services and web on `d6596fe`, hooks Succeeded, 0 restarts, `/v1/me` 401; api and
  ingest log `privacy_policy_version 2026-10-20` (2 pods each); web PDB `maxUnavailable 1`, allowed 1.
- Live: pages 200, `/privacy` `no-cache`, `/privacy/2026-10-20` `public, max-age=300`, AASA 200 `application/json`.

## H — YAML bytes, check hardening, hold-proof tests (#31)
The first engineer was cut off by the API limit with the work uncommitted; a second gitops-engineer finished it in the
same worktree: `bd8da0f` (fixtures instead of the live overlay — the drill's defect), `74fcce7` (items 1–8), `b6537e7`
(merge main). code-reviewer: no blocker; the drill scenario (new policy in force, other pins, both groups held) keeps
all 15 tests and k8s-validate green; the allowlist accepts every overlay since 361e34a. Should-fix: S1 the overlay
allowlist accepts remote `resources` (`- github.com/…` passed `--check`; kustomize fetched it unpinned) and `../`
patches → only local paths; S2 the fixture strip misses a tab-indented hold the validator accepts (reproduced: four
tests red) → strip with the validator's shape and assert none is left; S3 `deploy_targets_test` still greps the live
overlay's comment and `smoke` doesn't run on overlay-only PRs → drop or move it; S4 `check_workflows.sh` mixes yq's
stderr into its JSON → separate, pin yq, refuse `<<`. Nits: Helm YAML under `infra/aws/kubernetes/charts` not
byte-checked; messages; SC1007; python3 needed for YAML commits (CLAUDE.md line).

security-privacy-reviewer: N2–N4 done, every earlier reproduction fails now; **N1 not closed**: H1 (high) a file named
`Kustomization` (no extension) with a U+2028-hidden inline patch made api's default-deny NetworkPolicy allow-all and
passed every `manifests` step and the hook; also any extension via `patches`/`resources` and directories outside
`deploy/` → byte-check every tracked file by content (binary allowlist by extension), refuse `Kustomization` /
`kustomization.yml`; M1 `.gitattributes` (working-tree-encoding) is in no filter → filter + none may exist; M2
`deploy/argocd/apps` also applies .yml/.json (pre-existing) → only `*.yaml`; M3 remote and out-of-tree `resources` →
a walker over every kustomization. Lows: refuse by Unicode category (L1), env/PATH knobs in judging scripts (L2),
`check_registry` argument mode accepts non-ghcr refs silently (L3), Helm chart and legal pages outside the byte check
(L4, covered by H1's repo-wide scope). → all back to gitops-engineer with the code review's S1–S4.

Round 2 (`7eac99e`): `check_bytes.sh` over every tracked file, banned names, `check_kustomize_refs.sh`, apps/ only
`*.yaml`, yq pinned, registry argument rule, hold-proof fixtures. Local: all green. CI red: the pinned `yq` needs Go
1.26 under `GOTOOLCHAIN=local`; `web/tests/hooks.test.ts`'s fixture lacks `scripts/check_bytes.sh` (the lead ran only
the scripts tests locally, not the site's) → back to gitops-engineer.
Security re-verify: H1, M1, M2, M3 (lowercase), L1, L3 done. New: (1, high) kustomize reads field names
case-insensitively (`Resources:`, `reſources:`) and decodes `!!binary` — a remote base passed every check;
(2) a symlinked kustomization or `.png`/`envs` file carries a hidden patch; (3) `check_argocd.sh` reads fields by regex
(a second document, `sources:`, `source.kustomize` overrides, an app path outside deploy/); (5) the byte check runs
only on the `manifests` filter; (6) `<<` refused only via a yq warning; (7) whole-file exemptions; (8) default-ignorable
characters. **Lead's decision:** merge #31 once CI is green (it closes the reported classes and the drill's defect —
a hold blocking every image build — and adds no gap that main doesn't already have) and do the allowlist round (exact
key and tag allowlists for kustomizations and Argo apps, no symlinks, binary skips only under web/, the byte check in an
unfiltered job, `!!merge` refused, escapes instead of exemptions) as the next PR at once.

## H2 — allowlists for kustomizations and Argo apps (#38)
gitops-engineer: `5cc3d69`, `9067d11`, `9875e7c`; every v7 reproduction fails now, main's files pass.
code-reviewer: **blocker** — `brand.mjs`'s escaped class breaks Prettier's width, so `web-checks`/`ci-ok` are red.
Should-fix: (2) the key schema and the reference walk are separate lists (adding a path-carrying key such as `files`
passes unchecked; `files` was used by old bases) → path-typed schema fields the walk follows; (3) the new test reads
live `deploy/` files (deleting `events.yaml` would turn main's `smoke` red); (4) a wrong value type ends in a traceback
that hides the findings; (5) the pinned yq only in `manifests`, `smoke` uses the runner's; (6) binaries outside `web/`
and emoji with U+FE0F are refused — document or relax; (7) D-entry details (D115 in #39). Nits: test runtime (91 s in
CI), error hints, OPERATIONS placement and wording, `curl --retry`. Verified: render-identical local-postgres,
`brand.mjs` identical over all code points, notify text byte-identical, the `bytes` job wiring, the yq checksum.
security-privacy-reviewer: findings 1–8 of #31's re-verify done; every earlier reproduction fails a gate. New: **N1
(high, a regression of this PR)** `github.com/<org>/<repo>` passes the segment rule with a committed decoy directory
and kustomize still clones the remote; **N2 (high)** a new app pointing at a path whose kustomization renders a nested
`Application` (project `ryft`, `source.kustomize.images` → a foreign image) passes; N3 double-quoted inline patches
decode `\L`/`\n` (ASCII bytes); N4 reserved default-ignorable ranges; N5 brand.mjs's class. Outside the PR: a visible
`config.env` edit to `AUTH_MODE=apikey` passes every gate → a render-level production-invariants check (pinned images
only, default-deny intact, supabase auth, no argoproj kinds outside apps/). → all back to gitops-engineer as round 2,
including the invariants check.
Round 2 (`46788a3`, `3fecd6e`): CI green (the `bytes` job and the shared yq install worked); GitGuardian flagged
`AUTH_NAMESPACES = ("ryft-api", "ryft-ingest")` as an "Authentication Tuple" — a false positive, not a required
check. Security re-verify: N1 (incl. any-case github.com, `.git`, decoys; `GIT_ALLOW_PROTOCOL=file` proven), N3
(outer document), N4, N5 and the extras done; 68 old variants still refused. The render invariants have bypasses:
(1, high) a path is checked once under the first app's project (a shadow app in `ryft-operators` switches the strict
rules off); (2, high) the auth rule reads names, not the effective env (`envFrom` prefix, `valueFrom`, Secret keys;
`USER_ID`); (3–9) the operators project's power, VSO objects (VaultConnection address, templates), allow-all
NetworkPolicy shapes, workloads without a namespace, images that run any command, privileged pods and PSA labels,
`project.yaml` read from the PR, inline literal patches not decoded; (10, 11) `$` vs fullmatch, fail-open image
listing. **Lead's decision:** #38 gets 1, 2, 10, 11, the empty-rule NetworkPolicy shapes, the GitGuardian rename and
honest wording of what the invariants cover; 3–9 belong in an admission-level policy in the cluster (CI scripts
can't enforce pod security completely) — a separate ticket, platform-architect first.

## D — Synthetic Monitoring (#20)
security-privacy-reviewer: no blocker, no user data, the token never reaches argv/disk/logs (`-K -` on stdin, verified
with a curl argv logger). Should-fix: (1) `SM_API_URL` guard is prefix-only (`http://127.0.0.1@evil/` and lookalike
hosts pass) → allow only `https://synthetic-monitoring-api(-region)?.grafana.net`, the loopback only behind a test
switch, `--proto =https`; (2) curl reads `~/.curlrc` (a `verbose` there prints the header) → `-q` first; (3) the token
stays exported in the shell → subshell or `unset`, per-session token, tenant-wide scope stated; (4) the URL is Grafana's
"Backend address", not the "API server URL" (gRPC). Nits: reject quotes/whitespace in the token; trap-delete the temp
file; allow-list check keys and the ryftapp.de host; the test's port; dry run without a diff; pre-existing
`Makefile:204` puts the rules token in the container argv. No new processor (Grafana Cloud is listed in the policy).

code-reviewer: no blocker; the check JSON matches the SM agent's proto and validation; all four checks pass against the
live site. Should-fix: (1) SiteProbeFlapping fires per probe for ~20 min after any full outage recovers (proven with a
promtool test; tested fix with `unless … min_over_time(…[30m:1m]) == 0`); (2) SyntheticsMissing watches only the home
job → one `absent_over_time` per job; (3) offline regexp validation uses jq's Oniguruma, not RE2 → RE2, and all
validation before the first POST (no partial apply); (4) `check/list` may be `null` on a fresh stack; (5) "Backend
address" and the stack's region `eu-west-2`; (6) no guard on the body markers (a policy rewording would page critical
after deploy) → assert them against `web/` sources; (7) no CI for `ryftapp.json`/promtool → a `monitoring` job;
(8) STATUS/DECISIONS (lead, G). Nits: SM republishes every 2 min (thresholds hold); HEAD gets 308 from Traefik; curl
timeouts; skip unchanged checks; test robustness. → all back to sre with the security items.

Re-review of the fixes (957ce03): items 2, 4–6, 8–10 and the nits done. **Blocker:** the new `monitoring` job fails in
CI — `mktemp -d` is 0700 and promtool runs as `nobody` under rootful docker (podman on macOS hid it). Should-fix: the
"skip unchanged" diff walks only the wanted side, so a removed marker/status/probe reads "unchanged" and posts nothing
(proven); the flapping suppression also swallows intermittent failures that overlap across probes (proven) → suppress on
SiteDown's own condition. Nits: RE2 helper error paths, `SYNTHETICS_TEST_LOCAL=1` exactly, nested allow-list keys,
SyntheticsMissing's job list checked against the JSON, "~25 minutes", promtool/mimirtool by digest. → back to sre.
Fixed in `affcd17`: `chmod 755` on the temp dir (only provable on rootful docker: the PR run is the test); the diff
projects both sides onto the allow-list (a removed marker, status or header = one update; a dropped probe = four, since
probes are shared); flapping suppressed only on SiteDown's own condition, with an intermittent-overlap test; the nits.

Integration note for C/D/B: `ci.yml` is edited by B (`promote`), C-G (`manifests`) and D (new `monitoring` job) in
disjoint places; merge order B → C → D, each merging `main` in.

## E — `../ROADMAP.md` (lead)
Lines 45, 159, 161, 191, 281, 292, 319, 371, 410, 469 reworded (the file has no git; a backup of the old version
was kept outside the repositories). Masked scan afterwards: no `hard` hit; 12 `warn` (the borrowed milestone and
feature names the user chose to keep: M3, M6, M8). Line count unchanged (542), mode unchanged (600).
