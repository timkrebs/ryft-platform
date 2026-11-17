# Review of #199 (code-reviewer, 2026-11-17)

Verdict: no blockers. No CSP got wider, `/admin`'s policy is unchanged, and no check was weakened (the deleted
assertions are covered by the new "no script at all" checks).

| # | Finding | Rank | Outcome |
|---|---|---|---|
| 1 | OPERATIONS 10a: the policy comment and "Stop if" still said "you and `site-deployer`" while a later paragraph allowed `firebase-adminsdk-fbsvc`; an operator would stop first | should-fix | fixed: both name it, the late paragraph is gone |
| 2 | Stock zsh has `interactivecomments` off: `S=ryftlab-site  # taken? …` fails and leaves `S` unset | should-fix | fixed: step 10's intro and the app-pin block say `setopt interactivecomments` |
| 3 | `Home.module.css` `.tileValue`: a 17 px number in Light; the app sets numbers under 20 pt Regular (`lightFloor`) | should-fix | fixed: body weight, with the reason |
| 4 | `tokens.mjs` passes silently on an unreadable `.tracking`/`.kerning`/`.textCase` in `ryftLabelStyle`, and on a role missing from a cut-short block | should-fix | fixed: throws on both; every declared `RyftTextStyle` case needs a size and a weight. Output byte-identical |
| 5 | `ultraLight`/`thin` CSS weights were inverted (from the lead's brief) | nit | fixed: both 300, as the app draws them (Satoshi-Light) |
| 6 | `screenshots.mjs` comment still described light captures in light mode | nit | fixed |
| 7 | `/support` phone capture: the Profile image card is empty | question | a capture artefact (`complete` isn't paint, and the spec scrolled back up). `visual.spec.ts` now loads every image eagerly and waits for `decode()` and `naturalWidth > 0` |
| 8 | The light screenshot files are deployed, unreferenced | nit | left: harmless, `screenshots.mjs` still writes them |
| 9 | The dark screenshots are pre-Nocturne captures | nit | open (STATUS), needs the app's PR 5 |
| 10 | No "amended by D234" notes on D107 / web D10 | nit | left: D234's heading says what it amends |

Also answered: the footer's empty third column is harmless (named grid areas, one column under 48rem). The brand,
legal-content and no-tracking rules hold. No doc claims the stale icons match the tokens. A trial merge with #196
conflicts only in OPERATIONS 10a.
