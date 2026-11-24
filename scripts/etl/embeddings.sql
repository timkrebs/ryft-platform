-- Optional and applied explicitly after the Platform migrations. Requires an installed pgvector extension.
BEGIN;
CREATE EXTENSION IF NOT EXISTS vector;
DO $$
DECLARE extension_schema text;
BEGIN
    SELECT n.nspname INTO STRICT extension_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector';

    EXECUTE format('GRANT USAGE ON SCHEMA %I TO ryft_api, ryft_nutrition_etl', extension_schema);
    EXECUTE format('CREATE TABLE IF NOT EXISTS nutrition.food_embeddings (
        food_id uuid PRIMARY KEY REFERENCES nutrition.foods(id) ON DELETE CASCADE,
        model text NOT NULL,
        input_hash bytea NOT NULL,
        embedding %I.vector(768) NOT NULL,
        updated_at timestamptz NOT NULL DEFAULT now()
    )', extension_schema);
    EXECUTE format('CREATE INDEX IF NOT EXISTS nutrition_food_embeddings_cosine
        ON nutrition.food_embeddings USING hnsw (embedding %I.vector_cosine_ops)', extension_schema);
END $$;
ALTER TABLE nutrition.food_embeddings ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON nutrition.food_embeddings FROM PUBLIC;
GRANT SELECT ON nutrition.food_embeddings TO ryft_api;
GRANT SELECT, INSERT, UPDATE ON nutrition.food_embeddings TO ryft_nutrition_etl;
DROP POLICY IF EXISTS public_catalog_embeddings ON nutrition.food_embeddings;
CREATE POLICY public_catalog_embeddings ON nutrition.food_embeddings FOR SELECT TO ryft_api USING (
    EXISTS (
        SELECT FROM nutrition.foods f WHERE f.id=food_embeddings.food_id
            AND f.owner_user_id IS NULL AND f.status='active'
            AND food_embeddings.input_hash = sha256(convert_to(concat_ws(E'\n', f.canonical_name, nullif(f.brand, '')), 'UTF8'))
    )
);
DROP POLICY IF EXISTS etl_embeddings ON nutrition.food_embeddings;
CREATE POLICY etl_embeddings ON nutrition.food_embeddings FOR ALL TO ryft_nutrition_etl USING (
    EXISTS (SELECT FROM nutrition.foods f WHERE f.id = food_embeddings.food_id AND f.owner_user_id IS NULL)
);
COMMIT;
