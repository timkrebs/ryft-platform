# ryftlab.de

The RYFT website: landing page, science, beta, legal pages, licences, the app's
universal-link fallback and `/.well-known/apple-app-site-association`. Next.js (App Router)
renders it to static HTML; Firebase Hosting serves it (project `ryftlab-site`, D19), with the
routes and headers of a `firebase.json` the build generates. No cookies, no analytics, no
third-party requests. Rules for working here: `CLAUDE.md`. Why things are the way they are:
`docs/DECISIONS.md`. Where things stand: `docs/STATUS.md`.

## Set up

This is `web/` of the platform repository (`timkrebs/ryft-platform`). Node 24 (`.nvmrc`). The app
is a sibling of the platform checkout (`../../app` from here; `RYFT_APP_DIR` overrides it, absolute or relative to `web/`): the
drift checks and some tests read it, the build itself doesn't need it. The platform's own files
(`../docs/SCORING.md`, the `PRIVACY_POLICY_VERSION` in `deploy/run/config.env`) are one folder up.

```bash
make web-install   # from the repository root: npm ci in web/
make hooks         # once per clone: the brand guard on staged web/ files and on every commit message
```

Then, from `web/` (or with the `make web-*` targets from the root: `web-test`, `web-build`,
`web-e2e`, `web-hosting`, `web-image`, `web-serve`, `web-tokens`):

```bash
npm run fonts      # Satoshi from Fontshare into src/fonts/, verified by SHA-256 (git-ignored)
npm run dev        # http://localhost:3000
npm run build      # static site in out/ (postbuild: runtime removed, CSP hashes, firebase.json, brand check)
```

`npm run dev` and `npm run build` fetch the fonts and the licence texts themselves when
they're missing.

## Firebase Hosting, locally

The Hosting emulator of the pinned firebase-tools (`.firebase-tools-version`, the one place the
version lives) serves `out/` with the generated `firebase.json` at http://localhost:8080. The
first run installs it into `node_modules/.cache/firebase-tools-<version>` with npm's install
scripts off, then builds `re2` alone (the emulator needs it for the regex rules; web D19). The
project is `demo-ryft`, so no login and no Google project is involved. Build first.

```bash
npm run e2e:hosting   # the emulator around Playwright, check:hosting, links and Lighthouse; exit 1 if any failed
npm run hosting       # the emulator until Ctrl-C
npm run e2e           # Playwright against a running emulator (BASE_URL=… for another origin)
npm run check:hosting -- --base http://localhost:8080   # the deploy's live checks, one PASS/FAIL line each
```

`firebase.json` is git-ignored: it carries this build's script hashes. Its rules are
`scripts/lib/hosting.mjs` (`tests/hosting.test.ts` pins them; D19 says why they look the way
they do). The site goes live through CI only (`deploy.yml`, the `site` job, once the repository
variable `SITE_DEPLOY` is `on`); never `firebase deploy` by hand. `check:hosting` takes
`--base` (required), `--www <origin>`, `--policy-version YYYY-MM-DD` and `--apple-cdn`, and runs
from the repository root too (`node web/scripts/check-hosting.mjs`, Node built-ins only).

The nginx image stays until the platform's PR D3 removes it (design §10), and is no longer what ships: `npm run image` (docker or podman →
`ryft-web:local`), `npm run serve` (http://localhost:8080), `npm run serve:stop`.

Build-time settings (public, baked into the HTML; both may be empty):

| Variable | Effect |
| --- | --- |
| `NEXT_PUBLIC_TESTFLIGHT_URL` | The "Open in TestFlight" buttons (/beta, the app-link fallback). Hidden while unset. |
| `NEXT_PUBLIC_SUPPORT_EMAIL` | The invite and contact `mailto:` links. A `TODO(user)` marker while unset. |

Pass them to `npm run build` from the environment (`npm run image` and `docker build
--build-arg` for the nginx image). In CI they come from the repository variables `TESTFLIGHT_URL`
and `SUPPORT_EMAIL`.

## Checks

```bash
npm run typecheck && npm run lint && npm test && npm run build
```

| Script | What it checks |
| --- | --- |
| `npm run lint` | ESLint, Prettier, the brand check |
| `npm run brand` | The denylisted brand in every file tracked under `web/`, `out/` and the last commit message (the git hooks in `../.githooks` run it on staged `web/` files and every commit message) |
| `npm run tokens:check` | `src/styles/tokens.*` against `../../app/Ryft` |
| `npm run licenses:check` | `config/app-packages.json` against the app's `Package.resolved` |
| `npm run check:policy` | The privacy version against `PRIVACY_POLICY_VERSION` in `../deploy/run/config.env` and its k8s copies (the published version; exact line `PRIVACY_POLICY_VERSION=YYYY-MM-DD`): the page exists, it is the newest, api and ingest agree (warns; `-- --strict` fails, and runs from the repository root as `node web/scripts/check-policy-version.mjs --strict`, Node built-ins only) |
| `npm test` | Vitest: brand matcher, contrast, mark, messages, legal ports, AASA, science version, CSP, `firebase.json`'s rules, the built site |
| `npm run e2e:hosting` | The Hosting emulator: Playwright (every page, axe, routes, headers), `check:hosting`, links, Lighthouse |

## Regenerating from the app

| Command | When |
| --- | --- |
| `npm run tokens` | A colour set, `Theme.swift` metrics or the type scale changed |
| `npm run screenshots` | New captures in `../../app/docs/screenshots` |
| `npm run icons` | The mark or the tokens changed (favicon, touch icon, manifest) |
| `npm run og` | Copy or tokens of the OG image changed (needs Playwright's Chromium) |
| `npm run licenses -- --sync` | The app's Swift packages changed |
| `npm run brand:report` | Refresh `docs/BRAND_CHECK.md` for the app, this repository outside `web/`, and the shared docs |

Commit what they write.

## Publishing a legal page

The texts are in `content/legal/en/` (English only, D18), ported from the platform's drafts. Each is a
draft until you publish it: a banner, `noindex`, `TODO(user)` markers.

1. Fill every `TODO(user)` and have the text reviewed.
2. In the front matter: delete `portedFrom`, set `status: published` and
   `effective: 'YYYY-MM-DD'`.
3. A new privacy text is a new file, `privacy/<version>.mdx`, named
   `YYYY-MM-DD.mdx` (the old one stays: `/privacy/<old-version>` is what earlier consents
   point at), the version is appended to `content/legal/privacy-versions.json`, and
   `PRIVACY_POLICY_VERSION` in `deploy/run/config.env` (with its copies in
   `deploy/k8s/base/{api,ingest}/config.env` until D3) and `docker-compose.yml` changes to the
   same date in the same PR. So a policy PR is: the page, `privacy-versions.json`,
   `deploy/run/config.env`, the k8s copies and `docker-compose.yml`. Only on the user's
   decision: the app asks every user to consent again. `check:policy --strict` fails otherwise.
4. Open a PR (`legal: publish privacy 2026-11-01`). The merge puts the version into force:
   `deploy.yml` deploys the services with it and the site with the page, and the `site` job's
   live check fails while ryftlab.de/privacy shows another version. The window is described in
   `docs/OPERATIONS.md`.

## Layout

```text
src/app/(en)/…        the site's routes (English only, D18; the app's URLs)
src/app/(admin)/…     /admin, its own root layout (the operator's page)
src/admin/            the admin page's script (TypeScript, bundled by esbuild into one inline script)
src/views/            page bodies
src/components/       mark, chrome, cards, charts, screenshots, prose
src/lib/              i18n, metadata, content loading, the mark's geometry, inline scripts
src/styles/           tokens.css (generated), globals.css
content/              science and legal texts (MDX)
messages/             UI strings (en.json)
public/               AASA, icons, the OG image, screenshots
nginx/                the image's server config and headers until PR D3 (csp.conf is generated)
scripts/              tokens, fonts, screenshots, licences, postbuild, CSP, firebase.json, Hosting checks, brand guard
scripts/lib/hosting.mjs  firebase.json's rules (redirects, rewrites, headers)
.firebase-tools-version  the pinned firebase-tools
tests/, e2e/          Vitest, Playwright
```
