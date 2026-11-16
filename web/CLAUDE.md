# RYFT web — instructions for Claude Code

You are the **web agent** for RYFT: `ryftlab.de`, a static site (landing, science, beta,
legal pages, licences, the app's universal-link fallback and the AASA file). This folder
is `web/` of the platform repository (`timkrebs/ryft-platform`, D107). The root `CLAUDE.md`
applies to the whole repository, `web/` included (secrets, git, production); this file adds
the site's rules. The SwiftUI app is a sibling checkout of the platform, `../../app` from
here: read from it, never write there.

## Rules
- **Brand rule.** One wearable brand's name never appears anywhere: text, code, comments,
  alt text, metadata, file names, URLs, images, commit messages, generated output. No
  competitor comparisons of any kind and no pricing jabs (the phrases are in the
  denylist's warn rules), no borrowed feature names from another brand's vocabulary. Apple Health, Apple Watch, Garmin, Oura
  only as data sources ("any watch or ring that writes to Apple Health"). No hardware of
  our own. Enforced by `npm run brand` (pre-commit, commit-msg, `npm run lint`, postbuild,
  CI); rules in `config/brand-denylist.json`. Refer to it as "the denylisted brand".
- **Tokens come from the app.** Colours, radii, spacing, type scale and motion are
  generated from `../../app/Ryft` by `npm run tokens` into `src/styles/tokens.{css,json}`.
  Never hard-code a colour in CSS or components; CI fails when the tokens drift.
- **Static export only.** `output: 'export'`, no API routes, no proxy/middleware, no server
  runtime, no database. Pages render to HTML at build time; the postbuild removes the
  Next/React runtime (D2 in `docs/DECISIONS.md`), so no client components — the only
  client JS is `src/lib/inline-scripts.ts`. Every inline script is hashed into the CSP.
- **No tracking.** No cookies, no analytics, no third-party requests (fonts self-hosted,
  no CDNs), no forms. `localStorage` holds the theme choice and nothing else.
- **The one exception: `/admin`** (the operator's page; web D15–D17). It is its own document
  (`src/app/(admin)`, a separate root layout, `noindex`, not linked, not in the sitemap or
  `robots.txt`) with ONE inline hashed script bundled by esbuild from `src/admin` (no framework,
  no supabase-js, no client components: it is a server component that inlines the bundle). It
  has forms (sign-in, TOTP), talks to exactly two origins (`config/admin.json`: the api and the
  Supabase project, path-scoped to `/v1/admin/` and `/auth/v1/` in `connect-src` of its own CSP, `adminPolicy` in
  `scripts/lib/csp.mjs`, served by `firebase.json`'s `^/admin$` rule) and keeps the
  session in `sessionStorage` (never `localStorage`, never a cookie; it is per tab but readable by every
  page of the origin in that tab, so it is cleared when the tab leaves `/admin`, `src/admin/leave.ts`). Its UI strings are
  English-only and live in the bundle, outside `messages/`. Everything the api returns is
  rendered with `textContent` (no HTML sink; `trusted-types 'none'`; ESLint and
  `tests/admin-source.test.ts` enforce it). Exactly one CSP rule of `firebase.json` matches `/admin`
  (the admin one) and exactly one matches every other path (the marketing one), never both: two
  CSPs intersect, and how Hosting merges rules is not documented (web D19, `tests/hosting.test.ts`,
  `e2e/hosting.spec.ts`). Every other page keeps
  `connect-src 'none'`, and no change here may widen the marketing policy.
- **Legal content is ported, not invented.** `content/legal/` holds the platform's drafts,
  restructured only. Keep every `TODO(user)`; never change legal meaning or add facts.
  The privacy `version` equals `PRIVACY_POLICY_VERSION` in `../deploy/k8s/base/{api,ingest}/config.env`
  (kustomize env files; the line is exactly `PRIVACY_POLICY_VERSION=YYYY-MM-DD`, no quotes, spaces or
  comment; `node scripts/check-policy-version.mjs --strict`, also in CI; that is the *published*
  version). The version in force on EKS is set by promote together with the web pin
  (`overlays/eks`), once the pinned image carries the page. Only the user flips
  `status: draft` → `published`, and a new privacy version (`PRIVACY_POLICY_VERSION`) only
  on the user's decision: it asks every user to consent again. The new text, its entry in
  `content/legal/privacy-versions.json` and the version bump go in one PR (the page,
  `privacy-versions.json`, base `config.env` of api and ingest, `docker-compose.yml`);
  old versions keep their pages. The version goes into force with the web pin, not with the
  merge (the window is in `../docs/OPERATIONS.md`).
- **Science numbers come from `../docs/SCORING.md` and `../docs/ARCHITECTURE.md`**, by
  hand, for the model version in `content/science/meta.json`. A test fails when the
  platform's model version moves.
- **No medical claims.** RYFT is a wellness app, not a medical device; say so near scores.
- **English only** (web D18). Every UI string in `messages/en.json`; `/de/…` redirects to the
  English page for the app's older builds. German may come back; the views keep `Locale`.
- **Fonts**: Satoshi WOFF2 files are fetched by `npm run fonts`, verified by hash and never
  committed (ITF FFL: self-hosting yes, redistribution through repositories no, no subsetting).

## Verify before every commit
From the repository root:
```bash
make web-test web-build   # typecheck, lint (ESLint, Prettier, brand), Vitest, static export + firebase.json
make web-e2e              # the build, then the Firebase Hosting emulator with Playwright, check-hosting,
                          # links and Lighthouse against it (no Podman or Docker, no login)
```
(`make web-install` once; `make hooks` once per clone.) The same from `web/` with `npm ci`,
`npm run typecheck && npm run lint && npm test && npm run build` and `npm run e2e:hosting`
(`make web-hosting` or `npm run hosting` keeps the emulator up at http://localhost:8080). The
emulator is `npx --yes firebase-tools@<version>` with npm's install scripts off, the version in
`.firebase-tools-version` and nowhere else (web D19). `firebase.json` is generated by the build and
git-ignored: change `scripts/lib/hosting.mjs`, never the file. The nginx image (`npm run image &&
npm run serve`, `npm run e2e`) stays until the platform's PR D3 removes it, but is not what ships. Never run `firebase deploy`
yourself: `deploy.yml` does. The drift checks read the app from `../../app`; `RYFT_APP_DIR` overrides it
(absolute, or relative to `web/`). Without the app, `tokens:check` and `licenses:check` skip
outside CI (and fail when `CI` is set); `REQUIRE_SIBLINGS=1` only affects `tests/aasa.test.ts`,
turning its skip into an error.

## Git: branches and pull requests (never push to `main`)
This repository's flow: every session starts from an up-to-date `main` on a short branch
(`feat/…`, `fix/…`, `chore/…`, `ci/…`, `docs/…`, `legal/…`, `web/…`), commits there, pushes, and
opens a PR in `timkrebs/ryft-platform` with `gh pr create --fill`. The PR title is
`area: what changed`, with any area (`web:`, `pages:`, `legal:`, `design:`, `docs:`, …). A
privacy text and `PRIVACY_POLICY_VERSION` change in the same PR. Wait for the required checks
(`gh pr checks --watch`), then ask the user to merge: the merge ships the site once the repository
variable `SITE_DEPLOY` is `on`: CI's `deploy.yml` `site` job deploys `out/` with the generated
`firebase.json` to Firebase Hosting (project `ryftlab-site`, web D19) and runs
`scripts/check-hosting.mjs` against ryftlab.de and www; while it is off, a merge deploys nothing
(a privacy text goes live with that deploy; the version in force: the legal rule above). Never commit to `main` directly, never force-push. Ask
before changing GitHub settings.
