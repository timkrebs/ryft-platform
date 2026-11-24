-- D266: score classifications are captured when a meal item is written.
ALTER TABLE nutrition.meal_items ADD COLUMN quality_profile jsonb
    CHECK (jsonb_typeof(quality_profile) = 'object');

GRANT INSERT ON nutrition.classifications TO ryft_api;
CREATE POLICY create_private_quality_profile ON nutrition.classifications FOR INSERT TO ryft_api
    WITH CHECK (system = 'ryft_nutrition' AND version = '1'
        AND code IN ('non_starchy_vegetable_fraction', 'whole_fruit_fraction', 'nuts_legumes_fraction',
                     'red_processed_meat_fraction', 'sugary_drink')
        AND contribution_fraction IS NOT NULL
        AND (code <> 'sugary_drink' OR contribution_fraction IN (0, 1))
        AND EXISTS (
            SELECT FROM nutrition.foods f
            JOIN nutrition.food_sources s ON s.food_id = f.id
            WHERE f.id = classifications.food_id AND f.owner_user_id = nutrition.account()
              AND s.id = classifications.source_id AND s.provider = 'user'
        ));

CREATE INDEX nutrition_meals_time ON nutrition.meals(user_id, consumed_at);

CREATE TABLE nutrition.glucose_samples (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    measured_at timestamptz NOT NULL,
    glucose_mg_dl double precision NOT NULL CHECK (glucose_mg_dl > 0 AND glucose_mg_dl <= 10000),
    PRIMARY KEY (user_id, measured_at)
);
ALTER TABLE nutrition.glucose_samples ENABLE ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON nutrition.glucose_samples TO ryft_api;
CREATE POLICY owner_glucose ON nutrition.glucose_samples TO ryft_api
    USING (user_id = nutrition.account()) WITH CHECK (user_id = nutrition.account());
