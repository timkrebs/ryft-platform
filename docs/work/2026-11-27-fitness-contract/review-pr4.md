# Review — C1 PR 4, net energy (`feat/net-energy`, D310)

One code-reviewer pass on `origin/feat/sync-fitness-types...HEAD`, 2026-11-27. No blockers.

| # | Severity | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | should-fix | One absurd `basal_energy` sample (ingest checks only `body_mass`'s value) makes the store's integer-range check fail and the whole day unscorable, retried into the DLQ; a negative one inflates `net_kcal` | Scoring treats a resting total outside 0–20 000 kcal as missing (`PlausibleRestingKcal`, pinned). A per-sample ingest bound belongs to PR 3 (#267): noted there as a follow-up |
| 2 | should-fix | A long `/v1/days` range reads every nutrient of every item, and `counts` scans items per date | `NutritionEnergyTotals` has its own query: one pass grouped by date, only `nutrients->'energy_kcal'` |
| 3 | should-fix | A nutrition read failure now fails `/v1/days` and `/v1/days/{day}` (they never touched the nutrition schema) | Degrades: intake unknown (`intake` in `missing`), a fixed warn line without values |
| n1 | nit | `HasData` comment does not mention resting energy | Comment updated |
| n2 | nit | Energy reads share the `daily` metric label | Own label `energy_totals` |
| n3 | nit | A stored day after local today (travel west) is `partial_day: false` with a full estimate | Counted as partial with nothing elapsed |

Checked and sound (reviewer): user scoping, the diary guard (test renames the meals table), existing nutrition routes
unchanged, Henry coefficients, confidence/missing/rounding/DST tests, no health values in logs, migration 0070 and its
grant parity, idempotent rescore, goldens and no model bump, response shape.
