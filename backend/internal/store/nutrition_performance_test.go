package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestNutritionCatalogPerformance records sample timings and physical sizes on
// opt-in local data. Timing is reported without a CI-specific pass threshold.
func TestNutritionCatalogPerformance(t *testing.T) {
	if os.Getenv("RYFT_NUTRITION_PERF") != "1" {
		t.Skip("set RYFT_NUTRITION_PERF=1 on an isolated database")
	}
	ctx := context.Background()
	api, root, user, _ := nutritionTestStore(t)
	for _, sql := range []string{
		`INSERT INTO nutrition.foods(id,canonical_name,brand,food_type,quality,markets,completeness)
			SELECT md5('ryft perf'||i)::uuid, 'Catalog fixture '||md5(i::text),'Fixture Co',
				'branded','source_reported',ARRAY['DE'],100 FROM generate_series(1,10000) i`,
		`INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,source_url,license_identifier,checksum)
			SELECT id,id,'off',id::text,'perf','https://example.invalid/'||id::text,'test',decode(md5(id::text)||md5(id::text),'hex')
			FROM nutrition.foods WHERE brand='Fixture Co'`,
		`INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized,normalized_brand)
			SELECT f.id,s.id,'en',f.canonical_name,lower(f.canonical_name),'fixture co'
			FROM nutrition.foods f JOIN nutrition.food_sources s ON f.id=s.food_id WHERE s.dataset_version='perf'`,
		`INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized,normalized_brand)
			SELECT f.id,s.id,'de','Lebensmittel '||f.id, 'lebensmittel '||f.id,'fixture co'
			FROM nutrition.foods f JOIN nutrition.food_sources s ON f.id=s.food_id
			WHERE s.dataset_version='perf' AND right(f.id::text,1) IN ('0','1','2','3','4','5','6','7')`,
		`INSERT INTO nutrition.nutrient_values(food_id,source_id,code,amount,reference_amount,reference_unit,method,quality)
			SELECT s.food_id,s.id,n.code,1.234567,100,'g','perf','source_reported'
			FROM nutrition.food_sources s CROSS JOIN (SELECT code FROM nutrition.nutrients ORDER BY code LIMIT 24) n
			WHERE s.dataset_version='perf'`,
		`INSERT INTO nutrition.portions(id,food_id,source_id,label,quantity,unit,weight_g)
			SELECT md5('portion'||id)::uuid,food_id,id,'Serving',1,'serving',32 FROM nutrition.food_sources
			WHERE dataset_version='perf' AND right(id::text,1) IN ('0','1','2','3')`,
		`INSERT INTO nutrition.barcodes(food_id,source_id,original_barcode,gtin,market)
			SELECT food_id,id,code,code,'DE' FROM (
				SELECT food_id,id,lpad(row_number() OVER (ORDER BY id)::text,14,'0') AS code
				FROM nutrition.food_sources WHERE dataset_version='perf'
			) s`,
		`VACUUM (ANALYZE) nutrition.foods, nutrition.food_sources, nutrition.food_names, nutrition.nutrient_values,
			nutrition.portions, nutrition.barcodes`,
	} {
		if _, err := root.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var version string
	if err := root.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL %s, 10,000 synthetic foods, 24 values per food", version)
	rows, err := root.Query(ctx, `SELECT relname, pg_table_size(c.oid), pg_indexes_size(c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='nutrition' AND relkind='r' ORDER BY relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var table, index int64
		if err := rows.Scan(&name, &table, &index); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s table %d bytes, indexes %d bytes", name, table, index)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	err = api.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		probeNutritionSQL(t, ctx, tx, "barcode", nutritionBarcodeSQL, "nutrition_barcodes_gtin",
			"00000000005000", "DE", user)
		probeNutritionSQL(t, ctx, tx, "text-selective", nutritionSearchSQL, "",
			"c4ca4238a0b923820dcc509a6f75849b", "en", "DE", 21, 0, "")
		probeNutritionSQL(t, ctx, tx, "text-broad", nutritionSearchSQL, "",
			"catalog fixture", "en", "DE", 21, 0, "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// EXPLAIN reports a Function Scan to the API role. Inspect the function's
	// inner query with the owner, the same scope, and the same parameters.
	err = New(root).nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var sql string
		if err := tx.QueryRow(ctx, `SELECT split_part(prosrc, '$query$', 2) FROM pg_proc
			WHERE oid='nutrition.search_foods_v2(text,text,text,integer,integer,text)'::regprocedure`).Scan(&sql); err != nil {
			return err
		}
		probeNutritionSQL(t, ctx, tx, "indexed-text-selective", sql, "nutrition_names_",
			"c4ca4238a0b923820dcc509a6f75849b", "en", "DE", 21, 0, "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func probeNutritionSQL(t *testing.T, ctx context.Context, tx pgx.Tx, name, sql, index string, args ...any) {
	t.Helper()
	var durations []float64
	for range 41 {
		var raw []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if index != "" && !strings.Contains(string(raw), index) {
			t.Fatalf("%s did not use index %q", name, index)
		}
		var plan []struct {
			Duration float64 `json:"Execution Time"`
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, plan[0].Duration)
	}
	samples := durations[1:]
	slices.Sort(samples)
	t.Logf("%s PostgreSQL execution p95 %.3f ms", name, samples[37])
}
