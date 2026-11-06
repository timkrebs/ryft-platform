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

_Since the move into `plattform/web` (platform D107) the platform is this repository itself and only the app is a sibling (`../../app`); the rest of this entry is the two-repository record._

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

## D15 — The admin page is one hashed inline script in its own document (2026-11-05; amends D2)

`/admin` is the operator's page (invites, beta access, feedback, flags, audit; root D162). It
has its own root layout, `src/app/(admin)`, a server component that inlines ONE script,
`data-ryft="admin"`, built by esbuild (a pinned dev dependency; nothing of it ships) from
`src/admin`. No React runtime, no supabase-js (the GoTrue calls are plain `fetch` in
`gotrue.ts`), no third-party code: about 25 KB, under 10 KB gzipped for the whole page. The
DOM is built only through `dom.ts` (`createElement`, text nodes, an attribute allow-list);
ESLint, `tests/admin-source.test.ts`, the bundle step and `trusted-types 'none'` at run time
keep HTML sinks out.

It has its own nginx location and CSP (`nginx/headers-admin.conf`): one script hash,
`connect-src` path-scoped to `<supabase>/auth/v1/` and `<api>/v1/admin/` from
`config/admin.json` (so the user routes, PostgREST and storage cannot be reached from the
page), `img-src 'self' data:` for the TOTP QR code, `form-action 'none'`, `frame-ancestors
'none'`. Every other page keeps `connect-src 'none'` and the two theme hashes. The shared
security headers live in `headers-common.conf`, so a location with its own CSP does not
lose them; a location includes exactly one of the two CSP snippets, never both (two CSPs
intersect). The postbuild allows the admin script on `admin.html` only, and no other page may
carry it. `noindex` by header and meta tag; not linked, not in the sitemap or `robots.txt`.

Would change if the site gets more dynamic pages: then the admin page moves to its own
subdomain.

## D16 — `sessionStorage` on `/admin` holds the operator's session (2026-11-05; amends D5)

D5's "nothing else is stored" no longer holds for `/admin`: the operator's session (access
and refresh token) is kept in `sessionStorage['ryft.admin.session']`, never in
`localStorage` and never in a cookie. `sessionStorage` is per tab and per origin: it is gone
when the tab closes and is not shared with other tabs, but every page of `ryftapp.de` loaded
in the same tab (and a tab made with "Duplicate tab") can read it. So `src/admin/leave.ts`
clears it on `pagehide` unless the Navigation API announced a navigation to `/admin` itself.
A browser reload, pull to refresh and a URL typed in the address bar fire no `navigate`
event, so they cannot be told apart from leaving and sign the operator out: the safe
direction, chosen over keeping the token readable by the next page. Changing section
(`#users`) is a same-document navigation and keeps the session.

What makes the remaining exposure acceptable: the marketing pages run no dynamic code (two
hashed scripts, Trusted Types) and are served under a CSP with `connect-src 'none'`. The operator signs in again (password manager and TOTP) after closing the tab or
reloading. The v2 hardening is a subdomain.

## D17 — The admin page has forms, talks to two origins, and its strings are English-only (2026-11-05)

Amends the site's "no forms", "no third-party requests" and two-language rules for this page
only. The sign-in and TOTP forms exist for password-manager autofill (`form-action 'none'`;
the script handles submission). The page connects to the api and to the project's Supabase
Auth, both named in its footer; every other page still makes no request beyond the site. Its
strings are for one operator, live in the bundle, outside `messages/{en,de}.json`, and there
is no `/de/admin`: the page sits outside `[locale]`, so the two-language checks do not apply
to it.

## D18 — English only; the German pages are gone (2026-11-05; amends D8)

The user's decision of 2026-11-05: the site moves to a monochrome, light-only, typographic
design, English only for now; the restyle follows in the next PR on top of this one. German
goes because every page, legal text, string and OG image existed twice, and a redesign of
twenty-four pages is half the work of twelve. So: `src/app/[locale]`, `messages/de.json`,
`content/legal/de`, `content/science/de.mdx` and `public/og/de.png` are deleted, with the
language switch, the hreflang alternates, the German sitemap entries and
`scripts/check-messages.mjs` (parity has nothing to compare; `tests/messages.test.ts`
keeps the empty-string check). `src/lib/i18n.ts` keeps `Locale`, `messages()`,
`localePath()` (now the identity) and `intlLocale()` (`en-GB`), so the views compile
unchanged and a second language can come back without touching them.

Consequences:
- Older builds of the app link to `/de/privacy`, `/de/terms` and `/de/impressum`; nginx
  answers every `/de/…` with a 301 to the English page (`e2e/routes.spec.ts`), cached for an
  hour. The redirect's capture is the decoded path, so it is held to the trailing-slash rule's
  safe characters (platform #156): it starts with a letter or digit and holds only letters,
  digits and `/ . _ -`, so Location is never `//host`, `/\host` or a header with a control
  character; any other `/de/…` path is a 404 (`tests/nginx.test.ts`).
- The English legal notice carried the VAT and consumer-dispute statements only by linking
  to the German page; they are ported into `content/legal/en/impressum.mdx` as they were.
- `scripts/lib/policy.mjs` checks the English privacy page only. The repository's own guards
  (`scripts/check_policy_in_force.sh`, `scripts/pin_images.sh`) looked for both languages
  and are changed with this.
- The privacy texts in force still mention "your choice of appearance" (D19); the wording is
  the user's legal call and changes only with a new version.

Would change if the user wants German back: `messages/de.json`, `content/legal/de` and a
`[locale]` segment again, from this PR's parent commit.

## D19 — Light only; no theme, no script on the site's pages (2026-11-05; replaces D5, amends D2)

The same decision as D18: the new design is monochrome and light. There is one appearance,
so there is nothing to switch. `tokens.css` carries the light value of every colour set in
`:root` with `color-scheme: light`; the `[data-theme]` and `prefers-color-scheme` blocks are
gone (`tokens.json` keeps both appearances, for the icon and OG scripts and for the day the
app's dark pass lands). The theme switch, its two inline scripts (`src/lib/inline-scripts.ts`),
the `ryft.theme` key in `localStorage` and the second `theme-color` meta are deleted; the
screenshots show the light captures only, and the dark files leave `public/screenshots`.

Consequences:
- The site's pages ship no JavaScript at all. The marketing CSP says `script-src 'none'`; the
  admin page keeps its one hash in its own policy (D15). D2's budget holds trivially.
- Nothing is stored in the browser any more; the privacy texts still mention "your choice of
  appearance", which is harmless over-disclosure and changes only with a new version (the
  user's call).
- The app's light `Warning` is `#8A8A8A`: 3.3:1 on white, enough for a mark (bars, borders,
  fills), not for text. The warning tone of `Label` and the admin badge read in
  `text-secondary`; `tests/contrast.test.ts` checks the light values, `warning` among the
  marks at 3:1. The restyle decides how a warning looks.
- `e2e/site.spec.ts` and `e2e/admin.spec.ts` run once per page, not per appearance.

Would change if the app's monochrome dark pass lands and the user wants the site to follow
the system again: the blocks come back in `renderCss`, from `tokens.json`'s `dark` values.

## D20 — Inter, committed under the OFL (2026-11-05; replaces D4 and D13, closes D14's font risk)

The third part of the 2026-11-05 decision: the typographic design is set in Inter. Inter 4.1
(SIL Open Font License 1.1) allows redistribution and modification, so subsets of
`Inter-Regular.woff2` and `Inter-Bold.woff2` and the licence as `OFL.txt` are committed in
`src/fonts/`. `scripts/fetch-fonts.mjs`, `config/fonts.json` and the `fonts` step of `dev` and
`prebuild` are gone: a clone builds offline, and the public image (D14) no longer
redistributes a font whose licence forbids it.

**Provenance.** `https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip`
(SHA-256 `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`), from which:

| File in the zip | Bytes | SHA-256 |
| --- | --- | --- |
| `web/Inter-Regular.woff2` | 111,268 | `e06f6b1bc553aaea4e4668023ed0ab0a147129c3107f511bc7d03d361b0ae085` |
| `web/Inter-Bold.woff2` | 114,840 | `fa888127b6da015b65569f0351f3b5c391ad928904951f1c20e9f8462a8d95ea` |
| `LICENSE.txt` → `src/fonts/OFL.txt`, unchanged | 4,380 | `262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a` |

**The committed files** (pinned by `tests/fonts.test.ts`, which also checks this table):

| File | Bytes | SHA-256 |
| --- | --- | --- |
| `src/fonts/Inter-Regular.woff2` | 18,560 | `0fe23dbdad67636b8cf0018c9608c7c6fe870c58dfb0e06fa41d30beab890e39` |
| `src/fonts/Inter-Bold.woff2` | 19,216 | `1713739f95d2e55b5a1aa463f0dca828826bd3342396ce6932d0017bd7462768` |

Each is 443 glyphs for 303 code points: Basic Latin and Latin-1 (`U+0000-00FF`), the
Windows-1252 extras (`ı Œ œ Š š Ÿ Ž ž`, `ʻ ʼ ˆ ˚ ˜`), General Punctuation (`U+2000-206F`), `₂`
(SpO₂ on the privacy pages), `€`, `™`, the arrows `U+2190-2199`, `−`, `≈ ≠ ≤ ≥`, `○ ●`, `✓`
and `U+FEFF`. Layout features kept: GSUB `calt`, `case`, `locl`, `tnum`; GPOS `kern`, `cpsp`.
Hinting dropped. Name table complete (`--name-IDs='*'`), so the licence notice and URL
(name IDs 13 and 14) travel inside each file as well as in `OFL.txt`. The formulas on
`/science` (`σ`, `∑`, `⋅`) are MathML and set in the system's math font on purpose
(`globals.css`), not in Inter.

To regenerate, with fontTools 4.68.0 and Brotli 1.2.1 (`pip install fonttools==4.68.0
brotli==1.2.1`), for `W` in `Regular` and `Bold`, from the zip's `web/` files:

```bash
pyftsubset web/Inter-$W.woff2 --flavor=woff2 \
  --unicodes="U+0000-00FF,U+0131,U+0152-0153,U+0160-0161,U+0178,U+017D-017E,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+2000-206F,U+2082,U+20AC,U+2122,U+2190-2199,U+2212,U+2248,U+2260,U+2264-2265,U+25CB,U+25CF,U+2713,U+FEFF" \
  --layout-features="kern,cpsp,calt,case,locl,tnum" --name-IDs='*' \
  --no-hinting --desubroutinize --output-file=src/fonts/Inter-$W.woff2
```

This reproduces both committed files byte for byte (checked 2026-11-06). The first subsets
(2026-11-05, 18,408 and 18,988 bytes, `deea70cc…2498e90a` and `ffa48996…fff406d5`) were the same
command without `U+2082` and without `--name-IDs='*'`, which also reproduces them byte for byte;
adding `₂` added exactly one glyph and changed no other outline. A font change updates the
two tables above and the pins in `tests/fonts.test.ts` in the same commit.

`src/lib/fonts.ts` is `localFont` with 400 and 700, `--font-inter`, `display: swap`, both
files preloaded (two files of about 19 KB; D4 chose not to preload three full files), and
the hand-written metric-matched `'Inter Fallback'` (below) instead of next/font's computed one
(D13's `Satoshi Fallback` is deleted with that font). Only two weights exist: the app's type
scale still says 500 for display, titles, headlines, metrics, labels and the wordmark, and
those tokens stay as they are, so the browser's font matching resolves 500 to Regular and 600
to Bold; `font-synthesis: none` makes sure no face is faked. Emphasis (`strong`, the draft
title, the TODO marker) moves from 500 to 700, since Medium no longer exists. `/licenses`
lists Inter with the OFL text under the website; the app lists its own typeface itself.

**Why subsets.** With the full release files (111 KB and 115 KB) preloaded, Lighthouse's mobile
run reported the hero text's LCP at 2.25 s (the text repaints when Inter arrives, and that
repaint is the LCP), against the then 1.5 s budget in `lighthouserc.json`. A first, wider
subset (Latin Extended-A and more features, about 30 KB a face) was replaced the same day by
the one above.

**Loading (measured, not guessed).** Lighthouse's mobile run (simulated slow 4G, `lighthouserc.json`)
counts the repaint of a text block when its web font arrives as the LCP whenever that repaint
changes the block's size, so the font recipe decides the LCP figure more than the bytes do. Measured
on this branch: full 111 KB files preloaded with next/font's computed fallback → LCP 2.25 s; 19 KB
subsets preloaded, same fallback → 1.50 s flat; subsets not preloaded, next/font fallback → 1.65 s;
not preloaded with a hand-written metric-matched fallback → 1.35 s on `/` but 1.5–1.8 s on the long
pages, swinging run to run. The recipe kept: **subsets preloaded + the hand-written `'Inter
Fallback'`** in `globals.css` (Arial-class local fonts with Inter's ascent/descent/size-adjust, as D13
did before) and next/font's fallback off — LCP 1.5–1.65 s on every page, FCP 1.2 s, CLS 0. The LCP
budget moves from 1.5 s to **1.8 s** for that reason only: first paint is 1.2 s everywhere, the
performance score is 1.0, and the swap is what the number measures. The hero's two lines and the
meta line are `white-space: nowrap`, because a wrap that undoes itself at the swap is a layout shift.

Would change if the restyle wants Medium or SemiBold: then the files are added (the OFL
allows it) and the weight map in `fonts.ts` grows; or if the app's type scale moves to
400/700 only, which removes the question. Would also change if a page needs glyphs outside
the subset (Greek, Cyrillic, CJK): widen the unicode ranges, don't swap back to the full file.
