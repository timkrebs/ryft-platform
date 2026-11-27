-- D284: Indexes for portion identity, the one-default rule and the recent-use lookup.
-- The migrator runs each file in a transaction, so CREATE INDEX CONCURRENTLY is not
-- available; plain CREATE INDEX holds a SHARE lock (blocks writes, not reads) for the
-- build. The sets are small: a few thousand portions, and meal_items is the member diary
-- of a beta cohort. Revisit with a separate maintenance step before the diary is large.
CREATE UNIQUE INDEX IF NOT EXISTS nutrition_portions_source_key
    ON nutrition.portions(food_id,source_id,source_portion_id) WHERE source_portion_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS nutrition_portions_semantic_key
    ON nutrition.portions(food_id,source_id,portion_key) WHERE portion_key IS NOT NULL;
-- At most one default portion per food; clients and CatalogSelections can rely on it.
CREATE UNIQUE INDEX IF NOT EXISTS nutrition_portions_one_default
    ON nutrition.portions(food_id) WHERE is_default;
CREATE INDEX IF NOT EXISTS nutrition_items_food_recent
    ON nutrition.meal_items(user_id,food_id,created_at DESC,id DESC) WHERE food_id IS NOT NULL;
