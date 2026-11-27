# Review: the BLS data PR (`data/bls-4-0`), 2026-11-27

Reviewer: code-reviewer, read-only, on commits e68c290 and 3f199b1. No blockers.

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | should-fix | `transform.py` proposes twin candidates only from the name's first comma segment. BLS 4.0 adds the state after a space (`Banane roh` F503100, `Apfel roh` F110100, `Hühnerei roh` E111100, `Hühnerei gekocht` E111132, `Tomate roh` G561100, `Avocado roh` F502100, `Hafer Flocken` C133000, `Reis poliert, gekocht` C352032), so obvious twins were never proposed. The merge never retires rows, so this must be settled before the write. | fixed: `twin_keys` (whole name, first segment, without state words, without spaces), with a test. A search found 5 more twins. 20 twins in all, the unsure ones listed in D296. |
| 2 | should-fix | `--no-bls-csv` no longer does "exactly what it did before" (`run_etl.py:134`, D296 Import). The attribution changes the checksum under version `staples2`, which gives 3 `conflict`. After the full write it would be `updated` and replace the staples' CSV nutrients with the curated subset. | fixed: `refuse_staples_over_full_bls` (read-only, only with `--write`), with a test; the comment and D296 are corrected |
| 3 | nit | `scripts/etl/README.md`: an empty cell or `-` in a required component doesn't stop the converter; the ETL rejects the row later. | fixed |
| 4 | nit | D296: after markers become 0, "measured 0" and "below the limit" can't be told apart. | fixed |
| 5 | nit | X/Y codes (about 2,000 dishes) have no `BLS_MAIN_GROUPS` hint. This predates the branch. | noted in D296 |

Checked and sound: the attribution length (≈153 ≤ 300) and the DOI `source_url`; the code pattern is identical in the converter, transform and 0062; nothing else assumes 7 digits; the uuid5 IDs; the 0062/0063 split; `migrate_nutrition_test.go` logic; the marker counting and the thousands-check bypass; the colon in header codes (no new ambiguity); the three twins; the counts; a CSV spot check.
