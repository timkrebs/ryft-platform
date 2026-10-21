# Status — ryftapp.de (web/)

As of 2026-10-20. Eight commits on `main`, in the order of the brief, at
`github.com/timkrebs/ryft-web` (private).

## Done, and how it was checked

| Area | State | Checked by |
| --- | --- | --- |
| Pages | Landing, science, beta, privacy (+ `/privacy/2026-10-20`), terms (+ version), impressum, support, licences — each in English and German; `/open` (app-link fallback), 404, `/styleguide` | 24 static pages; Playwright on every page and language |
| App URLs | `/privacy`, `/terms`, `/support`, `/impressum`, `/de/impressum`, `/auth/callback`, `/day/*`, `/sleep/*`, `/insights/*`, `/train/*` all 200 from the nginx image | `e2e/routes.spec.ts` against the image |
| AASA | Byte-identical to the platform's, `application/json`, no redirect, covers every URL `AppRoute.url` creates | `tests/aasa.test.ts`, `e2e/routes.spec.ts` |
| Brand rule | 0 hard findings in the repository, the built site and commit messages; pre-commit and commit-msg hooks; CI | `npm run brand`, `tests/brand.test.ts` (48 cases) |
| Brand report | `docs/BRAND_CHECK.md`: nothing user-facing in app/ or plattform/; 48 internal hits | `npm run brand:report` |
| Tokens | 23 colours, six metrics, three durations, the type scale, from `../app/Ryft` | `npm run tokens:check`; WCAG contrast of every text pair in both themes (`tests/contrast.test.ts`) |
| Legal | The platform's drafts ported to MDX, every `TODO(user)` kept, draft banner, `noindex`; versioned privacy/terms | `tests/legal.test.ts`: the text of each port equals its platform draft word for word |
| Privacy version | `2026-10-20` on the site and in the platform's api and ingest ConfigMaps | `npm run check:policy` |
| Science | Model `2026.10.1`, formulas from `SCORING.md`, `ARCHITECTURE.md` and the scoring code | `tests/science.test.ts` fails when SCORING.md's version moves |
| Licences | 7 Swift packages at their pinned revisions + Satoshi; 166 npm production packages + Satoshi; full texts | `npm run licenses:check`, `tests/output.test.ts` |
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

**First CI run** on GitHub (`timkrebs/ryft-web`, 2026-10-20, run 39426279137): Checks,
Secrets (gitleaks), and in the E2E job Trivy, all Playwright and axe tests and the link
check passed. **Lighthouse failed on Linux**: CLS 0.17 on `/`, 0.19 on `/privacy`, 0.06 on
`/science` (performance 0.93 and 0.91 as a result), because the font fallback only bound to
Arial (D13). Fixed on `fix/font-fallback-cls`; its CI run is the proof, and the image job
runs after that merges.

## Open

- **Repository, protection, variables, image visibility**: your decisions (HANDOFF.md → 1).
- **Platform switch to the image**: HANDOFF.md → 2, after the first green `main`.
- **Legal texts**: drafts until you fill the `TODO(user)`s and publish (HANDOFF.md → 1).
- **Support address and TestFlight link**: repository variables; until then the pages show
  `TODO(user)` where the address goes and hide the TestFlight button.
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
