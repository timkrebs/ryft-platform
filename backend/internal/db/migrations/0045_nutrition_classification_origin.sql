-- D267: refresh only ETL-generated classifications. Operator and user rows keep their provenance.
ALTER TABLE nutrition.classifications ADD COLUMN origin text NOT NULL DEFAULT 'manual'
    CHECK (origin IN ('manual', 'python_etl'));

DO $$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ryft_nutrition_etl') THEN
        BEGIN
            CREATE ROLE ryft_nutrition_etl NOLOGIN;
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            -- Roles are cluster-wide. A migration in another database can create it concurrently.
            NULL;
        END;
    END IF;
END $$;

GRANT USAGE ON SCHEMA nutrition TO ryft_nutrition_etl;
GRANT SELECT, INSERT ON nutrition.foods, nutrition.food_sources TO ryft_nutrition_etl;
GRANT UPDATE (canonical_name, brand, quality, markets, completeness, updated_at)
    ON nutrition.foods TO ryft_nutrition_etl;
GRANT UPDATE (dataset_version, source_url, license_identifier, source_updated_at, checksum, imported_at)
    ON nutrition.food_sources TO ryft_nutrition_etl;
GRANT SELECT, INSERT, DELETE ON nutrition.food_names, nutrition.barcodes, nutrition.nutrient_values,
    nutrition.classifications TO ryft_nutrition_etl;

CREATE POLICY etl_foods ON nutrition.foods FOR ALL TO ryft_nutrition_etl
    USING (owner_user_id IS NULL) WITH CHECK (owner_user_id IS NULL);
CREATE POLICY etl_sources ON nutrition.food_sources FOR ALL TO ryft_nutrition_etl
    USING (provider IN ('off', 'usda') AND EXISTS (SELECT FROM nutrition.foods f WHERE f.id = food_sources.food_id));

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['food_names','barcodes','nutrient_values','classifications'] LOOP
        EXECUTE format('CREATE POLICY etl_children ON nutrition.%1$I FOR ALL TO ryft_nutrition_etl
            USING (EXISTS (SELECT FROM nutrition.food_sources s WHERE s.id = %1$I.source_id AND s.food_id = %1$I.food_id)%2$s)',
            t, CASE WHEN t = 'classifications' THEN ' AND origin = ''python_etl''' ELSE '' END);
    END LOOP;
END $$;
