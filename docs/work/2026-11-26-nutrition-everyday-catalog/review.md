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
