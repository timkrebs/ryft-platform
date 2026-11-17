# web: tokens from Nocturne (2026-11-17)

Brief: head-architect's follow-up "fix main, the tokens from Nocturne" (2026-11-16, updated 2026-11-17).
Branch `web/nocturne-tokens` from main `4094d09`. App source: `/Users/tim.krebs/Development/Projects/ryft-app-main`,
detached at `APP_SHA` = `7358f0c99b7a6eb544521e87407b7d2c9bc09556` (app main at #114). Never `../app`.

## Why main is red
`web-checks` checks out `timkrebs/ryft-app` at its main. App PR #112 (Nocturne PR 1, ADR 0016 in the app) changed
`Typography.swift` (`fileprivate var size` → `var size`, Light weights, no label tracking, the wordmark's tracking a
constant), so `tokens.mjs` throws "Typography.swift → size not found". Reproduced locally with
`RYFT_APP_DIR=… make web-tokens`.

## What Nocturne implies for the site (ADR 0016)
- **Dark-only.** Every colour set has one value. The generated tokens have no light palette, so the footer's
  System / Light / Dark switch would switch nothing. The site becomes dark-only like the app (web D20, amends web D5).
- **Type.** New scale (callout 15, metric 26, number-large 34, display 52, headline 17, axis 11, label 13,
  wordmark 15), titles and numbers Light (300), text Regular, emphasis Medium. Labels sentence case, no tracking.
- **Fonts.** Satoshi Light joins Regular, Medium, Bold: the same Fontshare zip, the same ITF FFL (self-hosting
  allowed, files git-ignored, pinned by SHA-256, no subsetting).
- **Icon.** The app icon is now the wordmark (M1). The site's icons are drawn from the mark (`src/lib/mark.ts`), whose
  geometry is unchanged, in the token colours. They're regenerated with `npm run icons` for the new colours and
  aren't redesigned. Whether the site's icons should follow M1 is a question for the user.
- New colours `signal`, `on-signal`; new metrics (neutral/glow/hero tile radii, circle button, hit target, pill,
  item spacing). No design change beyond what the tokens carry.

## Tickets
| # | Ticket | Owner | Acceptance |
|---|---|---|---|
| T1 | Pin the app in CI: `web/.app-ref` = APP_SHA; `web-checks` reads it, refuses anything but 40 hex, checks out `timkrebs/ryft-app` at it. D234; `web/CLAUDE.md` line; OPERATIONS bump procedure | lead | `web-checks` checks out APP_SHA (log); a malformed file fails the step |
| T2 | `tokens.mjs` reads Nocturne's Typography/RyftHeader/Theme; tokens regenerated; idempotent | web-engineer | `make web-tokens` twice → no diff; CI `tokens:check` green |
| T3 | Satoshi Light (300) fetched, pinned, served | web-engineer | `config/fonts.json` pin; `output.test.ts` green |
| T4 | Dark-only: no theme switch, no theme scripts, one `theme-color`, dark screenshots only; CSP follows | web-engineer | web-checks + web-e2e green; axe green |
| T5 | Labels follow the app's label style (case and tracking from the tokens) | web-engineer | no hard-coded `uppercase` on a label |
| T6 | Icons and OG image in the new colours (`npm run icons`, `npm run og`) | web-engineer | regenerated, or the refused command listed |
| T7 | web D20 (amends D3, D4, D5); `web/CLAUDE.md` rules | web-engineer | |
| T8 | OPERATIONS 10a fixes: `ft()` for zsh, the Admin SDK account (expected, no keys, disabled), the 403 after `addfirebase` | lead | |
| T9 | Review | code-reviewer | findings in `review.md`, blockers fixed |

## Risks
- **No local test bed.** `make web-install` needs approval in this headless run, so `node_modules` is missing:
  `make web-test`, `web-build` and `web-e2e` stop at "next: command not found". Only `make web-tokens` (Node
  built-ins) runs, plus `make web-build`'s font fetch. CI is the test bed: plan red rounds.
- **LCP.** A third font file per page (Light + Regular + Medium). Web D4 measured 1.35 → 1.5 s for a third file
  against the 1.5 s budget. If Lighthouse fails, that's a design call (which weights a page uses), not a quick fix.
- **#196 conflicts** in OPERATIONS 10a (it rewrites the step). The T8 edits avoid #196's hunks except 10a's intro.
  Resolution after this merges: keep #196's text and write `ft` where it says `$FT`.
- Touching `ci.yml` runs every CI job (backend tests, smoke) on the PR.

## Rollback
Revert the PR. With the pin, main stays green on a revert too (the revert brings back the old tokens and the
unpinned checkout, which is red again against app main, so a revert must keep T1).

## Needs the user
- Merge (after green CI). With `SITE_DEPLOY` on, the merge deploys the site; while it's off, nothing ships.
- Decide whether the site's favicon and touch icons should follow the app's wordmark icon (M1).
