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

INSERT INTO nutrition.foods (id, canonical_name, brand, food_type, quality, markets, completeness,
    is_canonical_staple, canonical_group_key, food_form, subtitles, density_g_ml)
SELECT food_id, name, brand, food_type, quality, markets, completeness,
    is_canonical_staple, canonical_group_key, food_form, subtitles, density_g_ml
FROM etl_delta WHERE outcome='inserted';

-- A row explicitly associated with another canonical food keeps that food's operator-owned attributes.
UPDATE nutrition.foods f
SET canonical_name=d.name, brand=d.brand, quality=d.quality, markets=d.markets,
    completeness=d.completeness, is_canonical_staple=d.is_canonical_staple,
    canonical_group_key=d.canonical_group_key, food_form=d.food_form, subtitles=d.subtitles,
    density_g_ml=coalesce(f.density_g_ml,d.density_g_ml),updated_at=now()
FROM etl_delta d WHERE d.outcome='updated' AND d.food_id=d.stable_id AND f.id=d.food_id;

INSERT INTO nutrition.food_sources (
    id,food_id,provider,external_id,dataset_version,source_url,license_identifier,source_updated_at,checksum,attribution
)
SELECT source_id,food_id,provider,external_id,version,source_url,license,source_updated_at,checksum,attribution
FROM etl_delta WHERE true
ON CONFLICT (provider, external_id) DO UPDATE SET dataset_version=excluded.dataset_version,
    source_url=excluded.source_url, license_identifier=excluded.license_identifier,
    source_updated_at=coalesce(excluded.source_updated_at,nutrition.food_sources.source_updated_at),
    checksum=excluded.checksum,attribution=excluded.attribution,imported_at=now();

DELETE FROM nutrition.food_names n USING etl_delta d WHERE n.food_id=d.food_id AND n.source_id=d.source_id;
DELETE FROM nutrition.nutrient_values n USING etl_delta d WHERE n.food_id=d.food_id AND n.source_id=d.source_id;
DELETE FROM nutrition.barcodes b USING etl_delta d WHERE b.food_id=d.food_id AND b.source_id=d.source_id;
DELETE FROM nutrition.classifications c USING etl_delta d
WHERE c.food_id=d.food_id AND c.source_id=d.source_id AND c.origin='python_etl'
  AND (c.system, c.version) IN (('ahei_category', '1'), ('ryft_nutrition', '1'));

INSERT INTO nutrition.food_names (food_id, source_id, language, label, normalized, normalized_brand, alias)
SELECT d.food_id, d.source_id, n.lang, n.label, n.normalized, d.normalized_brand, n.alias
FROM etl_delta d CROSS JOIN LATERAL jsonb_to_recordset(d.names) n(lang text,label text,normalized text,alias boolean);

INSERT INTO nutrition.nutrient_values (
    food_id, source_id, code, amount, reference_amount, reference_unit, method, quality
)
SELECT d.food_id, d.source_id, n.code, n.amount, 100, n.basis, n.method, d.quality
FROM etl_delta d CROSS JOIN LATERAL jsonb_to_recordset(d.nutrients) n(code text, amount numeric, method text, basis text);

INSERT INTO nutrition.barcodes (food_id, source_id, original_barcode, gtin, market)
SELECT d.food_id, d.source_id, d.external_id, d.gtin, market
FROM etl_delta d CROSS JOIN LATERAL unnest(d.markets) market WHERE d.gtin IS NOT NULL;

CREATE TEMP TABLE etl_portions ON COMMIT DROP AS
SELECT d.food_id, d.source_id, coalesce(prior.id, p.id) AS id,
    p.label,p.labels,p.unit,p.weight_g,p.volume_ml,p.source_portion_id,p.portion_key,
    p.short_label_en,p.short_label_de,p.is_default,p.sort_order
FROM etl_delta d CROSS JOIN LATERAL jsonb_to_recordset(d.portions)
    p(id uuid,legacy_id uuid,label text,labels jsonb,unit text,weight_g numeric,volume_ml numeric,
      source_portion_id text,portion_key text,short_label_en text,short_label_de text,is_default boolean,sort_order integer)
LEFT JOIN LATERAL (
    SELECT k.id FROM nutrition.portions k
    WHERE k.food_id=d.food_id AND k.source_id=d.source_id AND k.origin='python_etl'
      -- A keyed row is the same portion only while its measure is the same. A changed measure
      -- gets the new ID computed by import_pg.py and the old row is retired below, so a
      -- selection saved against the old ID cannot log the new grams. Rows imported before
      -- D284 have no key; their legacy ID already contains the measure.
      AND ((k.source_portion_id=p.source_portion_id AND k.unit=p.unit
              AND k.weight_g IS NOT DISTINCT FROM round(p.weight_g,6)
              AND k.volume_ml IS NOT DISTINCT FROM round(p.volume_ml,6))
           OR (k.source_portion_id IS NULL AND k.id=p.legacy_id))
    ORDER BY (k.source_portion_id IS NOT NULL) DESC, k.id
    LIMIT 1
) prior ON true
WHERE prior.id IS NOT NULL OR NOT EXISTS (SELECT FROM nutrition.portions known
    WHERE known.source_id=d.source_id AND known.food_id=d.food_id AND known.origin='manual'
      AND known.label=p.label AND known.unit=p.unit AND known.quantity=1 AND NOT known.estimated
      AND known.weight_g IS NOT DISTINCT FROM p.weight_g AND known.volume_ml IS NOT DISTINCT FROM p.volume_ml);

DELETE FROM nutrition.portions p USING etl_delta d
WHERE p.source_id=d.source_id AND p.food_id=d.food_id AND p.origin='python_etl'
  AND NOT EXISTS (SELECT FROM etl_portions candidate WHERE candidate.id=p.id);

-- At most one default per food (nutrition_portions_one_default) is checked per row, so a default
-- that moves to another portion would collide mid-statement. Clear the flag on the rows this
-- import owns first; the upsert below sets it again on the portion that keeps it.
UPDATE nutrition.portions p SET is_default=false FROM etl_delta d
WHERE p.source_id=d.source_id AND p.food_id=d.food_id AND p.origin='python_etl' AND p.is_default;

INSERT INTO nutrition.portions (id,food_id,source_id,label,labels,quantity,unit,weight_g,volume_ml,origin,
    source_portion_id,portion_key,short_label_en,short_label_de,is_default,sort_order)
SELECT id,food_id,source_id,label,labels,1,unit,weight_g,volume_ml,'python_etl',
    source_portion_id,portion_key,short_label_en,short_label_de,is_default,sort_order
FROM etl_portions
ON CONFLICT (id) DO UPDATE SET label=excluded.label,labels=excluded.labels,quantity=excluded.quantity,
    unit=excluded.unit,weight_g=excluded.weight_g,volume_ml=excluded.volume_ml,
    source_portion_id=excluded.source_portion_id,portion_key=excluded.portion_key,
    short_label_en=excluded.short_label_en,short_label_de=excluded.short_label_de,
    is_default=excluded.is_default,sort_order=excluded.sort_order
WHERE nutrition.portions.origin='python_etl';

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
