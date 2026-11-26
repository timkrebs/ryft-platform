package db

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestNutritionMigrationWithSupabaseExtensionAndRoles(t *testing.T) {
	pool := freshDB(t, true)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA extensions; CREATE EXTENSION pg_trgm SCHEMA extensions"); err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(ctx, pool, quiet, 47); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"anon", "authenticated", "service_role", "ryft_ingest", "ryft_scoring", "ryft_notify"} {
		if scalar[bool](t, pool, "SELECT has_schema_privilege($1,'nutrition','USAGE')", role) {
			t.Errorf("%s can access Nutrition", role)
		}
	}
	if scalar[bool](t, pool, "SELECT has_schema_privilege('ryft_api','extensions','USAGE')") {
		t.Fatal("API role has direct access to the extensions schema")
	}
	if !scalar[bool](t, pool, `SELECT has_function_privilege(
		'ryft_api','nutrition.search_foods(text,text,text,integer,integer)','EXECUTE')`) {
		t.Fatal("API role cannot use the scoped search function")
	}
	if scalar[bool](t, pool, "SELECT has_table_privilege('ryft_api','nutrition.import_jobs','SELECT')") {
		t.Fatal("API can read operator import jobs")
	}
	for _, claim := range []string{
		"nutrition.claim_wearable(timestamptz,text)",
		"nutrition.claim_wearable(timestamptz,text,text[])",
		"nutrition.complete_wearable_lease(bigint,smallint,text,text,timestamptz)",
	} {
		if !scalar[bool](t, pool, "SELECT has_function_privilege('ryft_api',$1,'EXECUTE')", claim) {
			t.Errorf("API role cannot execute %s", claim)
		}
		for _, role := range []string{"anon", "authenticated", "service_role", "ryft_ingest", "ryft_nutrition_etl"} {
			if scalar[bool](t, pool, "SELECT has_function_privilege($1,$2,'EXECUTE')", role, claim) {
				t.Errorf("%s can execute %s", role, claim)
			}
		}
	}
	for _, role := range []string{"anon", "authenticated", "service_role", "ryft_api", "ryft_ingest", "ryft_nutrition_etl"} {
		if scalar[bool](t, pool, "SELECT has_function_privilege($1,'nutrition.discard_revoked_wearable()','EXECUTE')", role) {
			t.Errorf("%s has execute on the consent-withdrawal trigger function", role)
		}
	}
	withoutRLS := scalar[int](t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname='nutrition' AND NOT rowsecurity`)
	if withoutRLS != 0 {
		t.Errorf("%d unprotected tables", withoutRLS)
	}
}

func TestNutritionPortionUpgradeFrom052(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := migrateTo(ctx, pool, quiet, 52); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../nutrition/testdata/seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id) VALUES (md5('portion-owner')::uuid);
INSERT INTO nutrition.meal_requests(user_id,request_id) VALUES (md5('portion-owner')::uuid,md5('legacy-request')::uuid);
INSERT INTO nutrition.meals(id,user_id,client_request_id,request_hash,consumed_at,timezone,local_date,meal_type,input_method,name,notes)
VALUES (md5('legacy-meal')::uuid,md5('portion-owner')::uuid,md5('legacy-request')::uuid,decode(repeat('00',32),'hex'),
    now(),'UTC',CURRENT_DATE,'snack','manual_search','','');
INSERT INTO nutrition.meal_items(id,meal_id,user_id,position,food_id,food_name,brand,quantity,unit,portion_id,weight_g,nutrients,sources,calculation_version,created_at)
SELECT gen_random_uuid(),md5('legacy-meal')::uuid,md5('portion-owner')::uuid,v.pos,v.food::uuid,'Saved name','',1,'portion',
    'a7e40c6e-bfb4-47eb-ad64-aea61136db04',v.grams,'{}','[]',1,now()
FROM (VALUES
  (0,'a7e40c6e-bfb4-47eb-ad64-aea61136db01',150),
  (1,'a7e40c6e-bfb4-47eb-ad64-aea61136db01',140),
  (2,'a7e40c6e-bfb4-47eb-ad64-aea61136db02',150)
) v(pos,food,grams)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if label := scalar[string](t, pool, "SELECT portion_label_snapshot FROM nutrition.meal_items WHERE position=0"); label != "Medium apple" {
		t.Errorf("legacy label = %q", label)
	}
	if count := scalar[int](t, pool, "SELECT count(*) FROM nutrition.meal_items WHERE position>0 AND portion_label_snapshot IS NOT NULL"); count != 0 {
		t.Errorf("ambiguous legacy labels filled: %d", count)
	}
	if n := scalar[int](t, pool, "SELECT count(*) FROM nutrition.meal_items WHERE food_name='Saved name' AND nutrients='{}'"); n != 3 {
		t.Error("legacy snapshot changed")
	}
	if scalar[bool](t, pool, "SELECT has_table_privilege('ryft_nutrition_etl','nutrition.food_favorites','SELECT')") {
		t.Fatal("ETL can read member favorites")
	}
	fn := "nutrition.search_foods_v2(text,text,text,integer,integer,text)"
	for _, role := range []string{"anon", "authenticated", "ryft_nutrition_etl"} {
		if scalar[bool](t, pool, "SELECT has_function_privilege($1,$2,'EXECUTE')", role, fn) {
			t.Errorf("%s can execute personalized search", role)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
VALUES (gen_random_uuid(),'a7e40c6e-bfb4-47eb-ad64-aea61136db01','bls','M111300','4.0','CC-BY-4.0')`); err == nil {
		t.Fatal("BLS accepted without named attribution")
	}
	// SET LOCAL keeps the role inside the transaction: a failed statement must never
	// leave a pooled connection running as the ETL role.
	asETL := func(statements ...string) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) // no-op after Commit
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE ryft_nutrition_etl"); err != nil {
			return err
		}
		for _, statement := range statements {
			tag, err := tx.Exec(ctx, statement)
			if err != nil {
				return err
			}
			// Row-level security hides rows silently; every statement here must touch one.
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("%q affected %d rows", statement, tag.RowsAffected())
			}
		}
		return tx.Commit(ctx)
	}
	if err := asETL(`INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier,attribution)
		VALUES (gen_random_uuid(),'a7e40c6e-bfb4-47eb-ad64-aea61136db01','bls','M111300','4.0','CC-BY-4.0','Max Rubner-Institut')`); err != nil {
		t.Fatalf("ETL could not persist a public, attributed BLS source: %v", err)
	}
	if role := scalar[string](t, pool, "SELECT current_user"); role == "ryft_nutrition_etl" {
		t.Fatal("the ETL role leaked onto a pooled connection")
	}
	const portion = `INSERT INTO nutrition.portions(id,food_id,source_id,label,quantity,unit,weight_g,origin,source_portion_id,is_default)
		SELECT gen_random_uuid(),s.food_id,s.id,'%s',1,'portion',100,'python_etl','%s',%t
		FROM nutrition.food_sources s WHERE s.provider='bls' AND s.external_id='M111300'`
	// The ETL can write, clear and set the default flag on its own rows (etl_portion_update).
	if err := asETL(fmt.Sprintf(portion, "First", "p1", true), fmt.Sprintf(portion, "Second", "p2", false),
		"UPDATE nutrition.portions SET is_default=false WHERE origin='python_etl' AND is_default",
		"UPDATE nutrition.portions SET is_default=true WHERE source_portion_id='p2'"); err != nil {
		t.Fatalf("ETL could not move a default portion: %v", err)
	}
	// A second default for the same food is refused by nutrition_portions_one_default.
	if err := asETL(fmt.Sprintf(portion, "Third", "p3", true)); err == nil {
		t.Fatal("two default portions accepted for one food")
	}
	if scalar[bool](t, pool, "SELECT has_column_privilege('ryft_nutrition_etl','nutrition.meal_items','portion_id','SELECT')") {
		t.Fatal("ETL can read diary items")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint
		WHERE conrelid IN ('nutrition.foods'::regclass,'nutrition.portions'::regclass,'nutrition.food_sources'::regclass)
		AND contype='c' AND NOT convalidated`); n != 0 {
		t.Errorf("%d catalog constraints left NOT VALID", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_constraint WHERE contype='c' AND convalidated AND conname IN (
		'nutrition_foods_group_key_format','nutrition_foods_form_format','nutrition_foods_subtitles_shape','nutrition_staple_public',
		'nutrition_portions_key_format','nutrition_portions_short_en_length','nutrition_portions_short_de_length',
		'nutrition_portions_source_id_length','nutrition_default_measured','food_sources_provider_check',
		'nutrition_sources_attribution_length','nutrition_bls_attribution')`); n != 12 {
		t.Errorf("%d of 12 named catalog constraints are validated", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_indexes WHERE schemaname='nutrition' AND indexname IN (
		'nutrition_portions_source_key','nutrition_portions_semantic_key','nutrition_portions_one_default',
		'nutrition_items_food_recent','nutrition_favorites_food')`); n != 5 {
		t.Errorf("%d of 5 selection indexes exist", n)
	}
	if !scalar[bool](t, pool, `SELECT EXISTS (SELECT FROM information_schema.columns WHERE table_schema='nutrition'
		AND table_name='food_favorites' AND column_name='created_at' AND is_nullable='NO')`) {
		t.Error("food_favorites has no created_at for the export")
	}
}
