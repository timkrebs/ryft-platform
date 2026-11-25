package db

import (
	"context"
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
	if _, err := pool.Exec(ctx, `
SET ROLE ryft_nutrition_etl;
INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier,attribution)
VALUES (gen_random_uuid(),'a7e40c6e-bfb4-47eb-ad64-aea61136db01','bls','M111300','4.0','CC-BY-4.0','Max Rubner-Institut');
RESET ROLE`); err != nil {
		t.Fatalf("ETL could not persist a public, attributed BLS source: %v", err)
	}
}
