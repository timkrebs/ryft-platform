# Nutrition ETL follow-ups (portions v2 + BLS), 2026-11-27

Branch `fix/nutrition-etl-followups` from `origin/main` (c76afd7). One PR, D313. No image publish, no job runs.

## Findings (local, before any change)

### 1. BLS: 94 rejected, 20 skipped (execution `nutrition-etl-job-4r7vd`)
A local run of `transform.bls_row_food` over the committed `data/bls_4_0.csv` gives the same numbers: 7,140 rows,
91 `atwater_deviation`, 3 `missing_or_invalid_macro`, 20 skipped.

| Category | Rows | Data right? | Cause | Fix |
|---|---|---|---|---|
| High-fibre vegetables, fruit, mushrooms (Grünkohl, Artischocke, Topinambur, Schalotte, Sellerie, Himbeere, Johannisbeere, Morchel, Austernpilz, …) | 72 | yes | BLS `ENERCC` counts fibre at 2 kcal/g (Regulation (EU) 1169/2011, Annex XIV). Our Atwater sum leaves fibre out, BLS `CHO` is the available carbohydrate (no fibre), so low-energy, fibre-rich foods miss by more than 30 % | BLS Atwater adds fibre × 2 kcal |
| Near-zero drinks (coffee, tea, Nearwater) | 7 | yes | 0–2 kcal against 0.4–1.4 kcal: a relative 30 % of almost nothing; BLS rounds kcal to whole numbers | BLS check passes when the gap is at most 2 kcal |
| Organic acids (Zitronen-/Limettensaft-Konzentrat, Rhabarbersaft, Milch-, Citronen-, Essigsäure) | 6 | yes | energy from organic acids (3 kcal/g); the CSV has no organic-acid column | none: stays rejected (needs `OA` in the extract) |
| Polyols and sweeteners (Sorbit, Xylit, Mannit, zuckerfreie Bonbons/Fruchtgummi, Tafelsüße) | 6 | yes | polyols are in `CHO` but give 2.4 kcal/g; no polyol column | none: stays rejected (needs `POLYL`) |
| Additives without a fat value (Glutamat, Kutterhilfsmittel E 450, Pottasche) | 3 | yes (`-` = no data) | fat unknown | none: rule is right |
| Skipped | 20 | — | the 20 curated twins of USDA staples (D296), by design | none |

Both fixes together recover 79, flip no accepted row, leave 15. BLS only: USDA `carbohydrates_total` already holds
fibre, and OFF is out of scope (follow-up).

### 2. (a) Salatgurke 168409, Naturjoghurt 171284 have no portions
`portions: {}` in `staples.v1.json`. SR Legacy has no single fitting row (US container sizes 4/6/8 oz; a US slicing
cucumber). The USDA release cannot be read from this session (no network, no duckdb), so: preset weights, like
Magerquark's: ½ Salatgurke 200 g (half of a typical 400 g German Salatgurke), 1 Becher Naturjoghurt 150 g (the usual
German single cup). New staple portion kind `preset_grams`. Both the default. `staples` version 2 → 3.

### 3. (b) Foundation: dry run 2026-10-29 (498), write 2026-04-30 (469)
Same code path for both modes; the only branch to an older release is `stage_usda`'s fallback (#240): when the
October ZIP lacks `food.csv`/`food_nutrient.csv` (USDA serves a stale ZIP at that URL), it retries once with a query
parameter and then takes the previous release. The write hit the stale ZIP twice. Fix: never fall back to an older
release; retry the newest with no-cache headers and a fresh URL, then fail the USDA kind with a clear message.

### 4. (c) 3,375 English-only labels (unmapped heads)
Extend `UNITS` with the SR Legacy count words (fruit, item, wedge, stalk, spear, stick, leaf, clove, head, bunch,
sprig, pod, strip, ring, pat, steak, chop, drumstick, thigh, wing, leg, roll, bun, bagel, muffin, cookie, cracker,
tortilla, loaf, package, bag, jar, envelope, tablet, frank, sausage, cubic inch, pinch, glass, sandwich, order) and a
few qualifiers. Add `unmapped_heads` (top 25 remaining head words) to the dry-run line so the next pass is data-driven.
The before/after count needs the user's USDA dry run (no network here).

## Tickets
| # | Owner | Files | Acceptance |
|---|---|---|---|
| T1 BLS energy rule | backend-engineer | `transform.py`, `tests/test_followups.py`, `test_bls.py` | fibre 2 kcal/g + 2 kcal floor for BLS only; `BLS_TRANSFORM_VERSION = "bls2"`; the real CSV gives 7,105 accepted, 15 rejected, 20 skipped (test, runs without deps) |
| T2 presets | backend-engineer | `staples.v1.json`, `transform.py`, tests | `preset_grams` entries; 168409 ½ = 200 g default, 171284 Becher = 150 g default; staples version 3 |
| T3 release | backend-engineer | `download.py`, `test_etl.py` | no older-release fallback; 3 attempts on the newest; failure names the release |
| T4 labels | backend-engineer | `transform.py`, tests | new heads/qualifiers pinned; `unmapped_heads` in the summary |
| T5 docs | lead | README, `DECISIONS.md` D313, `STATUS.md`, PR body | |
| R | code-reviewer | read-only | findings in `review.md` |

## Risks, rollback
- Staples 3 and bls2: the next write reports `updated` once for every USDA and BLS row (README's version rule), then
  `unchanged`. Portion IDs do not move (labels only; presets get new IDs from key + grams).
- A stale USDA ZIP now fails the USDA kind instead of importing old data; a rerun later fixes it.
- Rollback: revert the PR, publish the previous image digest (`sha256:d56ac763…`) in `terraform.tfvars`.

## Needs the user (after merge)
Publish image, pin digest in local `terraform.tfvars`, plan/apply, dry runs, writes (PR body has the commands).
