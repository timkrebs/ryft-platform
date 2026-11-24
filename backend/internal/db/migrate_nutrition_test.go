package db

import (
	"context"
	"testing"
)

func TestNutritionMigrationWithSupabaseExtensionAndRoles(t *testing.T) {
	pool := freshDB(t, true)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA extensions; CREATE EXTENSION pg_trgm SCHEMA extensions"); err != nil {
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
	withoutRLS := scalar[int](t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname='nutrition' AND NOT rowsecurity`)
	if withoutRLS != 0 {
		t.Errorf("%d unprotected tables", withoutRLS)
	}
}
