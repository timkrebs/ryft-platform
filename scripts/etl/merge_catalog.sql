-- The Go importer takes the same transaction lock. This snapshot contains at most one candidate per source ID.
CREATE TEMP TABLE etl_merge ON COMMIT DROP AS
WITH candidates AS (
    SELECT DISTINCT ON (provider, external_id) * FROM etl_batch
    ORDER BY provider, external_id, source_updated_at DESC NULLS LAST, checksum, ordinal
)
SELECT c.*, coalesce(s.food_id, c.stable_id) AS food_id, coalesce(s.id, c.stable_id) AS source_id,
    CASE
        WHEN (s.id IS NOT NULL AND f.owner_user_id IS NOT NULL)
            OR (s.id IS NULL AND occupied.id IS NOT NULL) THEN 'conflict'
        WHEN s.id IS NULL THEN 'inserted'
        WHEN s.checksum = c.checksum THEN 'unchanged'
        WHEN s.dataset_version = c.version
            OR (s.source_updated_at IS NOT NULL AND (c.source_updated_at IS NULL OR c.source_updated_at < s.source_updated_at)) THEN 'conflict'
        ELSE 'updated'
    END AS outcome
FROM candidates c
LEFT JOIN nutrition.food_sources s USING (provider, external_id)
LEFT JOIN nutrition.foods f ON f.id=s.food_id
LEFT JOIN nutrition.foods occupied ON occupied.id=c.stable_id;

CREATE TEMP TABLE etl_delta ON COMMIT DROP AS
SELECT * FROM etl_merge WHERE outcome IN ('inserted', 'updated');

INSERT INTO nutrition.foods (id, canonical_name, brand, food_type, quality, markets, completeness)
SELECT food_id, name, brand, food_type, quality, markets, completeness
FROM etl_delta WHERE outcome='inserted';

-- A row explicitly associated with another canonical food keeps that food's operator-owned attributes.
UPDATE nutrition.foods f
SET canonical_name=d.name, brand=d.brand, quality=d.quality, markets=d.markets,
    completeness=d.completeness, updated_at=now()
FROM etl_delta d WHERE d.outcome='updated' AND d.food_id=d.stable_id AND f.id=d.food_id;

INSERT INTO nutrition.food_sources (
    id, food_id, provider, external_id, dataset_version, source_url, license_identifier, source_updated_at, checksum
)
SELECT source_id, food_id, provider, external_id, version, source_url, license, source_updated_at, checksum
FROM etl_delta WHERE true
ON CONFLICT (provider, external_id) DO UPDATE SET dataset_version=excluded.dataset_version,
    source_url=excluded.source_url, license_identifier=excluded.license_identifier,
    source_updated_at=coalesce(excluded.source_updated_at,nutrition.food_sources.source_updated_at),
    checksum=excluded.checksum, imported_at=now();

DELETE FROM nutrition.food_names n USING etl_delta d WHERE n.food_id=d.food_id AND n.source_id=d.source_id;
DELETE FROM nutrition.nutrient_values n USING etl_delta d WHERE n.food_id=d.food_id AND n.source_id=d.source_id;
DELETE FROM nutrition.barcodes b USING etl_delta d WHERE b.food_id=d.food_id AND b.source_id=d.source_id;
DELETE FROM nutrition.classifications c USING etl_delta d
WHERE c.food_id=d.food_id AND c.source_id=d.source_id AND c.origin='python_etl'
  AND (c.system, c.version) IN (('ahei_category', '1'), ('ryft_nutrition', '1'));

INSERT INTO nutrition.food_names (food_id, source_id, language, label, normalized, normalized_brand)
SELECT d.food_id, d.source_id, n.lang, n.label, n.normalized, d.normalized_brand
FROM etl_delta d CROSS JOIN LATERAL jsonb_to_recordset(d.names) n(lang text, label text, normalized text);

INSERT INTO nutrition.nutrient_values (
    food_id, source_id, code, amount, reference_amount, reference_unit, method, quality
)
SELECT d.food_id, d.source_id, n.code, n.amount, 100, n.basis, n.method, d.quality
FROM etl_delta d CROSS JOIN LATERAL jsonb_to_recordset(d.nutrients) n(code text, amount numeric, method text, basis text);

INSERT INTO nutrition.barcodes (food_id, source_id, original_barcode, gtin, market)
SELECT d.food_id, d.source_id, d.external_id, d.gtin, market
FROM etl_delta d CROSS JOIN LATERAL unnest(d.markets) market WHERE d.gtin IS NOT NULL;

INSERT INTO nutrition.classifications (food_id, source_id, system, version, code, confidence, contribution_fraction, origin)
SELECT d.food_id, d.source_id, 'ahei_category', '1', d.category, d.confidence, NULL, 'python_etl'
FROM etl_delta d
UNION ALL
SELECT d.food_id, d.source_id, 'ryft_nutrition', '1', p.key, d.confidence, p.value::numeric, 'python_etl'
FROM etl_delta d CROSS JOIN LATERAL jsonb_each_text(d.score_profile) p WHERE true
ON CONFLICT DO NOTHING;

SELECT outcome, count(*) FROM etl_merge GROUP BY outcome
UNION ALL
SELECT 'duplicates', (SELECT count(*) FROM etl_batch) - (SELECT count(*) FROM etl_merge);
