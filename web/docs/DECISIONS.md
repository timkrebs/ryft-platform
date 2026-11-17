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

_Amended by D20: the theme scripts and `src/lib/inline-scripts.ts` are gone; no page but `/admin` runs a script, and the marketing CSP is `script-src 'none'`._

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

_Amended by D20: the app's wordmark is Satoshi Light now, so `--type-wordmark-weight` is 300; the tracking (0.32 em) is `Wordmark.tracking`._

The brief said "Satoshi Bold with the app's letter spacing". The app's `Wordmark`
(`RyftHeader.swift`) uses `.medium` with tracking `size × 0.32`, and the brand board's
wordmark is a light geometric cut. The web follows the app: `--type-wordmark-weight` (500)
and `--tracking-wordmark` (0.32 em) come from the app through `tokens.mjs`. Changing the
app's weight changes the site's on the next `npm run tokens`.

## D4 — Fonts: static Satoshi files, not preloaded (2026-10-20)

_Amended by D20: Satoshi Light joins them, and a page now fetches three files (Light, Regular, Medium)._

Regular, Medium and Bold WOFF2 exactly as Fontshare supplies them (the ITF FFL forbids
subsetting and format conversion), pinned by SHA-256 in `scripts/fetch-fonts.mjs`, through
`next/font/local` (hashed names under `/_next/static/media/`, long cache, a metric-matched
Arial fallback against layout shift). Not preloaded: text paints at once in the fallback and
swaps when the font arrives, which keeps the LCP independent of 80 KB of fonts on a slow
connection. `tests/output.test.ts` checks the served files are byte-identical to the pins.
Emphasis uses Medium (500), as the app does, so no page fetches Bold: two font files per
page kept the LCP at 1.35 s where a third pushed it to 1.5 s (Lighthouse, mobile).

## D5 — Theme: system by default, a three-way switch in the footer (2026-10-20)

_Superseded by D20: the site is dark-only, like the app; the switch, its scripts and `ryft.theme` are gone._

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

## D18 — English only; the German pages are gone (2026-11-05, kept 2026-11-14; amends D8)

The user's decision of 2026-11-05, kept on 2026-11-14 when the restyle it was made for (light
only, Inter; platform #142) was dropped: the site is English only, the same as the app. The
theme switch, the dark appearance and Satoshi stay as they are. German goes because every
page, legal text, string and OG image existed twice. So: `src/app/[locale]`, `messages/de.json`,
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
  character; any other `/de/…` path is a 404 (`tests/nginx.test.ts`). The move to Firebase
  Hosting (C1) must carry the same 301 in `firebase.json` (the C1 spec, platform
  `docs/work/2026-11-14-gcp/design.md` §8, says so).
- The English legal notice carried the VAT and consumer-dispute statements only by linking
  to the German page; they are ported into `content/legal/en/impressum.mdx` as they were.
- `scripts/lib/policy.mjs` checks the English privacy page only. The repository's own guards
  (`scripts/check_policy_in_force.sh`, `scripts/pin_images.sh`) looked for both languages
  and are changed with this (platform D230).
- `content/legal/privacy-versions.json` keeps every version (2026-10-20, 2026-10-26); each
  keeps its English page. The version in force does not change with this.

Would change if the user wants German back: `messages/de.json`, `content/legal/de` and a
`[locale]` segment again, from this PR's parent commit.

## D19 — Firebase Hosting serves the site, not nginx (2026-11-16; C1, platform D233)

The export moves from the nginx image on EKS to Firebase Hosting in the project `ryftlab-site`
(Spark plan, no billing account; platform `docs/work/2026-11-14-gcp/design.md` §8). The pages
are unchanged. nginx's config becomes `firebase.json`:
- The build writes it (`scripts/firebase-json.mjs`, postbuild, after `csp.mjs`). The rules are
  the pure `firebaseJson()` in `scripts/lib/hosting.mjs`, from the same CSP sources as the nginx
  snippets. It is git-ignored, like `nginx/csp.conf`.
- firebase-tools is one exact version, in `.firebase-tools-version` and nowhere else, always
  with npm's install scripts off. `deploy.yml`'s `site` job (once `SITE_DEPLOY` is `on`) runs
  it as `npx --yes firebase-tools@<it>`. The emulator (`npm run e2e:hosting`, `npm run hosting`,
  project `demo-ryft`, no login) installs it once into `node_modules/.cache/firebase-tools-<it>`
  (`npm ci` clears it). There one package gets its install script, `re2` (`npm rebuild re2`),
  and `scripts/hosting.mjs` stops unless `re2` loads. The deploy doesn't serve, so it needs no
  `re2`.
- `scripts/check-hosting.mjs` holds runbook step 10's assertions. It runs against the emulator
  in CI and live after every deploy.

What `firebase.json` says:
- `public: out`, `cleanUrls: true`, `trailingSlash` unset, and no logging keys.
- `ignore: ["**/*.gz"]` and nothing else: `firebase init`'s `**/.*` would drop `.well-known/`.
- Redirects, all 301, in this order:
  1. #156's trailing-slash rule, `^/(?P<p>[A-Za-z0-9][A-Za-z0-9/._-]*)/$` → `/:p`.
  2. `/de` → `/`, an exact `source`, so it never depends on how Hosting fills a capture that did
     not match.
  3. `^/de/(?P<p>…)$` → `/:p`, with the same safe capture (D18).
- One rewrite: the app-link paths go to `/open.html`.
- The headers are written as `regex` only, in RE2's syntax and anchored.
  - The shared set (nginx's `headers-common.conf`, value for value) is on every path.
  - The marketing CSP is on every path except exactly `/admin`. RE2 has no lookaround, so this
    is a complement (`allExcept('admin')`).
  - Cache-Control goes from general to specific.
  - The AASA gets `Content-Type: application/json` and an hour's cache.
  - `/admin` is last, with the admin CSP, `no-store` and `X-Robots-Tag`.
- `tests/hosting.test.ts` pins every rule. For a table of paths, and for every path of up to
  seven characters over a small alphabet, exactly one CSP rule matches and it is the right one.
  The last matching Cache-Control rule is checked per path class, every Location stays on the
  site, and #156's capture tables are kept. `tests/output.test.ts` checks that the built file is
  `firebaseJson()` of the build's hashes, with the same two policies as `nginx/csp*.conf`.

What Hosting's docs leave open: each point is tested against the emulator in CI's `web-e2e`
(`e2e/hosting.spec.ts`, which logs `observed: …` per case) and again live (`check-hosting.mjs`).
Where the emulator and the docs disagree, the emulator wins, but no Location may ever start with
`//` or `/\`. The session that wrote this couldn't run the emulator: its sandbox refused
`npx firebase-tools` and every Node run. **What it observed** comes from CI's `web-e2e` on
#194 (run 43857046841, firebase-tools 15.35.0 with `re2`, so RE2 matching): 195 Playwright tests
passed (5 skipped) and all 34 checks of `check-hosting` passed. Live Hosting is checked again
after every deploy.
- **(a) One CSP per response, the admin one on `/admin` only.** The two CSP rules are disjoint,
  so the result doesn't depend on how Hosting merges rules.
  - Observed: exactly one CSP on every response. The admin policy is on `/admin` only, and the
    marketing one is on `/`, `/privacy`, the 404s, `/administrator`, `/adm`, `/admin.html`'s 301,
    the AASA and the app links.
  - Observed: `/%61dmin` → 200 with the **marketing** CSP. The emulator matches headers against
    the path as sent and finds files by the decoded path, so `admin.html` is served there under
    the stricter policy. Its script is blocked, the page does nothing, and nothing is widened.
    It also lacks `/admin`'s `X-Robots-Tag` and `no-store`. Nothing links that URL, and the page
    is static, with no data in it. Accepted.
- **(b) A `headers` rule sets the AASA's Content-Type.** The file has no extension.
  - Observed: 200, `Content-Type: application/json`, an hour's cache, no redirect.
  - The body is byte-identical to `public/.well-known/apple-app-site-association`.
- **(c) `/de` → `/`, `/de/` → `/de`, `/de/privacy` → `/privacy`.** Observed as expected. Bare
  `/de` is answered by its exact rule, not the capture rule, and `/de/privacy/2026-10-20` →
  `/privacy/2026-10-20`.
  - Observed: **the `redirects` rules' 301s carry no header rules** (0 of 8 security headers,
    no Cache-Control). That covers #156's trailing slash, `/de`, `/de/…` and `/admin/`.
  - Observed: **`cleanUrls`' 301s carry all of them** (`/privacy.html`, `/admin.html`: 8 of 8
    and the path's Cache-Control).
  - nginx sent them on every redirect. A bare 301 carries no content, so it's accepted. The
    tests allow none, fail on two, and `check-hosting` reports the count live.
  - One cost: the hour's Cache-Control meant for the `/de` 301s (D18) doesn't reach them. A
    browser may keep a 301 that has no Cache-Control for good. If German comes back under `/de`,
    visitors who followed one of these redirects may keep landing on the English page until their
    cache clears. Live Hosting may differ; `check-hosting` shows it.
- **(d) `//`, `/\` and `%5C`.** Observed: no Location ever left the site.
  - `/privacy/` → `/privacy`.
  - `/%5Cevil.example/`, `/de/%5Cevil.example`, `/a/%5Cevil.example/`, `/%2F%2Fevil.example/`
    and `/de//evil.example` → 404.
  - `/x//` → 301 `/x/`, then `/x`. The capture may hold `/`, so it isn't the 404 that design §8
    expects. The Location stays on the site, so it's accepted.
  - `//evil.example/` → 404. The emulator doesn't merge slashes; live Hosting may answer 301
    `/evil.example`, which is accepted.
- **(e) Query strings on the redirects.** Observed: kept on every redirect, as nginx did
  (`/de?x=1` → `/?x=1`, `/privacy/?x=1` → `/privacy?x=1`, `/admin.html?x=1` → `/admin?x=1`).
- **(f) One Cache-Control per response.** The Cache-Control rules overlap on purpose. Observed:
  exactly one, the last matching rule's value:
  - `/privacy` `no-cache`;
  - the app links and `/admin` `no-store`;
  - the AASA an hour;
  - `/_next/static/` a year, immutable;
  - the day-long extensions a day;
  - every other path 300 s.

  If live Hosting sends two, `check-hosting` fails ("2 × Cache-Control"), and the classes must
  be made disjoint like the CSP rules.
- **Observed in C1's first CI run: the emulator needs the native `re2`.** Its static server
  (superstatic) loads `re2` as an optional module. Without it, superstatic falls back to
  JavaScript's `RegExp`, which can't parse RE2's `(?P<name>…)`, and the emulator died on the
  first request (ECONNRESET, then nothing listening). So only `re2` gets its install script
  (above). With `re2`, the emulator matches with RE2, as Hosting does.
- Also unchecked:
  - Whether live Hosting adds its own HSTS. If it does, `check-hosting` fails on two HSTS headers
    or on a different value.
  - Whether Location is relative or absolute. The tests accept both, on the same origin only.

Differences from nginx, accepted:
1. There is no `/healthz` (no pod, no probe).
2. A 404 carries its path's headers: 300 s and the marketing CSP, not `404.html`'s `no-cache`.
3. The `redirects` rules' 301s carry no security headers and no Cache-Control; `cleanUrls`' 301s
   carry them all (c).
4. There is no `merge_slashes`: `/x//` takes two hops (`/x/`, then `/x`), on the site.
5. An app-link path with a trailing slash (`/day/`) now goes 301 to `/day` first. nginx served
   `open.html` at once. The redirects run before the rewrite.
6. `/de/<file>.png` and similar now go 301 to `/<file>.png`. nginx's extension location
   answered 404.
7. The day-long cache matches extensions case-sensitively. Only 404s are affected: every file in
   `public/` is lowercase.
8. Hosting compresses. No precompressed copies are uploaded, and `*.gz` is a 404.
9. There is no access log of ours. nginx logged no address, and that test went with
   `tests/nginx.test.ts`. Google's edge sees the visitor's IP. The privacy text's paragraph on
   the site (`docs/LEGAL_CHECKLIST.md` S6) is the user's decision, in C2.
10. Location may be absolute; the tests compare paths on the origin (`sitePath`).
11. Percent-encoded paths may match headers as sent (`/%61dmin`; see (a)).
12. The `/admin` rule is now "exactly one CSP rule matches `/admin`". It was "a location
    includes exactly one header snippet".

The nginx image (`nginx/`, `Dockerfile`, `npm run image`, `make web-image`) stays until the
platform's PR D3 removes it. While it exists, its shared headers and CSPs are checked against
`firebase.json`'s.

Would change if the first emulator or live run disagrees with an expectation above. A doubled
header is fixed by making those rules disjoint. If headers do not reach redirects, browsers
keep the `/de` 301s without D18's one-hour limit. Whether that is acceptable, or the `/de` rules
become 302s, is then the user's decision.

## D20 — Nocturne: dark-only, Satoshi Light, sentence-case labels (2026-11-17; amends D2, D3, D4, D5)

The app's Nocturne PR 1 (app #112, the app's ADR 0016) gives every colour set one value, sets
titles and numbers in Satoshi Light, drops the label style's uppercase and tracking, and makes the
app icon the wordmark (M1). The user decided that the site takes the Nocturne look through the
tokens, with no design change beyond what they carry. `scripts/tokens.mjs` reads the app at the
commit in `web/.app-ref` (platform D234; the first pin is app main at #114), which CI checks out.

What the tokens carry now (old → new):
- **Colours, one value each** (25 sets; `signal` and `on-signal` are new, and nothing on the site
  uses them yet). Background (and Graphite, on-accent) `#0b0f0e` → `#0a0c0b`; surface (and Slate)
  `#1a2226` → `#121514`; surface-raised `#263137` → `#2f312f`; text (and Mist) `#e8e7e3` →
  `#f2f1ec`, secondary 60 → 62 %, tertiary 55 → 48 %; accent (and Teal) `#3fd1b4` → `#63d2b4`;
  sleep `#8f87f1` → `#9b8af0`; strain `#5b8def` → `#5e8bea`. Warning, alert, sage and the stages keep
  their dark values.
- **Type.** Display 56 → 52 pt, number-large 40 → 34, metric 22 → 26, headline 18 → 17, callout
  14 → 15, label 11 → 13, axis 10 → 11, wordmark 17 → 15. Display, number-large, title, metric and
  the wordmark are Light (300; they were Medium), the label is Regular (400; it was Medium). Button
  (16, Medium) and tab (11, Regular) are new.
- **Labels.** `--tracking-label` 0.1818em → 0em, and a new token, `--label-transform: none`
  (`tokens.mjs` reads `ryftLabelStyle`'s own body: `uppercase` if it has `.textCase(.uppercase)`
  again, 0 when it has no `.tracking`). The wordmark keeps 0.32 em, now read from `Wordmark.tracking`.
- **Metrics.** Seven new keys (neutral, glow and hero tile radii, circle button, minimum hit target,
  pill height, item spacing). The six the site uses are unchanged.

`tokens.json` keeps `{light, dark}` per colour (the scripts and tests read `.dark`); the two are
equal. `tokens.css` sets `color-scheme: dark` and has no light block. One would come back only if a
colour set has a light value again, and then `tests/contrast.test.ts` fails ("dark-only"), so the
site gets a decision of its own.

**Dark-only (supersedes D5).** With one palette, a System / Light / Dark switch would switch nothing.
- Removed: the footer's switch (`ThemeSwitch`, its CSS, `theme` and `a11y.theme` in
  `messages/en.json`), both theme scripts and `src/lib/inline-scripts.ts`, `localStorage['ryft.theme']`,
  the screenshots' light `<source>`s, and the light `theme-color`. The one `theme-color` is the
  background token. The footer keeps its three columns; the switch's column is empty.
- So no page but `/admin` runs a script (amends D2), and the marketing CSP tightens from two hashes
  to `script-src 'none'`. The postbuild fails when any page but `/admin` carries a `<script>`;
  `csp.mjs` and `firebase-json.mjs` fail on a marketing hash; `check-hosting.mjs` reads a marketing
  policy that allows a script as "other". The admin page's policy is unchanged.
- Nothing is stored in the browser on the marketing pages. `/admin` keeps its `sessionStorage` (D16).
- The light captures in `public/screenshots/` stay: `scripts/screenshots.mjs` still writes both
  appearances from the app's captures, and no page loads the light ones. Dropping them is a change
  to that script.
- Playwright still runs every page under a dark and a light system setting, and checks that the
  page stays dark under both (`color-scheme`, the background, the screenshot's source).

**Type (amends D3 and D4).**
- The wordmark is Satoshi Light, as the app's `Wordmark` (D3 said Medium).
- Satoshi Light joins Regular, Medium and Bold: the same Fontshare zip and the same ITF FFL
  (self-hosting allowed, the files git-ignored, no subsetting, no conversion), pinned by SHA-256 in
  `config/fonts.json`. Bold stays declared, and no page asks for it.
- **The LCP risk.** Headings and numbers are Light, text is Regular and emphasis Medium, so a page now
  fetches three font files. D4 measured a third file at 1.35 → 1.5 s against the 1.5 s budget
  (Lighthouse, mobile). If Lighthouse fails on it, which weights a page uses is a design decision
  for the user, not a fix inside this change.

**Labels.** Every label takes its case and tracking from the tokens: `Label`, the gauge's and the
Today tiles' labels, table heads, the lock-up's tagline, the styleguide's sample and the admin page's
"admin". The app's rule that a surface-raised fill carries text-primary or text-secondary, never
text-tertiary (its ADR 0016, 2.5), already holds here: the site puts only text-primary on that fill.
`tests/contrast.test.ts` checks primary and secondary on it.

**Icons keep the mark.** The favicon, touch icons and the OG image are drawn from the mark
(`src/lib/mark.ts`, geometry unchanged) in the token colours, and need regenerating for the new
Graphite and Mist (`npm run icons`, `npm run og`; the OG card now takes its weights, the wordmark's
tracking and the label's case from the tokens). Whether they follow the app's new wordmark icon (M1)
is the user's decision.

Would change if the app gets an appearance option again (its ADR 0016 says that needs an ADR of its
own); then the site decides separately whether to follow.
