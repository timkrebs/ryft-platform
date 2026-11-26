-- D284: Stable catalog portions and owner-scoped diary preferences (schema only).
-- Every ALTER is metadata-only: columns carry no inline CHECK and every constraint is
-- NOT VALID, so the ACCESS EXCLUSIVE lock is held for a catalog update, not a table
-- scan. 0056 validates the constraints (SHARE UPDATE EXCLUSIVE), 0057 backfills,
-- 0058 builds the indexes. Rolling back across this release is safe: the new columns
-- and constraints are ignored by older binaries, and the old search functions stay.
ALTER TABLE nutrition.foods
    ADD COLUMN IF NOT EXISTS is_canonical_staple boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS canonical_group_key text,
    ADD COLUMN IF NOT EXISTS food_form text,
    ADD COLUMN IF NOT EXISTS subtitles jsonb NOT NULL DEFAULT '{}',
    ADD CONSTRAINT nutrition_foods_group_key_format CHECK (canonical_group_key ~ '^[a-z0-9]+([_-][a-z0-9]+)*$'
        AND length(canonical_group_key) <= 64) NOT VALID,
    ADD CONSTRAINT nutrition_foods_form_format CHECK (food_form ~ '^[a-z0-9]+([_-][a-z0-9]+)*$'
        AND length(food_form) <= 64) NOT VALID,
    ADD CONSTRAINT nutrition_foods_subtitles_shape CHECK (jsonb_typeof(subtitles) = 'object'
        AND NOT jsonb_path_exists(subtitles,'$.* ? (@.type() != "string")')) NOT VALID,
    ADD CONSTRAINT nutrition_staple_public CHECK (NOT is_canonical_staple OR
        (owner_user_id IS NULL AND food_type = 'generic' AND canonical_group_key IS NOT NULL)) NOT VALID;

ALTER TABLE nutrition.portions
    ADD COLUMN IF NOT EXISTS portion_key text,
    ADD COLUMN IF NOT EXISTS short_label_en text,
    ADD COLUMN IF NOT EXISTS short_label_de text,
    ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS sort_order integer NOT NULL DEFAULT 100,
    ADD COLUMN IF NOT EXISTS source_portion_id text,
    ADD CONSTRAINT nutrition_portions_key_format CHECK (portion_key ~ '^[a-z0-9]+([_-][a-z0-9]+)*$'
        AND length(portion_key) <= 64) NOT VALID,
    ADD CONSTRAINT nutrition_portions_short_en_length CHECK (length(btrim(short_label_en)) BETWEEN 1 AND 200) NOT VALID,
    ADD CONSTRAINT nutrition_portions_short_de_length CHECK (length(btrim(short_label_de)) BETWEEN 1 AND 200) NOT VALID,
    ADD CONSTRAINT nutrition_portions_source_id_length CHECK (length(btrim(source_portion_id)) BETWEEN 1 AND 200) NOT VALID,
    ADD CONSTRAINT nutrition_default_measured CHECK (NOT is_default OR (NOT estimated AND
        (weight_g IS NOT NULL OR volume_ml IS NOT NULL))) NOT VALID;

ALTER TABLE nutrition.food_sources
    ADD COLUMN IF NOT EXISTS attribution text,
    DROP CONSTRAINT food_sources_provider_check,
    ADD CONSTRAINT food_sources_provider_check CHECK (provider IN ('off','usda','bls','user','ryft')) NOT VALID,
    ADD CONSTRAINT nutrition_sources_attribution_length CHECK (length(btrim(attribution)) BETWEEN 1 AND 300) NOT VALID,
    ADD CONSTRAINT nutrition_bls_attribution CHECK (provider <> 'bls' OR
        (license_identifier = 'CC-BY-4.0' AND attribution IS NOT NULL AND external_id ~ '^[A-Z][0-9]{6}$')) NOT VALID;

ALTER POLICY etl_sources ON nutrition.food_sources
    USING (provider IN ('off','usda','bls') AND EXISTS
        (SELECT FROM nutrition.foods f WHERE f.id=food_sources.food_id AND f.owner_user_id IS NULL));
GRANT UPDATE (is_canonical_staple, canonical_group_key, food_form, subtitles, density_g_ml)
    ON nutrition.foods TO ryft_nutrition_etl;
GRANT UPDATE (attribution) ON nutrition.food_sources TO ryft_nutrition_etl;
GRANT UPDATE (label, labels, quantity, unit, weight_g, volume_ml, portion_key, short_label_en,
    short_label_de, is_default, sort_order, source_portion_id) ON nutrition.portions TO ryft_nutrition_etl;
DROP POLICY IF EXISTS etl_portion_update ON nutrition.portions;
CREATE POLICY etl_portion_update ON nutrition.portions FOR UPDATE TO ryft_nutrition_etl
    USING (origin='python_etl' AND EXISTS
        (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id))
    WITH CHECK (origin='python_etl' AND NOT estimated AND (weight_g IS NOT NULL OR volume_ml IS NOT NULL)
        AND EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id));

-- Member favorites. The table is new and empty, so its index costs nothing here.
CREATE TABLE IF NOT EXISTS nutrition.food_favorites (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    food_id uuid NOT NULL REFERENCES nutrition.foods(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id,food_id)
);
CREATE INDEX IF NOT EXISTS nutrition_favorites_food ON nutrition.food_favorites(food_id);
ALTER TABLE nutrition.food_favorites ENABLE ROW LEVEL SECURITY;
GRANT SELECT, INSERT, DELETE ON nutrition.food_favorites TO ryft_api;
DROP POLICY IF EXISTS nutrition_favorite_read ON nutrition.food_favorites;
CREATE POLICY nutrition_favorite_read ON nutrition.food_favorites FOR SELECT TO ryft_api
    USING (user_id=nutrition.account());
DROP POLICY IF EXISTS nutrition_favorite_insert ON nutrition.food_favorites;
CREATE POLICY nutrition_favorite_insert ON nutrition.food_favorites FOR INSERT TO ryft_api
    WITH CHECK (user_id=nutrition.account() AND EXISTS
        (SELECT FROM nutrition.foods f WHERE f.id=food_favorites.food_id AND f.status='active'));
DROP POLICY IF EXISTS nutrition_favorite_delete ON nutrition.food_favorites;
CREATE POLICY nutrition_favorite_delete ON nutrition.food_favorites FOR DELETE TO ryft_api
    USING (user_id=nutrition.account());
