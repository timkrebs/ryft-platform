-- D265: catalog, scoped writes and import checkpoints live in the Platform database.
CREATE SCHEMA nutrition;
REVOKE ALL ON SCHEMA nutrition FROM PUBLIC;
GRANT USAGE ON SCHEMA nutrition TO ryft_api;

-- Existing Supabase installs can have extensions in extensions instead of public.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
SET LOCAL search_path = pg_catalog, public, extensions;
CREATE FUNCTION nutrition.account() RETURNS uuid
    LANGUAGE sql STABLE SET search_path = pg_catalog
    AS $$ SELECT nullif(current_setting('ryft.nutrition_user', true), '')::uuid $$;
REVOKE ALL ON FUNCTION nutrition.account() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.account() TO ryft_api;

CREATE TABLE nutrition.foods (
    id uuid PRIMARY KEY,
    owner_user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
    canonical_name text NOT NULL CHECK (length(canonical_name) BETWEEN 1 AND 200),
    brand text NOT NULL DEFAULT '' CHECK (length(brand) <= 160),
    food_type text NOT NULL CHECK (food_type IN ('generic', 'branded', 'custom', 'recipe')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    quality text NOT NULL CHECK (quality IN ('verified', 'source_reported', 'warnings', 'user_reported')),
    markets text[] NOT NULL DEFAULT '{}',
    density_g_ml numeric CHECK (density_g_ml > 0 AND density_g_ml <= 30),
    completeness smallint NOT NULL DEFAULT 0 CHECK (completeness BETWEEN 0 AND 100),
    request_hash bytea CHECK (octet_length(request_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, owner_user_id),
    CHECK ((owner_user_id IS NOT NULL) = (food_type IN ('custom', 'recipe')))
);
CREATE INDEX nutrition_foods_owner ON nutrition.foods(owner_user_id) WHERE owner_user_id IS NOT NULL;

CREATE TABLE nutrition.food_sources (
    id uuid PRIMARY KEY,
    food_id uuid NOT NULL REFERENCES nutrition.foods ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('usda', 'off', 'user', 'ryft')),
    external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 200),
    dataset_version text NOT NULL CHECK (length(dataset_version) BETWEEN 1 AND 100),
    source_url text NOT NULL DEFAULT '',
    license_identifier text NOT NULL,
    source_updated_at timestamptz,
    imported_at timestamptz NOT NULL DEFAULT now(),
    checksum bytea CHECK (octet_length(checksum) = 32),
    UNIQUE (provider, external_id),
    UNIQUE (id, food_id)
);
CREATE INDEX nutrition_sources_food ON nutrition.food_sources(food_id);

CREATE TABLE nutrition.food_names (
    food_id uuid NOT NULL REFERENCES nutrition.foods ON DELETE CASCADE,
    source_id uuid NOT NULL,
    language text NOT NULL CHECK (language ~ '^[a-z]{2,8}(-[a-z0-9]{2,8})*$'),
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
    normalized text NOT NULL CHECK (normalized <> ''),
    normalized_brand text NOT NULL DEFAULT '',
    alias boolean NOT NULL DEFAULT false,
    document tsvector GENERATED ALWAYS AS (
        to_tsvector('pg_catalog.simple', normalized || ' ' || normalized_brand) ||
        CASE language
            WHEN 'de' THEN to_tsvector('pg_catalog.german', normalized)
            WHEN 'en' THEN to_tsvector('pg_catalog.english', normalized)
            ELSE ''::tsvector
        END
    ) STORED,
    PRIMARY KEY (food_id, source_id, language, label),
    FOREIGN KEY (source_id, food_id) REFERENCES nutrition.food_sources(id, food_id) ON DELETE CASCADE
);
CREATE INDEX nutrition_names_prefix ON nutrition.food_names(normalized text_pattern_ops);
CREATE INDEX nutrition_names_brand ON nutrition.food_names(normalized_brand text_pattern_ops);
CREATE INDEX nutrition_names_trgm ON nutrition.food_names USING gin(normalized gin_trgm_ops);
CREATE INDEX nutrition_names_document ON nutrition.food_names USING gin(document);

CREATE TABLE nutrition.barcodes (
    food_id uuid NOT NULL,
    source_id uuid NOT NULL,
    original_barcode text NOT NULL CHECK (original_barcode ~ '^([0-9]{8}|[0-9]{12,14})$'),
    gtin text NOT NULL CHECK (gtin ~ '^[0-9]{14}$'),
    market text NOT NULL DEFAULT '',
    validation_status text NOT NULL DEFAULT 'valid' CHECK (validation_status IN ('valid', 'conflict')),
    PRIMARY KEY (source_id, original_barcode, market),
    FOREIGN KEY (source_id, food_id) REFERENCES nutrition.food_sources(id, food_id) ON DELETE CASCADE
);
CREATE INDEX nutrition_barcodes_gtin ON nutrition.barcodes(gtin, food_id);

CREATE TABLE nutrition.nutrients (
    code text PRIMARY KEY,
    name text NOT NULL,
    unit text NOT NULL CHECK (unit IN ('g', 'mg', 'ug', 'kcal', 'kJ')),
    nutrient_group text NOT NULL,
    description text NOT NULL DEFAULT ''
);
INSERT INTO nutrition.nutrients (code, name, unit, nutrient_group, description) VALUES
    ('energy_kcal','Energy','kcal','energy',''), ('energy_kj','Energy','kJ','energy',''),
    ('protein','Protein','g','macro',''), ('carbohydrates','Available carbohydrates','g','macro','Excludes fiber'),
    ('carbohydrates_total','Total carbohydrates','g','macro','Includes fiber'),
    ('fat','Fat','g','macro',''), ('fiber','Fiber','g','macro',''), ('total_sugar','Total sugar','g','macro',''),
    ('added_sugar','Added sugar','g','macro',''), ('saturated_fat','Saturated fat','g','macro',''),
    ('monounsaturated_fat','Monounsaturated fat','g','macro',''), ('polyunsaturated_fat','Polyunsaturated fat','g','macro',''),
    ('trans_fat','Trans fat','g','macro',''),
    ('omega_3','Omega-3','g','fatty_acid',''), ('omega_6','Omega-6','g','fatty_acid',''),
    ('epa','EPA','g','fatty_acid',''), ('dha','DHA','g','fatty_acid',''),
    ('sodium','Sodium','mg','mineral','Elemental sodium'), ('potassium','Potassium','mg','mineral',''),
    ('calcium','Calcium','mg','mineral',''), ('magnesium','Magnesium','mg','mineral',''),
    ('iron','Iron','mg','mineral',''), ('zinc','Zinc','mg','mineral',''),
    ('phosphorus','Phosphorus','mg','mineral',''), ('selenium','Selenium','ug','mineral',''),
    ('vitamin_a','Vitamin A','ug','vitamin','Retinol activity equivalents'),
    ('vitamin_c','Vitamin C','mg','vitamin',''), ('vitamin_d','Vitamin D','ug','vitamin',''),
    ('vitamin_e','Vitamin E','mg','vitamin','Alpha-tocopherol'), ('vitamin_k','Vitamin K','ug','vitamin','Phylloquinone'),
    ('vitamin_b1','Thiamin','mg','vitamin',''), ('vitamin_b2','Riboflavin','mg','vitamin',''),
    ('vitamin_b3','Niacin','mg','vitamin','Niacin mass'), ('vitamin_b6','Vitamin B6','mg','vitamin',''),
    ('vitamin_b12','Vitamin B12','ug','vitamin',''), ('folate','Folate','ug','vitamin','Dietary folate equivalents'),
    ('caffeine','Caffeine','mg','compound',''), ('alcohol','Alcohol','g','compound','Ethanol mass'),
    ('water','Water','g','compound','Water mass'), ('cholesterol','Cholesterol','mg','compound','');

CREATE TABLE nutrition.nutrient_values (
    food_id uuid NOT NULL,
    source_id uuid NOT NULL,
    code text NOT NULL REFERENCES nutrition.nutrients(code),
    amount numeric(20,9) NOT NULL CHECK (amount BETWEEN 0 AND 100000000),
    reference_amount numeric(16,6) NOT NULL CHECK (reference_amount > 0 AND reference_amount <= 100000),
    reference_unit text NOT NULL CHECK (reference_unit IN ('g', 'ml', 'serving')),
    method text NOT NULL,
    quality text NOT NULL CHECK (quality IN ('verified', 'source_reported', 'warnings', 'user_reported')),
    PRIMARY KEY (food_id, source_id, code, reference_unit, reference_amount),
    FOREIGN KEY (source_id, food_id) REFERENCES nutrition.food_sources(id, food_id) ON DELETE CASCADE
);

CREATE TABLE nutrition.portions (
    id uuid PRIMARY KEY,
    food_id uuid NOT NULL,
    source_id uuid NOT NULL,
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
    labels jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(labels) = 'object'),
    quantity numeric(16,6) NOT NULL CHECK (quantity > 0 AND quantity <= 100000),
    unit text NOT NULL CHECK (unit IN ('portion', 'serving')),
    weight_g numeric(16,6) CHECK (weight_g > 0 AND weight_g <= 100000),
    volume_ml numeric(16,6) CHECK (volume_ml > 0 AND volume_ml <= 100000),
    estimated boolean NOT NULL DEFAULT false,
    CHECK (weight_g IS NOT NULL OR volume_ml IS NOT NULL OR unit = 'serving'),
    FOREIGN KEY (source_id, food_id) REFERENCES nutrition.food_sources(id, food_id) ON DELETE CASCADE
);
CREATE INDEX nutrition_portions_food ON nutrition.portions(food_id, id);

CREATE TABLE nutrition.classifications (
    food_id uuid NOT NULL,
    source_id uuid NOT NULL,
    system text NOT NULL,
    version text NOT NULL,
    code text NOT NULL,
    confidence numeric CHECK (confidence BETWEEN 0 AND 1),
    contribution_fraction numeric CHECK (contribution_fraction BETWEEN 0 AND 1),
    PRIMARY KEY (food_id, source_id, system, version, code),
    FOREIGN KEY (source_id, food_id) REFERENCES nutrition.food_sources(id, food_id) ON DELETE CASCADE
);

CREATE TABLE nutrition.meal_requests (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    request_id uuid NOT NULL,
    PRIMARY KEY (user_id, request_id)
);
CREATE TABLE nutrition.meals (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    client_request_id uuid NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    consumed_at timestamptz NOT NULL,
    timezone text NOT NULL,
    local_date date NOT NULL,
    meal_type text NOT NULL CHECK (meal_type IN ('breakfast','lunch','dinner','snack','other')),
    input_method text NOT NULL CHECK (input_method IN ('manual_search','barcode','photo_capture','photo_import','natural_language','nutrition_label','external_import','manual_entry')),
    name text NOT NULL CHECK (length(name) <= 200),
    notes text NOT NULL CHECK (length(notes) <= 2000),
    revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, client_request_id),
    UNIQUE (id, user_id),
    FOREIGN KEY (user_id, client_request_id) REFERENCES nutrition.meal_requests(user_id, request_id) ON DELETE CASCADE
);
CREATE INDEX nutrition_meals_day ON nutrition.meals(user_id, local_date, consumed_at DESC, id);
CREATE TABLE nutrition.meal_items (
    id uuid PRIMARY KEY,
    meal_id uuid NOT NULL,
    user_id uuid NOT NULL,
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 49),
    food_id uuid REFERENCES nutrition.foods ON DELETE SET NULL,
    food_name text NOT NULL,
    brand text NOT NULL,
    quantity numeric(16,6) NOT NULL CHECK (quantity > 0 AND quantity <= 100000),
    unit text NOT NULL CHECK (unit IN ('g','ml','portion','serving')),
    portion_id uuid,
    weight_g numeric(20,6) CHECK (weight_g > 0),
    volume_ml numeric(20,6) CHECK (volume_ml > 0),
    nutrients jsonb NOT NULL CHECK (jsonb_typeof(nutrients) = 'object'),
    sources jsonb NOT NULL CHECK (jsonb_typeof(sources) = 'array'),
    calculation_version smallint NOT NULL CHECK (calculation_version > 0),
    created_at timestamptz NOT NULL,
    UNIQUE (meal_id, position),
    FOREIGN KEY (meal_id, user_id) REFERENCES nutrition.meals(id, user_id) ON DELETE CASCADE
);
CREATE INDEX nutrition_items_food ON nutrition.meal_items(food_id);

CREATE TABLE nutrition.goal_revisions (
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    revision integer NOT NULL CHECK (revision > 0),
    targets jsonb NOT NULL CHECK (jsonb_typeof(targets) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, revision)
);

CREATE TABLE nutrition.submissions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    food_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','submitted','validated','approved','rejected')),
    moderation_code text,
    moderated_at timestamptz,
    moderated_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (food_id, user_id) REFERENCES nutrition.foods(id, owner_user_id) ON DELETE CASCADE,
    UNIQUE (user_id, food_id)
);

CREATE TABLE nutrition.import_jobs (
    id uuid PRIMARY KEY,
    provider text NOT NULL CHECK (provider IN ('usda','off')),
    dataset_version text NOT NULL,
    dataset_sha256 bytea NOT NULL CHECK (octet_length(dataset_sha256) = 32),
    options_hash bytea NOT NULL CHECK (octet_length(options_hash) = 32),
    status text NOT NULL CHECK (status IN ('running','complete','failed')),
    checkpoint bigint NOT NULL DEFAULT 0 CHECK (checkpoint >= 0),
    counts jsonb NOT NULL DEFAULT '{}',
    reason text,
    started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX nutrition_import_recent ON nutrition.import_jobs(provider, dataset_version, started_at DESC);
CREATE INDEX nutrition_items_user ON nutrition.meal_items(user_id);

GRANT SELECT ON nutrition.foods, nutrition.food_sources, nutrition.food_names, nutrition.barcodes,
    nutrition.nutrients, nutrition.nutrient_values, nutrition.portions, nutrition.classifications TO ryft_api;
GRANT INSERT ON nutrition.foods, nutrition.food_sources, nutrition.food_names, nutrition.nutrient_values, nutrition.portions TO ryft_api;
GRANT SELECT, INSERT ON nutrition.meal_requests, nutrition.goal_revisions, nutrition.submissions TO ryft_api;
GRANT UPDATE (status, updated_at) ON nutrition.submissions TO ryft_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON nutrition.meals TO ryft_api;
GRANT SELECT, INSERT, DELETE ON nutrition.meal_items TO ryft_api;

ALTER TABLE nutrition.foods ENABLE ROW LEVEL SECURITY;
CREATE POLICY visible_foods ON nutrition.foods FOR SELECT TO ryft_api
    USING (owner_user_id IS NULL OR owner_user_id = nutrition.account());
CREATE POLICY create_private_food ON nutrition.foods FOR INSERT TO ryft_api
    WITH CHECK (owner_user_id = nutrition.account() AND food_type = 'custom' AND quality = 'user_reported');

ALTER TABLE nutrition.nutrients ENABLE ROW LEVEL SECURITY;
CREATE POLICY registry_read ON nutrition.nutrients FOR SELECT TO ryft_api USING (true);
ALTER TABLE nutrition.import_jobs ENABLE ROW LEVEL SECURITY;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['food_sources','food_names','barcodes','nutrient_values','portions','classifications'] LOOP
        EXECUTE format('ALTER TABLE nutrition.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY visible_food_children ON nutrition.%1$I FOR SELECT TO ryft_api
                        USING (EXISTS (SELECT FROM nutrition.foods f WHERE f.id = %1$I.food_id))', t);
    END LOOP;
    FOREACH t IN ARRAY ARRAY['food_sources','food_names','nutrient_values','portions'] LOOP
        EXECUTE format('CREATE POLICY create_private_food_children ON nutrition.%1$I FOR INSERT TO ryft_api
                        WITH CHECK (EXISTS (SELECT FROM nutrition.foods f WHERE f.id = %1$I.food_id
                                                         AND f.owner_user_id = nutrition.account())%2$s)',
                       t, CASE WHEN t = 'food_sources' THEN ' AND provider = ''user''' ELSE '' END);
    END LOOP;
    FOREACH t IN ARRAY ARRAY['meal_requests','meals','meal_items','goal_revisions','submissions'] LOOP
        EXECUTE format('ALTER TABLE nutrition.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY owner_read ON nutrition.%I FOR SELECT TO ryft_api
                        USING (user_id = nutrition.account())', t);
        EXECUTE format('CREATE POLICY owner_insert ON nutrition.%I FOR INSERT TO ryft_api
                        WITH CHECK (user_id = nutrition.account()%s)', t,
                       CASE WHEN t = 'submissions' THEN ' AND status IN (''draft'', ''submitted'') AND moderated_at IS NULL AND moderation_code IS NULL AND moderated_by IS NULL'
                            WHEN t = 'meal_items' THEN ' AND (food_id IS NULL OR EXISTS (SELECT FROM nutrition.foods f WHERE f.id = meal_items.food_id))'
                            ELSE '' END);
    END LOOP;
    FOREACH t IN ARRAY ARRAY['meals','meal_items'] LOOP
        EXECUTE format('CREATE POLICY owner_delete ON nutrition.%I FOR DELETE TO ryft_api USING (user_id = nutrition.account())', t);
    END LOOP;
END $$;

CREATE POLICY owner_edit_meal ON nutrition.meals FOR UPDATE TO ryft_api
    USING (user_id = nutrition.account()) WITH CHECK (user_id = nutrition.account());
CREATE POLICY owner_submit ON nutrition.submissions FOR UPDATE TO ryft_api
    USING (user_id = nutrition.account() AND status = 'draft')
    WITH CHECK (user_id = nutrition.account() AND status = 'submitted');

-- RLS on food_names prevents PostgreSQL from pushing non-leakproof text
-- operators into GIN index scans. This narrow read function owns the visibility
-- predicate, scopes through nutrition.account(), and returns compact matches.
-- Its fixed search path and qualified extension symbols are needed when
-- Supabase has installed pg_trgm in its extensions schema.
DO $$
DECLARE extension_schema text;
BEGIN
    SELECT n.nspname INTO STRICT extension_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pg_trgm';
    EXECUTE replace($definition$
CREATE FUNCTION nutrition.search_foods(text, text, text, integer, integer)
RETURNS TABLE(id uuid, label text, brand text, food_type text, quality text)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $search$
BEGIN
    -- Replan for the actual terms, since selective and broad searches need
    -- different paths. Only bound parameters supply user input.
    RETURN QUERY EXECUTE $query$
WITH terms AS (
SELECT plainto_tsquery('pg_catalog.simple', $1) ||
	plainto_tsquery(CASE $2 WHEN 'de' THEN 'pg_catalog.german'::regconfig
		WHEN 'en' THEN 'pg_catalog.english'::regconfig ELSE 'pg_catalog.simple'::regconfig END, $1) AS query
		), candidates AS (
SELECT DISTINCT ON (f.id) f.id, n.label, f.brand, f.food_type, f.quality, f.completeness,
	CASE WHEN n.normalized = $1 THEN 5
	     WHEN n.normalized LIKE $1 || '%' THEN 4
	     WHEN n.normalized_brand LIKE $1 || '%' THEN 3
	     WHEN n.document @@ terms.query THEN 2 ELSE 1 END AS match,
	n.language = $2 AS preferred_language, NOT n.alias AS preferred_label,
	ts_rank(n.document, terms.query) AS lexical,
	@trgm@.similarity(n.normalized, $1) AS fuzzy,
	$3 = ANY(f.markets) AS preferred_market
FROM nutrition.food_names n JOIN nutrition.foods f ON f.id=n.food_id CROSS JOIN terms
WHERE f.status='active' AND nutrition.account() IS NOT NULL
  AND (f.owner_user_id IS NULL OR f.owner_user_id=nutrition.account())
  AND (n.normalized LIKE $1 || '%' OR n.normalized_brand LIKE $1 || '%' OR n.document @@ terms.query
     OR (length($1) >= 3 AND n.normalized OPERATOR(@trgm@.%) $1))
ORDER BY f.id, match DESC, preferred_language DESC, preferred_label DESC, lexical DESC, fuzzy DESC, n.label
		)
		SELECT id, label, brand, food_type, quality FROM candidates
		ORDER BY match DESC, preferred_language DESC, preferred_market DESC,
CASE quality WHEN 'verified' THEN 3 WHEN 'source_reported' THEN 2 WHEN 'user_reported' THEN 1 ELSE 0 END DESC,
completeness DESC, lexical DESC, fuzzy DESC, id
		LIMIT least(greatest($4,0),51) OFFSET least(greatest($5,0),1000)
    $query$ USING $1, $2, $3, $4, $5;
END
$search$
    $definition$, '@trgm@', quote_ident(extension_schema));
END $$;
REVOKE ALL ON FUNCTION nutrition.search_foods(text,text,text,integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nutrition.search_foods(text,text,text,integer,integer) TO ryft_api;

INSERT INTO public.feature_flags(key, enabled, rollout_pct)
VALUES ('nutrition.enabled', true, 0) ON CONFLICT (key) DO NOTHING;
