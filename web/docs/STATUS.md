# Status — ryftapp.de (web/)

As of 2026-10-21. **Live on `https://ryftapp.de` since 2026-10-21, 20:32 CEST**: the
platform's `2cc55ef` (ryft-platform #3) runs `ghcr.io/timkrebs/ryft/web:f437747` (public,
D14) instead of the ConfigMap site. `main` at `github.com/timkrebs/ryft-web` (public).

## Live, and how it was checked (2026-10-21, against production)

| Check | Result |
| --- | --- |
| Routes | 200 `text/html` on `/`, `/de`, `/science`, `/beta`, `/privacy`, `/terms`, `/support`, `/impressum`, `/de/privacy`, `/de/impressum`, `/privacy/2026-10-20`, `/terms/2026-10-20`, `/licenses`, `/day/…`, `/auth/callback`, `/invite/…`, `/train/history` |
| AASA | 200 `application/json`, no redirect; `/day/*` … `/invite/*`, `/auth/*` |
| Redirects | `www.ryftapp.de/` → 308 `https://ryftapp.de/`; `/privacy/` → 301 `/privacy` |
| Headers | This image's CSP (two script hashes, Trusted Types), HSTS, `no-referrer`, `nosniff` |
| Legal pages | Drafts: banner and `noindex`, as intended until you publish |
| Lighthouse (mobile, 3 runs per page) | Every assertion of `lighthouserc.json` passed. Performance, accessibility, best practices 100 on `/`, `/de`, `/science`, `/beta`, `/privacy`; LCP 0.77–0.90 s; CLS ≤ 0.002; TBT 0; SEO 92 (`/privacy` 58: `noindex`) |
| Links | 249 (89 on ryftapp.de, 160 external), all OK (`linkinator` from production) |
| Image | Anonymous pulls of `f437747` and `abfbe26` answer 200 |

Not checked from here: the pods and the access log in the cluster (this shell's AWS session
had expired).

## Done, and how it was checked

| Area | State | Checked by |
| --- | --- | --- |
| Pages | Landing, science, beta, privacy (+ `/privacy/2026-10-20`), terms (+ version), impressum, support, licences — each in English and German; `/open` (app-link fallback), 404, `/styleguide` | 24 static pages; Playwright on every page and language |
| App URLs | `/privacy`, `/terms`, `/support`, `/impressum`, `/de/impressum`, `/auth/callback`, `/day/*`, `/sleep/*`, `/insights/*`, `/train/*`, `/invite/*` all 200 from the nginx image | `e2e/routes.spec.ts` against the image |
| AASA | Everything the platform's copy covers, plus `/invite/*` (the app's, 2026-10-21); `application/json`, no redirect, covers every URL `AppRoute.url` creates | `tests/aasa.test.ts`, `e2e/routes.spec.ts` |
| Brand rule | 0 hard findings in the repository, the built site and commit messages; pre-commit and commit-msg hooks; CI | `npm run brand`, `tests/brand.test.ts` (48 cases) |
| Brand report | `docs/BRAND_CHECK.md`: nothing user-facing in app/ or plattform/; 48 internal hits | `npm run brand:report` |
| Tokens | 23 colours, six metrics, three durations, the type scale, from `../app/Ryft` | `npm run tokens:check`; WCAG contrast of every text pair in both themes (`tests/contrast.test.ts`) |
| Legal | The platform's drafts ported to MDX, every `TODO(user)` kept, draft banner, `noindex`; versioned privacy/terms | `tests/legal.test.ts`: the text of each port equals its platform draft word for word |
| Privacy version | `2026-10-20` on the site and in the platform's api and ingest ConfigMaps | `npm run check:policy` |
| Science | Model `2026.10.1`, formulas from `SCORING.md`, `ARCHITECTURE.md` and the scoring code | `tests/science.test.ts` fails when SCORING.md's version moves |
| Licences | 8 Swift packages at their pinned revisions + Satoshi; 166 npm production packages + Satoshi; full texts | `npm run licenses:check`, `tests/output.test.ts` |
| JavaScript | 1.4 KB of inline JS on every page (theme), no framework runtime (D2) | `tests/output.test.ts` (< 80 KB), Lighthouse `resource-summary:script` |
| Performance | Lighthouse mobile, simulated throttling: LCP 1.35 s on `/`, `/de`, `/science`, `/beta`, `/privacy`; CLS ≤ 0.002; TBT 0 | `lighthouserc.json`, 3 runs per page |
| Lighthouse scores | Performance 100, Accessibility 100, Best Practices 100, SEO 92 (indexable pages; see below) | same |
| Accessibility | axe: 0 serious or critical on 22 pages × dark/light × desktop/phone | `e2e/site.spec.ts` (88 tests) |
| Links | 246 links, internal and external, all reachable | `npm run links` |
| Image | nginx-unprivileged 1.30.6, uid 101, read-only root FS; Trivy: 0 high or critical | `npm run image && npm run serve`, Trivy 0.76.0 locally |
| Secrets | gitleaks: no leaks in history | gitleaks 8.30.1 locally |
| CSP | Two script hashes, no `unsafe-inline`, Trusted Types; no violations on any page | `tests/csp.test.ts`, console checks in `e2e/site.spec.ts` |

Locally, every step CI runs passed on 2026-10-20 (macOS, Podman 6.1, Chrome for
Lighthouse); the Checks job was also replayed from a clean copy of the tracked files.

**CI on GitHub.** The first run (2026-10-20, run 39426279137) failed Lighthouse on Linux:
CLS 0.17 on `/`, 0.19 on `/privacy`, 0.06 on `/science`, because the font fallback bound only
to Arial (D13). PR #6 fixed it, and also caught up with the app, whose changes of 2026-10-21
failed the drift checks (the Swift licence bump in ryft-app #7; `/invite/*` in the AASA).
**First green `main`**: run 39624344892 on `abfbe26` (2026-10-21), all four jobs; Lighthouse
within every assertion on all 15 runs (5 pages × 3). The image job pushed
`ghcr.io/timkrebs/ryft/web:abfbe26` and `:latest` (amd64 and arm64, provenance, SBOM; Trivy
on the pushed image: no critical). ghcr.io refuses an anonymous pull token for it (401), so
the package is not public.

## Open

- **Done on 2026-10-21:** the repository is public and `main` is protected (ruleset: PRs,
  squash only, the four checks required); `SUPPORT_EMAIL` = `support@ryftapp.de`;
  `TESTFLIGHT_URL` stays unset (invite-only beta). HANDOFF.md → 1.
- **Merged:** #9 (an access log that identifies no visitor), as `f437747`; live.
- **In review:** #8 (these docs), #10 (your
  legal answers, both languages, still drafts), #11 (the promote job; Lighthouse reports
  uploaded). The ruleset wants each one up to date with `main` before it merges.
- **Mail for ryftapp.de**: no MX record yet, so mail to `support@ryftapp.de` bounces; later,
  by your choice (HANDOFF.md → 1).
- **Grafana Cloud**: exclude `ryft-.*` from pod-log collection (HANDOFF.md → 1); the privacy
  text says metrics only.
- **Web releases**: a one-line pin PR in ryft-platform (web's `newTag`) until the promote job
  (#11) has the App (HANDOFF.md → 1).
- **Legal texts**: drafts until every `TODO(user)` is resolved and you say "publish";
  `docs/LEGAL_CHECKLIST.md` → 0a lists what is answered and what is open. Your name and
  address stay off the site until there is a non-personal option (→ 0).
- **Local `main` was rewritten after the push** (`git filter-branch` removed the commits'
  `Co-Authored-By` lines; the trees are identical). `origin/main` is the history; a local
  `main` built on the rewrite shares no commit with it.
- **Captures from the app** (HANDOFF.md → 3): a clean Today screen, the journal and
  Profile → Data. Until then the hero draws Today in SVG and the journal shows a sample
  card, both labelled "Sample data".
- **SEO 92, not 100**: Lighthouse fetches `robots.txt` from inside the page, and the CSP's
  `connect-src 'none'` blocks that. Crawlers fetch it directly (it's served, 200).
- **No brotli** (D7).
- **German state words** on /science (ausgeglichen, angespannt, erschöpft) are translations
  of the API's `balanced`, `strained`, `depleted`; the app doesn't show these words in
  German yet. Align them when it does.

## Screenshots

Every page's first screen at 1280 × 900, from the nginx image (`node scripts/status-shots.mjs`).

### The app next to the site

The app's own capture (left) and the site (right) in the same appearance: the same
Graphite/Mist grounds, Slate/off-white surfaces, Teal accent, type and mark.

![App and site, dark](screenshots/compare-dark.jpg)
![App and site, light](screenshots/compare-light.jpg)

### Pages

| Page | Dark | Light |
| --- | --- | --- |
| `/` | ![](screenshots/home-dark.jpg) | ![](screenshots/home-light.jpg) |
| `/` whole page | [dark](screenshots/home-full-dark.jpg) | [light](screenshots/home-full-light.jpg) |
| `/` on a phone | ![](screenshots/home-phone-dark.jpg) | ![](screenshots/home-phone-light.jpg) |
| `/de` | ![](screenshots/de-home-dark.jpg) | ![](screenshots/de-home-light.jpg) |
| `/science` | ![](screenshots/science-dark.jpg) | ![](screenshots/science-light.jpg) |
| `/beta` | ![](screenshots/beta-dark.jpg) | ![](screenshots/beta-light.jpg) |
| `/privacy` | ![](screenshots/privacy-dark.jpg) | ![](screenshots/privacy-light.jpg) |
| `/terms` | ![](screenshots/terms-dark.jpg) | ![](screenshots/terms-light.jpg) |
| `/impressum` | ![](screenshots/impressum-dark.jpg) | ![](screenshots/impressum-light.jpg) |
| `/support` | ![](screenshots/support-dark.jpg) | ![](screenshots/support-light.jpg) |
| `/licenses` | ![](screenshots/licenses-dark.jpg) | ![](screenshots/licenses-light.jpg) |
| `/auth/callback` (app-link fallback) | ![](screenshots/auth-callback-dark.jpg) | ![](screenshots/auth-callback-light.jpg) |
| `/open` | ![](screenshots/open-dark.jpg) | ![](screenshots/open-light.jpg) |
| 404 | ![](screenshots/nope-dark.jpg) | ![](screenshots/nope-light.jpg) |
| `/styleguide` | ![](screenshots/styleguide-dark.jpg) | ![](screenshots/styleguide-light.jpg) |

CI attaches full-page screenshots of every page in both languages, themes and viewports to
the Playwright report (artifact `playwright-report`).
