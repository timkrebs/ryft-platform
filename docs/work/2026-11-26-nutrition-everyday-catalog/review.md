# Review — nutrition everyday catalog (feat/nutrition-everyday-catalog)

code-reviewer, one pass, 2026-11-26, on `3e38ef7..52f4849`. Postgres-backed tests were not run in the pass
(no `RYFT_TEST_POSTGRES_URL` locally). No blockers.

## Should fix (open, owner: backend-engineer / database-engineer)
1. **Favourites missing from export and merge guard.** `store/nutrition_export.go:16` exports meals only;
   `HasNutritionData` (`store/nutrition_catalog.go:52-60`) ignores `nutrition.food_favorites`. A new user table
   outside the export; an admin merge with the diary flag off silently drops favourites. Fix: additive export field
   + changelog, `OR EXISTS (… food_favorites …)` in the guard. API.md now says favourites are not in the export yet.
2. **Group derivatives fall below unrelated brand prefixes.** `0054_rank_nutrition_catalog.sql:184` puts non-staple
   members of a staple's group in tier 1; branded `Apfelmus…`/`Apfelstrudel…` prefixes are tier 4, so juice can drop
   off page 1 in a full catalog. Fix: a middle tier (e.g. 8) or keep the match tier; test with a branded fixture.
3. **One default per food not enforced.** No constraint in 0053; `CatalogSelections` picks the first by order.
   Fix: `CREATE UNIQUE INDEX … ON nutrition.portions(food_id) WHERE is_default;` (a new migration). API.md now
   describes the actual behaviour.

## Before merge
4. **0053 lock time.** Column CHECKs and three `ADD CONSTRAINT`s scan `foods`, `food_sources`, `portions` under
   ACCESS EXCLUSIVE; two unique indexes and `nutrition_items_food_recent` are built without CONCURRENTLY; backfill
   UPDATE in the same transaction. Negligible at ~10k rows; confirm production row counts, else `NOT VALID` +
   `VALIDATE` as in 0028.

## Nits
5. `nutrition/selection.go` `sameSavedMeasure` compares floats exactly (correct while both paths round to 6).
6. `0054:171-176` `recent` CTE aggregates all the user's items per keystroke; restrict to candidate food IDs.
7. `canonical_group_key IN ($1,$6)` never matches multi-word keys (`white_rice` vs `white rice`).
8. `db/migrate_nutrition_test.go`: a failed INSERT leaves `SET ROLE ryft_nutrition_etl` on a pooled connection.
9. `last_used_selection` duplicates `last_used_portion` on detail.

## Verified sound
Backfill formula matches writes; `item_id`-only PATCH keeps snapshots; request hash unchanged for old builds
(`language` omitempty); favourites RLS owner-only, cascade on user/food deletion; favourite routes idempotent,
404 on invisible food; ETL grants/policy cover the upsert; legacy portion IDs reused; `search_foods_v2` SECURITY
DEFINER with fixed `search_path`, old `search_foods` kept for rollback; contract additive and API.md matches code.

# Follow-up 2026-11-26: Codex findings and the two-step release

Codex (read-only) on `3e38ef7..5494c24`. Each finding was verified against the code first; all five held:
1. **Rollback erases snapshots.** Confirmed: `store/nutrition_meals.go:271-275` deleted every item and reinserted
   it typed. Fixed in step 1 (#249, D285); test `TestNutritionPatchCopiesRetainedItemsAsStored`.
2. **A refresh changes a reviewed weight under the same ID.** Confirmed: the ID was `uuid5(ns, "source-portion:"+key)`
   (`import_pg.py:111`) and the merge matched on `source_portion_id` alone. Fixed: the measure is part of the ID, and
   the merge matches only the same measure. ETL tests cover 110 → 112 g, a moving default and an identical rerun.
3. **Personalization outranks an exact variant.** Confirmed: `n.normalized IN ($1,$6)` made an exact alias "broad".
   Fixed in 0059; `store/nutrition_ranking_test.go` cases a–d.
4. **A merge deletes favourites-only data.** Confirmed: `HasNutritionData` skipped `food_favorites`. Fixed; store and
   admin-merge tests.
5. **0053 holds ACCESS EXCLUSIVE through the backfill and index builds.** Confirmed. Split into 0055–0059.
   `CONCURRENTLY` is impossible because the migrator runs each file in a transaction (`internal/db/db.go` ~177).

## code-reviewer pass on the new diffs (#249 `origin/main..HEAD`, #248 `1aee34a..e1467ce`)
#249 is sound (exact jsonb round-trip for every column type, PK/unique safe, RLS unchanged, the only rewrite path).
- **Blocker:** `cmd/api/nutrition_favorites_test.go:58-64` reads the raw `enabled` column; `nutrition.enabled` is
  `enabled=true, rollout_pct=0` from 0043, so the test always stops. Use `flags.Enabled`.
- **Should fix:** `model/nutrition.go:280` `favorites,omitempty` drops `[]` on a first page; use `omitzero` and test it.
- **Should fix:** `0059:45-46`: a single-word exact variant ("Vollmilch", "Fettarme") still promotes the group, so a
  favourite outranks the exact match. Prefix rule only when no staple matches exactly; test "Vollmilch".
- **Should fix:** `merge_catalog.sql` clears defaults per (source, food), but the one-default index is per food: two
  sources on one food, or a manual default, fail the batch. Clear per food, keep one winner, test two sources.
- Nits: 0055 is half re-runnable (dev databases that ran the earlier 0053/0054 must be reset); `rows.Close()` on a
  scan error in PATCH; the upgrade test starts at 052; a rollback to #249 drops favourites from the merge guard
  (note in OPERATIONS).
