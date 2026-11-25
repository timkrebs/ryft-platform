-- D284: Stable catalog portions and owner-scoped diary preferences.
ALTER TABLE nutrition.foods
    ADD COLUMN is_canonical_staple boolean NOT NULL DEFAULT false,
    ADD COLUMN canonical_group_key text CHECK (canonical_group_key ~ '^[a-z0-9]+([_-][a-z0-9]+)*$' AND length(canonical_group_key) <= 64),
    ADD COLUMN food_form text CHECK (food_form ~ '^[a-z0-9]+([_-][a-z0-9]+)*$' AND length(food_form) <= 64),
    ADD COLUMN subtitles jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(subtitles) = 'object'
        AND NOT jsonb_path_exists(subtitles,'$.* ? (@.type() != "string")')),
    ADD CONSTRAINT nutrition_staple_public CHECK (NOT is_canonical_staple OR
        (owner_user_id IS NULL AND food_type = 'generic' AND canonical_group_key IS NOT NULL));

ALTER TABLE nutrition.portions
    ADD COLUMN portion_key text CHECK (portion_key ~ '^[a-z0-9]+([_-][a-z0-9]+)*$' AND length(portion_key) <= 64),
    ADD COLUMN short_label_en text CHECK (length(btrim(short_label_en)) BETWEEN 1 AND 200),
    ADD COLUMN short_label_de text CHECK (length(btrim(short_label_de)) BETWEEN 1 AND 200),
    ADD COLUMN is_default boolean NOT NULL DEFAULT false,
    ADD COLUMN sort_order integer NOT NULL DEFAULT 100,
    ADD COLUMN source_portion_id text CHECK (length(btrim(source_portion_id)) BETWEEN 1 AND 200),
    ADD CONSTRAINT nutrition_default_measured CHECK (NOT is_default OR (NOT estimated AND
        (weight_g IS NOT NULL OR volume_ml IS NOT NULL)));
CREATE UNIQUE INDEX nutrition_portions_source_key ON nutrition.portions(food_id,source_id,source_portion_id)
    WHERE source_portion_id IS NOT NULL;
CREATE UNIQUE INDEX nutrition_portions_semantic_key ON nutrition.portions(food_id,source_id,portion_key)
    WHERE portion_key IS NOT NULL;

ALTER TABLE nutrition.food_sources ADD COLUMN attribution text CHECK (length(btrim(attribution)) BETWEEN 1 AND 300),
    DROP CONSTRAINT food_sources_provider_check,
    ADD CONSTRAINT food_sources_provider_check CHECK (provider IN ('off','usda','bls','user','ryft')),
    ADD CONSTRAINT nutrition_bls_attribution CHECK (provider <> 'bls' OR
        (license_identifier = 'CC-BY-4.0' AND attribution IS NOT NULL AND external_id ~ '^[A-Z][0-9]{6}$'));
ALTER POLICY etl_sources ON nutrition.food_sources
    USING (provider IN ('off','usda','bls') AND EXISTS
        (SELECT FROM nutrition.foods f WHERE f.id=food_sources.food_id AND f.owner_user_id IS NULL));
GRANT UPDATE (is_canonical_staple, canonical_group_key, food_form, subtitles, density_g_ml)
    ON nutrition.foods TO ryft_nutrition_etl;
GRANT UPDATE (attribution) ON nutrition.food_sources TO ryft_nutrition_etl;
GRANT UPDATE (label, labels, quantity, unit, weight_g, volume_ml, portion_key, short_label_en,
    short_label_de, is_default, sort_order, source_portion_id) ON nutrition.portions TO ryft_nutrition_etl;
CREATE POLICY etl_portion_update ON nutrition.portions FOR UPDATE TO ryft_nutrition_etl
    USING (origin='python_etl' AND EXISTS
        (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id))
    WITH CHECK (origin='python_etl' AND NOT estimated AND (weight_g IS NOT NULL OR volume_ml IS NOT NULL)
        AND EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id));

ALTER TABLE nutrition.meal_items
    ADD COLUMN portion_label_snapshot text CHECK (length(portion_label_snapshot) BETWEEN 1 AND 200),
    ADD COLUMN portion_short_label_snapshot text CHECK (length(portion_short_label_snapshot) BETWEEN 1 AND 200);

-- Only recover a legacy label when the original ID, food, unit and measured
-- quantity still agree. Never recompute a saved item's nutrient snapshot.
UPDATE nutrition.meal_items i SET portion_label_snapshot=p.label
FROM nutrition.portions p
WHERE i.portion_id=p.id AND i.food_id=p.food_id AND i.unit=p.unit
    AND (p.weight_g IS NOT NULL OR p.volume_ml IS NOT NULL)
    AND (p.weight_g IS NULL OR i.weight_g=round(p.weight_g*i.quantity/p.quantity,6))
    AND (p.volume_ml IS NULL OR i.volume_ml=round(p.volume_ml*i.quantity/p.quantity,6));
CREATE INDEX nutrition_items_food_recent ON nutrition.meal_items(user_id,food_id,created_at DESC,id DESC)
    WHERE food_id IS NOT NULL;

CREATE TABLE nutrition.food_favorites (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    food_id uuid NOT NULL REFERENCES nutrition.foods(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id,food_id)
);
CREATE INDEX nutrition_favorites_food ON nutrition.food_favorites(food_id);
ALTER TABLE nutrition.food_favorites ENABLE ROW LEVEL SECURITY;
GRANT SELECT, INSERT, DELETE ON nutrition.food_favorites TO ryft_api;
CREATE POLICY nutrition_favorite_read ON nutrition.food_favorites FOR SELECT TO ryft_api
    USING (user_id=nutrition.account());
CREATE POLICY nutrition_favorite_insert ON nutrition.food_favorites FOR INSERT TO ryft_api
    WITH CHECK (user_id=nutrition.account() AND EXISTS
        (SELECT FROM nutrition.foods f WHERE f.id=food_favorites.food_id AND f.status='active'));
CREATE POLICY nutrition_favorite_delete ON nutrition.food_favorites FOR DELETE TO ryft_api
    USING (user_id=nutrition.account());
