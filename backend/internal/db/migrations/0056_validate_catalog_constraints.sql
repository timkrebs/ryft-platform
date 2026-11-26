-- D284: Validate the constraints 0055 added NOT VALID. VALIDATE CONSTRAINT scans the
-- table under SHARE UPDATE EXCLUSIVE, which does not block reads or writes. New rows
-- were already checked since 0055; this proves the existing rows too. VALIDATE on an
-- already valid constraint is a no-op, so a rerun is safe.
ALTER TABLE nutrition.foods
    VALIDATE CONSTRAINT nutrition_foods_group_key_format,
    VALIDATE CONSTRAINT nutrition_foods_form_format,
    VALIDATE CONSTRAINT nutrition_foods_subtitles_shape,
    VALIDATE CONSTRAINT nutrition_staple_public;
ALTER TABLE nutrition.portions
    VALIDATE CONSTRAINT nutrition_portions_key_format,
    VALIDATE CONSTRAINT nutrition_portions_short_en_length,
    VALIDATE CONSTRAINT nutrition_portions_short_de_length,
    VALIDATE CONSTRAINT nutrition_portions_source_id_length,
    VALIDATE CONSTRAINT nutrition_default_measured;
ALTER TABLE nutrition.food_sources
    VALIDATE CONSTRAINT food_sources_provider_check,
    VALIDATE CONSTRAINT nutrition_sources_attribution_length,
    VALIDATE CONSTRAINT nutrition_bls_attribution;
