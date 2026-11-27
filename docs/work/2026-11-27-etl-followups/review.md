# Review log, ETL follow-ups

## Implementation hand-back (backend-engineer, 2026-11-27)
- T1: baseline 7,140 / 91 atwater_deviation / 3 missing_or_invalid_macro / 20 skipped (matches plan). After bls2:
  7,105 accepted, 15 rejected (12 atwater, 3 missing macro), 20 skipped. Recovered 79: 67 fibre term only, 7 floor
  only, 5 either. No accepted row lost. Left: 6 organic acids, 6 polyols/sweeteners, 3 additives without fat.
- T2: new kind `preset_grams`; 168409 `half_200_g` 200 g default; 171284 `pot_150_g` 150 g default; staples 3.
- T3: newest release only, 3 attempts, fresh `ryft_retry` + no-cache headers; raise names release + attempts.
- T4: 40 heads + plurals, 11 qualifiers; `unmapped_heads` list (top 25) in the USDA summary. Before/after not
  measurable locally (no USDA sample, no network).
- Not run by the engineer: test_etl.py, test_bls.py (no httpx/duckdb/psycopg locally). Open: infra/gcp/README.md:174
  says staples version 2.

## code-reviewer, pass 1 (2026-11-27)
1. BLOCKER test_etl.py:1006-1007 expects `1 bag` English-only; `bag` now maps to Beutel (DB test, runs in CI).
2. SHOULD scripts/etl/README.md staples paragraph and infra/gcp/README.md:77 say every row becomes `updated`;
   checksum excludes version, so unchanged content stays `unchanged`.
3. SHOULD D313/STATUS: merge never retires sources missing from a batch; replaced Foundation FDC IDs from
   2026-04-30 stay searchable.
4. NIT transform.py:653 `preset_grams` accepts "200" strings; only 0 tested; half_of + preset_grams both apply.
5. NIT the 67/7/5 split comes from a test that checks the floor without fibre; the rule checks it with fibre.
6. NIT German: Stick→Stange, Oberschenkel→Oberkeule (poultry), "2 Bunde"→"2 Bund", frozen→tiefgekühlt,
   frankfurter relabelled "frank", cubic_inch German labels unused.
7. NIT preset order 10 equals "small"; the default sorts after it.
8. NIT `--source all`: a Foundation failure now aborts SR Legacy and BLS after OFF wrote; stale "3 conflict"
   comment run_etl.py:150-152.
Expected outcomes confirmed: BLS 79 inserted / 7,026 unchanged / 0 conflict. USDA staples 3: 168409, 171284 and
newly mapped foods updated, rest unchanged, 0 conflict. Foundation: same ID new content updated, new IDs inserted
(≥29 net), conflict only on older/missing publication date; retired IDs stay.
Disposition: 1, 2, 4, 6, 7, 8-comment fixed by backend-engineer; 3 documented; 5 split recomputed by the rule;
8 behaviour accepted (fail closed, rerun), noted in the PR.
