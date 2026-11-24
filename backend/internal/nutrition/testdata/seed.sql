-- RYFT-authored search examples. Catalog nutrient values come from imports.
INSERT INTO nutrition.foods(id, canonical_name, food_type, quality) VALUES
    ('a7e40c6e-bfb4-47eb-ad64-aea61136db01', 'Apple', 'generic', 'source_reported'),
    ('a7e40c6e-bfb4-47eb-ad64-aea61136db02', 'Rolled oats', 'generic', 'source_reported'),
    ('a7e40c6e-bfb4-47eb-ad64-aea61136db03', 'Chicken breast', 'generic', 'source_reported');

INSERT INTO nutrition.food_sources (id, food_id, provider, external_id, dataset_version, license_identifier)
SELECT id, id, 'ryft', id::text, 'seed-1', 'RYFT' FROM nutrition.foods WHERE id IN (
    'a7e40c6e-bfb4-47eb-ad64-aea61136db01', 'a7e40c6e-bfb4-47eb-ad64-aea61136db02', 'a7e40c6e-bfb4-47eb-ad64-aea61136db03'
);
INSERT INTO nutrition.food_names(food_id, source_id, language, label, normalized, alias)
SELECT id::uuid, id::uuid, lang, label, normalized, alias FROM (VALUES
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db01', 'en', 'Apple', 'apple', false),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db01', 'de', 'Apfel', 'apfel', false),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db02', 'en', 'Rolled oats', 'rolled oats', false),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db02', 'en', 'Oats', 'oats', true),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db02', 'de', 'Haferflocken', 'haferflocken', false),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db03', 'en', 'Chicken breast', 'chicken breast', false),
 ('a7e40c6e-bfb4-47eb-ad64-aea61136db03', 'de', 'Hähnchenbrust', 'hahnchenbrust', false)
) AS names(id,lang,label,normalized,alias);

-- Synthetic records used to check explicit zero and unknown nutrient behavior.
INSERT INTO nutrition.nutrient_values(food_id,source_id,code,amount,reference_amount,reference_unit,method,quality)
SELECT id,id,code,amount,100,'g','synthetic','source_reported'
FROM (SELECT 'a7e40c6e-bfb4-47eb-ad64-aea61136db01'::uuid AS id) seed
CROSS JOIN (VALUES ('energy_kcal',52::numeric),('protein',0.3),('fat',0),('vitamin_c',4.6)) nutrients(code,amount);
INSERT INTO nutrition.portions(id,food_id,source_id,label,labels,quantity,unit,weight_g)
VALUES ('a7e40c6e-bfb4-47eb-ad64-aea61136db04','a7e40c6e-bfb4-47eb-ad64-aea61136db01',
        'a7e40c6e-bfb4-47eb-ad64-aea61136db01','Medium apple','{"de":"Mittelgroßer Apfel"}',1,'portion',150);
