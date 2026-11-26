package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/nutrition/importer"
)

var fixtureApple = uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db01")

func nutritionTestStore(t *testing.T) (*Store, *pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	_, root := testStore(t)
	ctx := context.Background()
	seed, err := os.ReadFile("../nutrition/testdata/seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	cfg := root.Config()
	cfg.MaxConns = 2
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE ryft_api")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := New(pool)
	a, b := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	return st, root, a, b
}

func TestNutritionBilingualCatalogSearch(t *testing.T) {
	ctx := context.Background()
	st, root, user, _ := nutritionTestStore(t)
	add := func(label, language, foodType string, markets []string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.foods(id,canonical_name,food_type,quality,markets)
			VALUES ($1,$2,$3,'source_reported',$4)`, id, label, foodType, markets); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
			VALUES ($1,$1,'ryft',$2,'test','RYFT')`, id, id.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized)
			VALUES ($1,$1,$2,$3,$4)`, id, language, label, nutrition.Normalize(label)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	banana := add("Bananas, raw", "en", "generic", []string{})
	juice := add("Apple juice", "en", "generic", []string{"DE"})
	bean := add("Kichererbsen", "de", "generic", []string{})
	add("Lentils brand meal", "en", "branded", []string{"DE"})
	de := add("Quark", "de", "branded", []string{"DE"})
	ch := add("Quark", "de", "branded", []string{"CH"})
	for _, tc := range []struct {
		query, language, market string
		want                    uuid.UUID
	}{
		{"Apfel", "de", "DE", fixtureApple}, {"Apple", "en", "DE", fixtureApple}, {"Apfelsaft", "de", "DE", juice},
		{"Banane", "de-DE", "de", banana}, {"chickpeas", "en", "", bean},
		{"Linsen", "de", "DE", uuid.Nil}, {"Quark", "de", "DE", de}, {"Quark", "de", "CH", ch},
	} {
		page, err := st.SearchFoods(ctx, user, model.FoodSearch{Query: tc.query, Language: tc.language, Market: tc.market})
		if err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		got := uuid.Nil
		if len(page.Foods) > 0 {
			got = page.Foods[0].ID
		}
		if got != tc.want {
			t.Errorf("%+v: first ID %s", tc, got)
		}
	}
}

func TestNutritionCatalogPrivacyAndSearch(t *testing.T) {
	ctx := context.Background()
	st, root, a, b := nutritionTestStore(t)
	for _, tc := range []struct{ q, lang, name string }{
		{"Apple", "en", "Apple"}, {"Apfel", "de", "Apfel"}, {"Hahnchenbrust", "de", "Hähnchenbrust"},
		{"appl", "en", "Apple"}, {"appel", "en", "Apple"}, {"oats", "en", "Oats"},
		{"chicken breasts", "en", "Chicken breast"},
	} {
		page, err := st.SearchFoods(ctx, a, model.FoodSearch{Query: tc.q, Language: tc.lang})
		if err != nil || len(page.Foods) == 0 || page.Foods[0].Name != tc.name {
			t.Errorf("%+v: %+v %v", tc, page, err)
		}
	}
	zero := 0.
	key := uuid.New()
	custom := model.FoodCustomInput{ClientRequestID: key, Name: "Private Müsli", Brand: "Hafer Test", Nutrients: []model.FoodNutrientWrite{
		{Code: "fat", Amount: &zero, Unit: "g", ReferenceAmount: 100, ReferenceUnit: "g"},
	}}
	food, err := st.CreateCustomFood(ctx, a, custom)
	if err != nil || food.Name != custom.Name || len(food.Nutrients) != 1 || food.Nutrients[0].Amount != 0 {
		t.Fatalf("create custom: %+v, %v", food, err)
	}
	same, err := st.CreateCustomFood(ctx, a, custom)
	if err != nil || same.ID != food.ID {
		t.Fatalf("retry: %+v %v", same, err)
	}
	custom.Name = "Conflicting same key"
	if _, err := st.CreateCustomFood(ctx, a, custom); !errors.Is(err, ErrNutritionConflict) {
		t.Fatal(err)
	}
	if _, err := st.Food(ctx, b, food.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.FoodPortions(ctx, b, food.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if page, err := st.SearchFoods(ctx, a, model.FoodSearch{Query: "Hafer Test"}); err != nil || len(page.Foods) != 1 {
		t.Fatalf("brand match: %+v %v", page, err)
	}
	if page, err := st.SearchFoods(ctx, b, model.FoodSearch{Query: "Private Müsli"}); err != nil || len(page.Foods) != 0 {
		t.Fatalf("privacy leak: %+v %v", page, err)
	}
	if _, err := st.SubmitFood(ctx, b, model.FoodSubmissionInput{FoodID: food.ID, Status: "draft"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.SubmitFood(ctx, a, model.FoodSubmissionInput{FoodID: food.ID, Status: "approved"}); !errors.Is(err, nutrition.ErrInvalid) {
		t.Fatal(err)
	}
	for _, status := range []string{"draft", "submitted", "submitted"} {
		row, err := st.SubmitFood(ctx, a, model.FoodSubmissionInput{FoodID: food.ID, Status: status})
		if err != nil || row.Status != status {
			t.Fatalf("submit %s: %+v %v", status, row, err)
		}
	}
	assertDenied := func(q string, args ...any) {
		t.Helper()
		err := st.nutritionTx(ctx, a, pgx.TxOptions{}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q, args...)
			return err
		})
		if err == nil {
			t.Fatalf("role allowed %s", q)
		}
	}
	assertDenied("UPDATE nutrition.foods SET quality='verified' WHERE id=$1", food.ID)
	if err := st.nutritionTx(ctx, a, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE nutrition.submissions SET status='approved' WHERE food_id=$1", food.ID)
		if tag.RowsAffected() != 0 {
			t.Fatal("submitted row became approved")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertDenied("INSERT INTO nutrition.foods(id,canonical_name,food_type,quality) VALUES ($1,'Shared','generic','verified')", uuid.New())
	assertDenied("INSERT INTO nutrition.foods(id,owner_user_id,canonical_name,food_type,quality) VALUES ($1,$2,'Other','custom','user_reported')", uuid.New(), b)
	var visible int
	if err := st.pool.QueryRow(ctx, "SELECT count(*) FROM nutrition.foods WHERE owner_user_id IS NOT NULL").Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("account scope survived commit: %d %v", visible, err)
	}
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.search_foods('private musli','de','DE',10,0)`).
		Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("unscoped search returned rows: %d %v", visible, err)
	}
	if _, err := root.Exec(ctx, "UPDATE nutrition.foods SET status='inactive' WHERE id=$1", fixtureApple); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Food(ctx, a, fixtureApple); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestNutritionBilingualTypeahead(t *testing.T) {
	ctx := context.Background()
	st, root, account, _ := nutritionTestStore(t)
	if _, err := root.Exec(ctx, `
DELETE FROM nutrition.food_names WHERE normalized='apfel' AND language='de';
WITH foods AS (
    INSERT INTO nutrition.foods(id,canonical_name,food_type,quality) VALUES
        (gen_random_uuid(),'Apple juice','generic','source_reported'),
        (gen_random_uuid(),'Apple drink','branded','source_reported')
    RETURNING id,canonical_name
), sources AS (
    INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
    SELECT id,id,'ryft',id::text,'typeahead','RYFT' FROM foods RETURNING id,food_id
)
INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized)
SELECT f.id,s.id,'en',f.canonical_name,lower(f.canonical_name) FROM foods f JOIN sources s ON s.food_id=f.id`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"Apf", nil},
		{"Apfe", []string{"Apple", "Apple juice"}},
		{"Apfel", []string{"Apple", "Apple juice"}},
		{"Apfels", []string{"Apple juice"}},
		{"Apple", []string{"Apple", "Apple drink", "Apple juice"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			page, err := st.SearchFoods(ctx, account, model.FoodSearch{Query: tc.query, Language: "de-DE", Market: "DE"})
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, food := range page.Foods {
				names = append(names, food.Name)
			}
			// English direct matches share the prefix rank and their relative order is not part of the contract.
			if tc.query == "Apple" {
				slices.Sort(names)
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("foods = %q, want %q", names, tc.want)
			}
		})
	}
}

func TestNutritionMealLifecycleAndGoals(t *testing.T) {
	ctx := context.Background()
	st, root, a, b := nutritionTestStore(t)
	at := time.Date(2026, 10, 24, 22, 30, 0, 0, time.UTC)
	in := model.NutritionMealInput{ClientRequestID: uuid.New(), ConsumedAt: at, Timezone: "Europe/Berlin", MealType: "snack", InputMethod: "manual_search",
		Items: []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: 150, Unit: "g"},
			{FoodID: uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db02"), Quantity: 10, Unit: "g"}}}
	bad := in
	bad.Items = append([]model.NutritionMealItemInput{}, in.Items...)
	bad.Items[1].FoodID = uuid.New()
	if _, _, err := st.CreateNutritionMeal(ctx, a, bad); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// The failed item's transaction left neither the meal nor its request key.
	type created struct {
		meal    model.NutritionMeal
		created bool
		err     error
	}
	ch := make(chan created, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			m, new, err := st.CreateNutritionMeal(ctx, a, in)
			ch <- created{m, new, err}
		})
	}
	wg.Wait()
	close(ch)
	var first model.NutritionMeal
	n := 0
	for x := range ch {
		if x.err != nil || x.meal.LocalDate != "2026-10-25" || len(x.meal.Items) != 2 {
			t.Fatalf("create or replay: %+v: %v", x.meal, x.err)
		}
		if x.created {
			n++
		}
		if first.ID != uuid.Nil && x.meal.ID != first.ID {
			t.Fatal("retry duplicated meal")
		}
		first = x.meal
	}
	if n != 1 {
		t.Fatalf("created %d", n)
	}
	if _, err := st.NutritionMeal(ctx, b, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.PatchNutritionMeal(ctx, b, first.ID, model.NutritionMealPatch{Revision: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := st.DeleteNutritionMeal(ctx, b, first.ID); err != nil {
		t.Fatal(err)
	}
	page, err := st.NutritionMeals(ctx, a, "2026-10-24", "2026-10-25", 1, 0)
	if err != nil || len(page.Meals) != 1 || len(page.Meals[0].Items) != 2 {
		t.Fatalf("list: %+v %v", page, err)
	}
	days, err := st.NutritionDaily(ctx, a, "2026-10-24", "2026-10-25", false)
	if err != nil || days[0].Meals != 0 || len(days[0].Nutrients) != 0 || days[1].Meals != 1 ||
		days[1].Nutrients["energy_kcal"].KnownSubtotal != 78 || days[1].Nutrients["energy_kcal"].Complete ||
		days[1].Nutrients["fat"].KnownItems != 1 || days[1].Nutrients["fat"].KnownSubtotal != 0 {
		t.Fatalf("expected partial day and explicit zero: %+v %v", days, err)
	}
	if _, err := root.Exec(ctx, "UPDATE nutrition.nutrient_values SET amount=70 WHERE food_id=$1 AND code='energy_kcal'", fixtureApple); err != nil {
		t.Fatal(err)
	}
	zone := "UTC"
	keep := []model.NutritionMealItemInput{{ItemID: &first.Items[0].ID}}
	updated, err := st.PatchNutritionMeal(ctx, a, first.ID, model.NutritionMealPatch{Revision: 1, Timezone: &zone, Items: &keep})
	if err != nil || updated.Revision != 2 || updated.LocalDate != "2026-10-24" || updated.Items[0].Nutrients["energy_kcal"].Amount != 78 {
		t.Fatalf("patch changed historical value: %+v %v", updated, err)
	}
	if _, err := st.PatchNutritionMeal(ctx, a, first.ID, model.NutritionMealPatch{Revision: 1}); !errors.Is(err, ErrNutritionConflict) {
		t.Fatal(err)
	}
	days, err = st.NutritionDaily(ctx, a, "2026-10-24", "2026-10-25", false)
	if err != nil || days[1].Meals != 0 || !days[0].Nutrients["energy_kcal"].Complete {
		t.Fatalf("move summary: %+v %v", days, err)
	}
	recompute := in.Items[:1]
	updated, err = st.PatchNutritionMeal(ctx, a, first.ID, model.NutritionMealPatch{Revision: 2, Items: &recompute})
	if err != nil || updated.Items[0].Nutrients["energy_kcal"].Amount != 105 {
		t.Fatalf("explicit recalc: %+v %v", updated, err)
	}
	if err := st.DeleteNutritionMeal(ctx, a, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateNutritionMeal(ctx, a, in); !errors.Is(err, ErrNutritionConflict) {
		t.Fatal(err)
	}
	days, err = st.NutritionDaily(ctx, a, "2026-10-24", "2026-10-25", false)
	if err != nil || days[0].Meals != 0 {
		t.Fatalf("delete summary: %+v %v", days, err)
	}
	targets := model.NutritionGoalInput{Targets: map[string]float64{"protein": 80, "water": 2000}}
	for range 2 {
		g, err := st.PutNutritionGoals(ctx, a, targets)
		if err != nil || g.Revision != 1 || g.Targets["protein"] != 80 {
			t.Fatalf("goal: %+v %v", g, err)
		}
	}
	if g, err := st.NutritionGoals(ctx, b); err != nil || g.Revision != 0 || len(g.Targets) != 0 {
		t.Fatalf("goals isolation: %+v %v", g, err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := root.QueryRow(ctx, `SELECT (SELECT count(*) FROM nutrition.goal_revisions WHERE user_id=$1)
		+ (SELECT count(*) FROM nutrition.meal_requests WHERE user_id=$1)`, a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("account deletion: %d %v", count, err)
	}
}

func TestNutritionImportIdempotencyAndRollback(t *testing.T) {
	ctx := context.Background()
	st, root, a, _ := nutritionTestStore(t)
	admin := New(root)
	file, err := os.Open("../nutrition/importer/testdata/off.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []nutrition.CatalogRecord
	err = importer.OFF(ctx, file, importer.Options{Version: "fixture-v1", Limit: 4, ChunkSize: 2, Markets: []string{"DE", "AT", "CH"}, EnergyTolerance: .35},
		func(r importer.Result) error {
			if r.Record != nil {
				records = append(records, *r.Record)
			}
			return nil
		})
	if err != nil || len(records) != 3 {
		t.Fatalf("fixture: %d %v", len(records), err)
	}
	start := func(id uuid.UUID, version string) nutrition.ImportJob {
		t.Helper()
		job, err := admin.StartNutritionImport(ctx, nutrition.ImportJob{ID: id, Provider: "off", Version: version})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	job := start(uuid.Nil, "fixture-v1")
	bad := append([]nutrition.CatalogRecord{}, records...)
	bad[2].Quality = "invalid"
	if _, err := admin.ApplyNutritionImport(ctx, job, 3, bad, nutrition.ImportStats{Processed: 3}); err == nil {
		t.Fatal("batch accepted bad row")
	}
	job = start(job.ID, "fixture-v1")
	if job.Checkpoint != 0 {
		t.Fatalf("advanced failed checkpoint: %+v", job)
	}
	miss, err := st.LookupFoodBarcode(ctx, a, model.FoodBarcodeQuery{Barcode: "012345678905"})
	if err != nil || len(miss.Matches) != 0 {
		t.Fatalf("partial transaction: %+v %v", miss, err)
	}
	for i := range 2 {
		if i > 0 {
			job = start(uuid.Nil, "fixture-v1")
		}
		job, err = admin.ApplyNutritionImport(ctx, job, 3, records, nutrition.ImportStats{Processed: 3})
		if err != nil || job.Checkpoint != 3 || (i == 0 && job.Counts.Inserted != 3) || (i == 1 && job.Counts.Unchanged != 3) {
			t.Fatalf("import pass %d: %+v %v", i, job, err)
		}
		if err := admin.FinishNutritionImport(ctx, job, ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.LookupFoodBarcode(ctx, a, model.FoodBarcodeQuery{Barcode: "0012345678905", Market: "DE"})
	if err != nil || !got.Conflicted || len(got.Matches) != 2 || got.Matches[0].Name != "Test-Müsli" {
		t.Fatalf("barcode: %+v %v", got, err)
	}
	update := records[0]
	update.Version, update.Name = "fixture-v2", "New name"
	job = start(uuid.Nil, "fixture-v2")
	job, err = admin.ApplyNutritionImport(ctx, job, 1, []nutrition.CatalogRecord{update}, nutrition.ImportStats{Processed: 1})
	if err != nil || job.Counts.Updated != 1 {
		t.Fatalf("update: %+v %v", job, err)
	}
	food, err := st.Food(ctx, a, got.Matches[0].ID)
	if err != nil || food.Name != "New name" || food.Sources[0].DatasetVersion != "fixture-v2" {
		t.Fatalf("updated source: %+v %v", food, err)
	}
}

func TestNutritionDailyWithoutConsentSkipsScoreInputs(t *testing.T) {
	ctx := context.Background()
	st, root, owner, _ := nutritionTestStore(t)
	const policy = "2026-10-26"
	const day = "2026-11-24"
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if _, _, err := st.CreateNutritionMeal(ctx, owner, model.NutritionMealInput{
		ClientRequestID: uuid.New(), ConsumedAt: at, Timezone: "UTC", MealType: "lunch", InputMethod: "manual_search",
		Items: []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: 150, Unit: "g"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutNutritionGlucose(ctx, owner, []model.NutritionGlucoseSample{
		{Timestamp: at, GlucoseMgDL: 100}, {Timestamp: at.Add(30 * time.Minute), GlucoseMgDL: 100},
		{Timestamp: at.Add(time.Hour), GlucoseMgDL: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, owner, policy, nil); err != nil {
		t.Fatal(err)
	}
	days, err := st.NutritionDailyWithN2R(ctx, owner, day, day, true, at, policy)
	if err != nil || len(days) != 1 || days[0].GlucoseScore == nil {
		t.Fatalf("expected valid CGM before withdrawal: %+v %v", days, err)
	}
	if err := st.RevokeConsent(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// In this disposable database, any score-only read of CGM would now fail.
	if _, err := root.Exec(ctx, `REVOKE SELECT ON nutrition.glucose_samples FROM ryft_api`); err != nil {
		t.Fatal(err)
	}
	days, err = st.NutritionDailyWithN2R(ctx, owner, day, day, true, at, policy)
	if err != nil || len(days) != 1 || days[0].Meals != 1 || days[0].GlucoseScore != nil {
		t.Fatalf("expected totals without reading CGM for scoring: %+v %v", days, err)
	}
}

func TestNutritionGlucoseExecModeAndAccountDeletion(t *testing.T) {
	ctx := context.Background()
	st, root, a, b := nutritionTestStore(t)
	sample := model.NutritionGlucoseSample{Timestamp: time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC), GlucoseMgDL: 100}
	if err := st.PutNutritionGlucose(ctx, a, []model.NutritionGlucoseSample{sample}); err != nil {
		t.Fatal(err)
	}
	sample.GlucoseMgDL = 125
	if err := st.PutNutritionGlucose(ctx, a, []model.NutritionGlucoseSample{sample}); err != nil {
		t.Fatal(err)
	}
	if err := st.nutritionTx(ctx, b, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM nutrition.glucose_samples WHERE user_id=$1", a).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("CGM data visible to the wrong account")
		}
		tag, err := tx.Exec(ctx, "DELETE FROM nutrition.glucose_samples WHERE user_id=$1", a)
		if tag.RowsAffected() != 0 {
			t.Fatal("CGM data deleted by the wrong account")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var updated float64
	if err := root.QueryRow(ctx, "SELECT glucose_mg_dl FROM nutrition.glucose_samples WHERE user_id=$1", a).Scan(&updated); err != nil || updated != 125 {
		t.Fatalf("CGM correction: %g, %v", updated, err)
	}
	if has, err := st.HasNutritionData(ctx, a); err != nil || !has {
		t.Fatalf("CGM must prevent an account merge: %v, %v", has, err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := root.QueryRow(ctx, "SELECT count(*) FROM nutrition.glucose_samples WHERE user_id=$1", a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("account deletion left CGM samples: %d, %v", count, err)
	}
}
