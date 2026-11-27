# Review: nutrition portions v2 (code-reviewer, 2026-11-26, uncommitted diff of scripts/etl)

The reviewer desk-checked the tests (no local Python) and found **no blockers**.

## Should fix
1. `merge_catalog.sql` upsert vs unique `nutrition_portions_semantic_key`: a `portion_key` moving between surviving
   rows (a staple `match` edit or a release rename) collides mid-statement, and the write aborts. Fix: clear moving keys
   before the upsert, the same way as the default-clearing step.
2. `transform.py` label growth: a cleaned label can exceed 200 characters (`cup` → `US cup`, longer German qualifiers).
   That breaks the 0043 CHECK and Go's `ValidPortion`. Fix: fall back if any label is over 200.
3. `transform.py` imperial marks outside parentheses (`1 cup 1/2" pieces`) survive, so `raw_marker` isn't 0 as the
   README promises. Fix: strip them, or drop the promise.
4. No D-entry yet. Lead: D288.

## Nits
- `half_of` grams with 7 decimals: round to 6 (`numeric(16,6)`).
- A tail without a comma still gets one (`1 medium, stalk`).
- Amount `0` renders as `1`.
- `IMPERIAL` also drops non-measure parentheticals such as `(long grain)`.
- A Go manual twin with the raw text now suppresses a new curated row on a food's first import. This only affects
  foods with no `python_etl` rows.
- Avocado `text: ["avocado","fruit"]` can match two rows → unmatched → no default. Check the dry run's `unmatched` and
  `description_mismatch`.

## Checked sound
ID stability (keyed, legacy, `prior`, `half` key-based ID), checksum determinism, version bump → `updated` once then
`unchanged`, curated-only defaults, NULL `legacy_label`, dry-run stats don't affect writes, all 42 SR rows and the
staple tests traced.

## Resolution (lead, inline)
1. Fixed: `merge_catalog.sql` releases a moving `portion_key` before the upsert. New PG test
   `test_curated_key_moves_between_surviving_portions`.
2. Fixed: `clean_portion` falls back if a label is over 200 (`LABEL_LIMIT`): en → cleaned text or grams, shorts or de →
   None. Test in `test_inch_marks_outside_parentheses_and_label_limits`.
3. Fixed: `INCH_MARK` strips `N"` and `dia` outside parentheses. Same test.
4. D288 written.
- Nit, fixed: `half_of` is rounded to 6 decimals.
- Nit, improved: size words as qualifiers (`slice, medium` → `1 Scheibe, mittel`). SR_TABLE row and map pin updated.
- Nit, added: the dry run now reports `staple_portions` (source grams per curated portion).
- Accepted, left as is: the comma join for a space tail, amount 0 → 1, a non-measure parenthetical with a measure
  word such as `(long grain)` dropped, and the manual twin winning on a food's first import.
