# ryftapp.de

The RYFT website: landing page, science, beta, legal pages, licences, the app's
universal-link fallback and `/.well-known/apple-app-site-association`. Next.js (App Router)
renders it to static HTML; nginx serves it from one image. No cookies, no analytics, no
third-party requests. Rules for working here: `CLAUDE.md`. Why things are the way they are:
`docs/DECISIONS.md`. Where things stand: `docs/STATUS.md`.

## Set up

Node 24 (`.nvmrc`). The sibling folders `../app` and `../plattform` are read by the drift
checks and some tests; the build itself doesn't need them.

```bash
npm ci
npm run fonts      # Satoshi from Fontshare into src/fonts/, verified by SHA-256 (git-ignored)
npm run dev        # http://localhost:3000
npm run build      # static site in out/ (postbuild: runtime removed, CSP hashes, brand check)
```

`npm run dev` and `npm run build` fetch the fonts and the licence texts themselves when
they're missing.

## The image

```bash
npm run image      # docker or podman build → ryft-web:local
npm run serve      # http://localhost:8080, read-only root FS, non-root, like the cluster
npm run e2e        # Playwright against it: every page en/de, light/dark, axe, routes, headers
npm run links      # internal and external links
npx --yes @lhci/cli@0.15.1 autorun   # Lighthouse budgets (CHROME_PATH=… if Chrome isn't found)
npm run serve:stop
```

Build-time settings (public, baked into the HTML; both may be empty):

| Variable | Effect |
| --- | --- |
| `NEXT_PUBLIC_TESTFLIGHT_URL` | The "Open in TestFlight" buttons (/beta, the app-link fallback). Hidden while unset. |
| `NEXT_PUBLIC_SUPPORT_EMAIL` | The invite and contact `mailto:` links. A `TODO(user)` marker while unset. |

Pass them to `npm run image` from the environment, or as `--build-arg` to `docker build`. In
CI they come from the repository variables `TESTFLIGHT_URL` and `SUPPORT_EMAIL`.

## Checks

```bash
npm run typecheck && npm run lint && npm test && npm run build
```

| Script | What it checks |
| --- | --- |
| `npm run lint` | ESLint, Prettier, the brand check, en/de message parity |
| `npm run brand` | The denylisted brand in every tracked file, `out/` and the last commit message (also in the git hooks) |
| `npm run tokens:check` | `src/styles/tokens.*` against `../app/Ryft` |
| `npm run licenses:check` | `config/app-packages.json` against the app's `Package.resolved` |
| `npm run check:policy` | The privacy version against the platform's `PRIVACY_POLICY_VERSION` (warns) |
| `npm test` | Vitest: brand matcher, contrast, mark, messages, legal ports, AASA, science version, CSP, the built site |

## Regenerating from the app

| Command | When |
| --- | --- |
| `npm run tokens` | A colour set, `Theme.swift` metrics or the type scale changed |
| `npm run screenshots` | New captures in `../app/docs/screenshots` |
| `npm run icons` | The mark or the tokens changed (favicon, touch icon, manifest) |
| `npm run og` | Copy or tokens of the OG images changed (needs Playwright's Chromium) |
| `npm run licenses -- --sync` | The app's Swift packages changed |
| `npm run brand:report` | Refresh `docs/BRAND_CHECK.md` for `../app` and `../plattform` |

Commit what they write.

## Publishing a legal page

The texts are in `content/legal/{en,de}/`, ported from the platform's drafts. Each is a
draft until you publish it: a banner, `noindex`, `TODO(user)` markers.

1. Fill every `TODO(user)` in both languages and have the text reviewed.
2. In the front matter: delete `portedFrom`, set `status: published` and
   `effective: 'YYYY-MM-DD'`.
3. A new privacy text is a new file, `privacy/<version>.mdx` in both languages (the old one
   stays at `/privacy/<old-version>`), and the platform's `PRIVACY_POLICY_VERSION` must
   change to the same date: the app asks everyone for consent to that version.
4. Open a PR (`legal: publish privacy 2026-11-01`). After merge, the platform pins the new
   image tag.

## Layout

```text
src/app/(en)/…        English routes at the root (the app's URLs)
src/app/[locale]/…    the same routes under /de
src/views/            page bodies, shared by both
src/components/       mark, chrome, cards, charts, screenshots, prose
src/lib/              i18n, metadata, content loading, the mark's geometry, inline scripts
src/styles/           tokens.css (generated), globals.css
content/              science and legal texts (MDX)
messages/             UI strings, en and de
public/               AASA, icons, OG images, screenshots
nginx/                server config and security headers (csp.conf is generated)
scripts/              tokens, fonts, screenshots, licences, postbuild, CSP, brand guard
tests/, e2e/          Vitest, Playwright
```
