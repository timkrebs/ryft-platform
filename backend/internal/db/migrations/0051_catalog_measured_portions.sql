-- ETL imports only source-backed portions and replaces only its own rows.
ALTER TABLE nutrition.portions ADD COLUMN origin text NOT NULL DEFAULT 'manual'
    CHECK (origin IN ('manual','python_etl'));

GRANT SELECT, INSERT, DELETE ON nutrition.portions TO ryft_nutrition_etl;
CREATE POLICY etl_portion_read ON nutrition.portions FOR SELECT TO ryft_nutrition_etl
    USING (EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id));
CREATE POLICY etl_portion_insert ON nutrition.portions FOR INSERT TO ryft_nutrition_etl
    WITH CHECK (origin='python_etl' AND NOT estimated AND (weight_g IS NOT NULL OR volume_ml IS NOT NULL)
        AND EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id));
CREATE POLICY etl_portion_delete ON nutrition.portions FOR DELETE TO ryft_nutrition_etl
    USING (origin='python_etl'
        AND EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id=portions.source_id AND s.food_id=portions.food_id));
