-- Train's number columns as double precision, like the rest of the schema.
-- 0011 made them real, so kg, RPE, factors and loads read back with float4
-- noise (75.1 → 75.0999984741211, 0.64 → 0.6399999856948853). The values are
-- rounded to three decimals on the way, which every one of them fits.
ALTER TABLE exercises
    ALTER COLUMN bodyweight_factor TYPE double precision USING round(bodyweight_factor::numeric, 3)::double precision,
    ALTER COLUMN load_factor       TYPE double precision USING round(load_factor::numeric, 3)::double precision;
ALTER TABLE plan_exercises
    ALTER COLUMN target_value TYPE double precision USING round(target_value::numeric, 3)::double precision;
ALTER TABLE sessions
    ALTER COLUMN bodyweight_kg  TYPE double precision USING round(bodyweight_kg::numeric, 3)::double precision,
    ALTER COLUMN muscular_trimp TYPE double precision USING round(muscular_trimp::numeric, 3)::double precision,
    ALTER COLUMN strain         TYPE double precision USING round(strain::numeric, 3)::double precision,
    ALTER COLUMN volume_kg      TYPE double precision USING round(volume_kg::numeric, 3)::double precision;
ALTER TABLE session_sets
    ALTER COLUMN weight_kg  TYPE double precision USING round(weight_kg::numeric, 3)::double precision,
    ALTER COLUMN rpe        TYPE double precision USING round(rpe::numeric, 3)::double precision,
    ALTER COLUMN distance_m TYPE double precision USING round(distance_m::numeric, 3)::double precision;
ALTER TABLE personal_records
    ALTER COLUMN value TYPE double precision USING round(value::numeric, 3)::double precision;
