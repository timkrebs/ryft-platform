# Decisions

Judgement calls for `ryftapp.de`, newest last. Each one says what was decided, why, and
what would change it.

## D1 — CSS Modules, not Tailwind (2026-10-20)

Styling is CSS Modules plus two global files (`src/styles/tokens.css`, generated, and
`globals.css`). Every value is a `var(--…)` from the tokens.

- The tokens already are the design system: 23 colours, six metrics, a type scale and three
  durations, generated from the app. Tailwind would need a config that mirrors them (a
  second place to keep in sync) or arbitrary values (`bg-[var(--surface)]`) everywhere.
- The site has a dozen components. Scoped class names are enough, and the CSS stays
  readable next to the app's SwiftUI (`radius 18, padding 18` reads the same in both).
- No build plugin, no PostCSS config, nothing to purge. The CSP allows styles only from
  files, which CSS Modules produce.

Would change if the site grows into many one-off layouts written by several people.

## D2 — Next renders, but no Next runtime ships (2026-10-20)

Next.js (App Router, `output: 'export'`) renders every page to HTML at build time. Then
`scripts/postbuild.mjs` removes the client runtime: every `<script>` without `data-ryft`,
the script preloads, the RSC payload files (`*.txt`, `__next.*`) and the JavaScript under
`_next/`. The only client JavaScript is `src/lib/inline-scripts.ts` (theme: 0.3 KB + 1.1 KB),
inline and hashed into the CSP.

Measured on 2026-10-20 with Next 16.4.2 and React 19.3.1: an empty App Router page ships
**170 KB of gzipped JavaScript** (six chunks), more than twice the budget of 80 KB on `/`.
The site needs none of it: no client state, no client navigation, no forms. The FAQ is
`<details>`, the language switch a link, the theme switch twelve lines of plain JS.

Consequences:
- No client components (`'use client'`) — they would render but never hydrate.
  `tests/static.test.ts` fails if one appears.
- Links are plain `<a>`; every navigation is a full page load of ~6–15 KB of HTML.
- The CSP is tiny: two script hashes, the same on every page.
- `/auth/callback` runs no script at all, so nothing can read the URL's fragment or query.

Would change if a page needs real interactivity; then that page keeps the runtime and the
budget is measured again.

## D3 — The wordmark uses Satoshi Medium, as in the app (2026-10-20)

The brief said "Satoshi Bold with the app's letter spacing". The app's `Wordmark`
(`RyftHeader.swift`) uses `.medium` with tracking `size × 0.32`, and the brand board's
wordmark is a light geometric cut. The web follows the app: `--type-wordmark-weight` (500)
and `--tracking-wordmark` (0.32 em) come from the app through `tokens.mjs`. Changing the
app's weight changes the site's on the next `npm run tokens`.

## D4 — Fonts: static Satoshi files, not preloaded (2026-10-20)

Regular, Medium and Bold WOFF2 exactly as Fontshare supplies them (the ITF FFL forbids
subsetting and format conversion), pinned by SHA-256 in `scripts/fetch-fonts.mjs`, through
`next/font/local` (hashed names under `/_next/static/media/`, long cache, a metric-matched
Arial fallback against layout shift). Not preloaded: text paints at once in the fallback and
swaps when the font arrives, which keeps the LCP independent of 80 KB of fonts on a slow
connection. `tests/output.test.ts` checks the served files are byte-identical to the pins.
Emphasis uses Medium (500), as the app does, so no page fetches Bold: two font files per
page kept the LCP at 1.35 s where a third pushed it to 1.5 s (Lighthouse, mobile).

## D5 — Theme: system by default, a three-way switch in the footer (2026-10-20)

Dark first (the default without a system preference), light from the light tokens.
System / Light / Dark like the app's Appearance setting, stored in `localStorage` under
`ryft.theme` (nothing else is stored; no cookie). The switch is hidden without JavaScript.
It lives in the footer, not the header, to keep the header to logo, three links and the
language on a phone. Screenshots follow the choice by retargeting their `<source>` media.

## D6 — One nginx image, the platform's headers plus a hashed CSP (2026-10-20)

`nginxinc/nginx-unprivileged` 1.30.6 (the stable line) on Alpine 3.24, pinned by its
multi-arch digest; Node 24.22.0 (LTS) only in the build stage. `nginx/default.conf` ports
the platform's `base/web/default.conf` (same routes, `/healthz`, AASA as `application/json`
without redirect, the app-link fallback) and adds trailing-slash normalisation (301 to the
slashless path, relative `Location`), `/de/…`, cache rules (immutable for
`/_next/static/`, a day for `public/` files, five minutes for HTML, `no-store` for the
app-link fallback because auth callbacks carry tokens) and precompressed gzip.

The CSP drops `style-src 'unsafe-inline'` (the old pages had inline `<style>`) and lists the
SHA-256 of each inline script (`scripts/csp.mjs` → `nginx/csp.conf`). Because of D2 there
are two, the same on every page, so one global header is enough. `require-trusted-types-for
'script'` is on: neither script uses a DOM sink. nginx doesn't merge `add_header` across
levels, so every location that sets its own header includes `snippets/headers.conf`.

## D7 — Precompressed gzip, no brotli module (2026-10-20)

The official unprivileged image has `gzip_static` but no brotli. Building `ngx_brotli`
against the exact nginx version needs a compiler stage, and the multi-arch build would
compile it again under QEMU on every change. For pages of 3–15 KB gzipped (the licences
page is the largest at 46 KB) brotli would save a few kilobytes. So: every text file over
1 KB is gzipped at build time (`scripts/precompress.mjs`), anything else on the fly. If
brotli matters later, Traefik's `compress` middleware can add it in front (HANDOFF.md).

## D8 — Legal content: MDX per language, versioned folders, ported text guarded (2026-10-20)

`content/legal/<locale>/{privacy,terms}/<version>.mdx` and `{impressum,support}.mdx`, front
matter `title`, `version`, `effective`, `status`, plus `draftNote` (the draft's own banner
text), `versionNote` (privacy: that the app asks for consent to this version) and
`portedFrom`. A folder per versioned document keeps every published version at
`/privacy/<version>`; the newest is `/privacy`, and its permalink's canonical is `/privacy`.
The drafts were converted mechanically from the platform's HTML (one-off, not committed);
`tests/legal.test.ts` compares the readable text with the source word for word while
`portedFrom` is set. Support's extra parts (export and delete steps with the app's
Account card, contact, response time) sit outside the ported text, in the page.

## D9 — The hero draws Today natively; screenshots elsewhere (2026-10-20)

The brief asked for the Today screenshot in the hero. The available Today captures are
Preview captures with the readiness number missing (dark) or two digits overlapping
mid-animation (light), and a canvas artefact top right. So the hero rebuilds Today in SVG
and HTML (gauge, tiles, a 7-day trend after CHART_STYLE.md) with sample numbers, labelled
"Sample data". Real captures carry the rest: Sleep and Strain (full screens, in a CSS
phone outline), the Train session and the Insights cards (crops clear of the artefact),
the Profile account card on /support. The journal has no capture yet, so it shows a
sample card in the app's style. HANDOFF.md asks the app session for new captures.

## D10 — Checks against the sibling repositories run in CI (2026-10-20)

`web/` is its own repository, but tokens, Swift licences, the AASA, the legal drafts, the
model version and the policy version come from `ryft-app` and `ryft-platform`. Both are
public, so CI checks them out next to `web/` (as locally) and the drift checks and tests
run there with `REQUIRE_SIBLINGS=1`; without that flag they skip when a sibling is missing.
What the build itself needs is committed (tokens, screenshots, the Swift package snapshot),
so the Docker build needs neither sibling.

## D11 — The brand guard (2026-10-20)

Two hard patterns: the name with leetspeak and look-alike letters anywhere, also inside
words, and the name with one repeated separator between every letter. Allowing *any*
separators between letters would flag "who operates". Text is normalised first (NFKD,
accents and zero-width characters removed, entities and escapes decoded), HTML is also
checked with its tags stripped. Test fixtures are ROT13 so the repository passes its own
check; findings are printed masked. The session prompt (`CLAUDE_PROMPT.md`) names the
brand and is git-ignored. Warnings (comparisons, borrowed feature names) don't fail; the
two files that must contain such phrases (the denylist, the tests) are excluded from them.

## D12 — Trivy and gitleaks as pinned images, not actions (2026-10-20)

Security tools run as their official container images pinned by digest
(`aquasec/trivy:0.76.0`, `zricethezav/gitleaks:v8.30.1`) instead of third-party actions,
so a moved tag can't change what runs. Every action is pinned by commit SHA; Dependabot
updates actions, npm and the Dockerfile's base images weekly.

## D13 — A fallback font that exists everywhere (2026-10-20)

next/font's metric-matched fallback is `local(Arial)` only. Linux and Android have no Arial,
so there the fallback face failed, text painted in whatever `system-ui` is, and reflowed
when Satoshi arrived: CLS 0.17 on `/` and 0.19 on `/privacy` in CI's Lighthouse (Ubuntu),
against 0.001 on macOS. `globals.css` now declares `Satoshi Fallback` itself, with the same
overrides next/font computed (ascent 102.23 %, descent 24.29 %, line gap 10.12 %, size
98.8 %) over Arial, Helvetica, Liberation Sans and Arimo (Arial's metric clones on Linux)
and Roboto (Android, the same average width); `adjustFontFallback` is off.

## D14 — The image is public (2026-10-21)

The user chose to make `ghcr.io/timkrebs/ryft/web` public so that the site can go live
without a pull secret in the cluster ("just deploy the website, we care about that stuff
later"). The image contains the Satoshi WOFF2 files, and the ITF Free Font License allows
self-hosting them but not making them available through publicly accessible servers; a
public image lets anyone pull it and take the files out. That risk is accepted for now. The
way back is ready: set the package private again and apply the platform's pull-secret
variant (`docs/HANDOFF.md` → 2, `docs/handoff/platform-web-image.patch`: a
`kubernetes.io/dockerconfigjson` Secret synced by the Vault Secrets Operator).
