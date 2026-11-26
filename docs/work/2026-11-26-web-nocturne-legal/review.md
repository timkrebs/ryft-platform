# Review — PR #251 (code-reviewer, one pass, 2026-11-26)

Nothing blocks. The reviewer opened all 8 distinct legal-page images under `shots/` and could read no personal details in any of them. AASA `/journal/*`, CSP,
storage, dark-only, fonts, tokens, budgets and tests were checked and are sound.

| # | Sev | Where | Finding | Fix | State |
|---|---|---|---|---|---|
| A1 | should | `web/src/components/insight.module.css:18` | On the glow card the severity word is text-secondary, about 4.1:1; the app uses ink (`InsightTile.swift:44`). `contrast.test.ts` doesn't check glow tiles | `.glowWord { color: var(--text-primary) }` | fixed |
| A2 | should | `instruments.module.css:35, 44-52`; `Home.module.css:351-367`; `ui.module.css:136` | Opacities sit outside `nocturne.css`, so nothing tests them against the app | Move them into `nocturne.css` with test rows | open, follow-up |
| A3 | should | `e2e/visual.spec.ts:76` | The mask is chosen by pattern, and nothing checks that it hit | Mask the whole first section on `/terms` and `/privacy*`, and check that at least one element was masked | open, follow-up |
| N | nit | `nocturne.test.ts:835, 838, 851`; `nocturne.css:20` (an unused variable); `Home.module.css:95` (tracking); `admin.css:56, 158, 182` (teal); NOCTURNE P1 "byte-identical" (untested) | — | — | open, follow-up |
| D | follow-up | `Screenshot.tsx` (move `DeviceFrame` out), `src/data/screenshots.json`, `scripts/screenshots.mjs`, `public/screenshots/` (70 files, deployed, nothing links to them) | Dead code | A cleanup PR | open |
| X | outside this PR | The live privacy 2026-10-26 names AWS and browser storage | — | PR #252 (privacy 2026-11-26) | in #252 |
