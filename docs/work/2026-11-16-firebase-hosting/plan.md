# C1: the site on Firebase Hosting (2026-11-16)

Brief: head-architect, 2026-11-16. Spec: `docs/work/2026-11-14-gcp/design.md` §8, runbook §11 step 10
(`docs/OPERATIONS.md` → GCP → 10). Branch `web/firebase-hosting` from main `02a41e0`. One PR.
Decisions: D233 (platform), web D19.

## Tickets

| # | Owner | Files | Done when |
|---|---|---|---|
| W | web-engineer | `web/**` (except nothing), `Makefile` web targets | below |
| G | gitops-engineer | `.github/workflows/**`, `scripts/check_workflows.sh` + test, `infra/aws/kubernetes/admission-celtest/` (delete), OPERATIONS CI/deploy prose | below |
| L | lead | `infra/gcp` site DNS variables + tests, `deploy/run/config.env` comment, Makefile `admission-celtest`, OPERATIONS step 10, STATUS, DECISIONS D233, CLAUDE.md, PR | below |
| R | code-reviewer | read-only, the whole diff | findings in `review.md`, blockers fixed and re-checked |

### W (web)
- `web/.firebase-tools-version`: one exact version, the only source (Makefile, ci.yml, deploy.yml, step 10 read it).
- `web/scripts/firebase-json.mjs` in `postbuild` (after `csp.mjs`) writes `web/firebase.json` (git-ignored, like
  `nginx/csp.conf`) from `scripts/lib/csp.mjs`: `public: out`, `cleanUrls`, `trailingSlash` unset, `ignore: ["**/*.gz"]`
  only, #156's redirect, `/de` exact → `/` and `^/de/(?P<p>safe)$` → `/:p` (no unfilled capture), the app-link rewrite,
  headers per path class with `regex` only (RE2 subset: no lookaround, no backrefs), the marketing CSP's rule never
  matching `/admin`, `emulators.hosting` on 8080, UI off.
- `web/tests/hosting.test.ts` replaces `nginx.test.ts`: the generated file, rule by rule; exactly one CSP rule per
  representative path; the redirect captures can't leave the site.
- `web/scripts/check-hosting.mjs`: dependency-free (node: builtins), raw headers, exact paths; `--base`, `--www`,
  `--policy-version`, `--apple-cdn`. The same assertions run against the emulator (CI) and live (deploy.yml, step 10).
- `npm run e2e:hosting`: the pinned emulator (`emulators:exec --only hosting --project demo-ryft`) around Playwright,
  `check-hosting.mjs`, links, Lighthouse. `make web-e2e` = build + that. e2e specs ported from nginx to Hosting.
- Settled by tests: one CSP per response; `Content-Type` through `headers` on the AASA; bare `/de`; headers on
  redirect responses; `//evil.example/`, `/%5Cevil.example/`.
- web docs: `web/CLAUDE.md`, `web/README.md`, `web/docs/{DECISIONS (web D19),STATUS}.md`.

### G (CI)
- ci.yml: `images`, `web-image`, `promote` and `admission` jobs gone; `changes` loses `targets`, `promote`, the
  outputs `services/web/held/run_admission`, the `admission` filter and the `force` input; `web-e2e` builds and
  runs `npm run e2e:hosting` (no Docker, no Trivy of an nginx image); `ci-ok` needs updated; header comments.
- `admission-e2e.yml` and `infra/aws/kubernetes/admission-celtest/` deleted (nothing on GCP uses them), and
  whatever only they fed, without touching the rest of `infra/aws/`.
- deploy.yml: `site-off` (notice), `site-build` (no token: build, SUPPORT_EMAIL checks, artifact), `site`
  (environment deploy, WIF as `site-deployer@ryftlab-site`, tip check, deploy only when `web/` differs from the live
  release's commit or on dispatch, `firebase deploy --message "web <sha>"`, then `check-hosting.mjs` live).
  Switch: repository variable `SITE_DEPLOY=on`.
- `check_workflows.sh` (+ test): environment `deploy` only for deploy.yml's `deploy` and `site`; the App key named
  nowhere; the site jobs' shape (permissions, actions by SHA, tip gate, no token in site-build).

## Order
W ∥ G (disjoint files; G codes against W's interface: `.firebase-tools-version`, `firebase.json` from
`npm run build`, `npm run e2e:hosting`, `check-hosting.mjs` flags) → L → R → fixes → narrow re-check → PR → CI.
#192 (step 0) when #191 is merged.

## Risks
- Hosting vs emulator differences (header merging, redirects' headers, `//` handling): the live check in the
  `site` job and at step 10 repeats every assertion; the emulator wins where the docs disagree.
- firebase-tools with WIF's `external_account` credentials *(unchecked until the first dispatch)*.
- Spark storage: every deploy keeps a version; step 10 sets a version retention.
- `check_policy_in_force.sh`'s EKS half needs the EKS web pin to serve the version; with promote gone, C2 must drop
  that half (noted for C2).

## Rollback
Revert the merge. The site: Firebase console → Hosting → Release history → Roll back, or `SITE_DEPLOY` off.

## Needs the user
Step 10 (the project, the console's custom domains, the DNS variables PR + apply 3, `SITE_DEPLOY=on`, the
dispatch, the checks). Merging #191 before #192.
