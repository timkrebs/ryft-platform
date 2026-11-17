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
- **Tokens come from the app.** Colours, radii, spacing, type scale (with the label's case and
  tracking) and motion are generated from `../../app/Ryft` by `npm run tokens` into
  `src/styles/tokens.{css,json}`, at the commit in `web/.app-ref` (platform D234): CI checks the
  app out there; regenerate from a checkout at that commit and move the pin in its own PR
  (`../docs/OPERATIONS.md` → GCP → Deploy, roll back, rotate → The app pin). Never hard-code a
  colour, a label's case or its tracking in CSS or components; CI fails when the tokens drift.
  The site is dark-only, like the app (web D20): one value per colour, no light palette, no
  theme switch.
- **Static export only.** `output: 'export'`, no API routes, no proxy/middleware, no server
  runtime, no database. Pages render to HTML at build time; the postbuild removes the
  Next/React runtime (D2 in `docs/DECISIONS.md`), so no client components. No page runs a
  script except `/admin` (below; web D20): the marketing CSP is `script-src 'none'`, and the
  postbuild fails when any other page carries a `<script>`.
- **No tracking.** No cookies, no analytics, no third-party requests (fonts self-hosted,
  no CDNs), no forms. Nothing is stored in the browser: no `localStorage` at all (the theme
  choice went with web D20), and only `/admin` uses `sessionStorage`.
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
  The privacy `version` equals `PRIVACY_POLICY_VERSION` in `../deploy/run/config.env` (its home on
  Cloud Run; the line is exactly `PRIVACY_POLICY_VERSION=YYYY-MM-DD`, no quotes, spaces or comment;
  the copies in `../deploy/k8s/base/{api,ingest}/config.env` must equal it until D3;
  `node scripts/check-policy-version.mjs --strict`, also in CI). Only the user flips
  `status: draft` → `published`, and a new privacy version (`PRIVACY_POLICY_VERSION`) only
  on the user's decision: it asks every user to consent again. The new text, its entry in
  `content/legal/privacy-versions.json` and the version bump go in one PR (the page,
  `privacy-versions.json`, `deploy/run/config.env`, the k8s copies, `docker-compose.yml`);
  old versions keep their pages. The merge puts it into force: `deploy.yml` deploys the services
  with the new version and the site with the new page, and the `site` job's live check fails while
  ryftlab.de/privacy shows another version (the window is in `../docs/OPERATIONS.md`).
- **Science numbers come from `../docs/SCORING.md` and `../docs/ARCHITECTURE.md`**, by
  hand, for the model version in `content/science/meta.json`. A test fails when the
  platform's model version moves.
- **No medical claims.** RYFT is a wellness app, not a medical device; say so near scores.
- **English only** (web D18). Every UI string in `messages/en.json`; `/de/…` redirects to the
  English page for the app's older builds. German may come back; the views keep `Locale`.
- **Fonts**: Satoshi Light, Regular, Medium and Bold WOFF2 files are fetched by `npm run fonts`,
  verified by their SHA-256 in `config/fonts.json` and never committed (ITF FFL: self-hosting yes,
  redistribution through repositories no, no subsetting, no conversion). A page uses Light
  (titles, numbers), Regular and Medium (emphasis), never Bold: three files against the LCP
  budget (web D4, D20).

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
emulator is the firebase-tools of `.firebase-tools-version` (the version lives nowhere else). It is
installed into `node_modules/.cache/firebase-tools-<version>` with npm's install scripts off, except
for `re2`, which the emulator needs for the regex rules (web D19). `firebase.json` is generated by the build and
git-ignored: change `scripts/lib/hosting.mjs`, never the file. The nginx image (`npm run image &&
npm run serve`, `npm run e2e`) stays until the platform's PR D3 removes it, but is not what ships. Never run `firebase deploy`
yourself: `deploy.yml` does. The drift checks read the app from `../../app`; `RYFT_APP_DIR` overrides it
(absolute, or relative to `web/`), and CI reads it at the commit in `.app-ref`. Without the app, `tokens:check`, `licenses:check` and the
app icon's drift test (`tests/app-icon.test.ts`, web D22) skip outside CI (and fail when `CI` is set); `REQUIRE_SIBLINGS=1` only affects `tests/aasa.test.ts`,
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
