package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

func TestNutritionPortionSelectionsAndHistory(t *testing.T) {
	ctx := context.Background()
	st, root, a, b := nutritionTestStore(t)
	medium := uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db04")
	if _, err := root.Exec(ctx, `
UPDATE nutrition.foods SET is_canonical_staple=true,canonical_group_key='apple',food_form='whole_raw',
    subtitles='{"de":"Mit Schale, roh","en":"Raw, with skin"}' WHERE canonical_name='Apple';
UPDATE nutrition.portions SET weight_g=182,portion_key='medium',short_label_de='Mittel',short_label_en='Medium',
    labels='{"de":"Ein mittelgroßer Apfel","en":"One medium apple"}',is_default=true,sort_order=20 WHERE label='Medium apple';
INSERT INTO nutrition.portions(id,food_id,source_id,label,quantity,unit,weight_g,portion_key,sort_order,short_label_de)
SELECT gen_random_uuid(),f.id,f.id,option.label,1,'portion',option.grams,option.key,option.position,option.de
FROM nutrition.foods f CROSS JOIN (VALUES
    ('Small apple',149,'small',10,'Klein'),('Large apple',223,'large',30,'Groß')
) option(label,grams,key,position,de) WHERE f.canonical_name='Apple';
WITH foods AS (
    INSERT INTO nutrition.foods(id,canonical_name,food_type,quality,canonical_group_key,food_form)
    SELECT md5(label)::uuid,label,'generic','source_reported','apple',form FROM (VALUES
        ('Apple slices','whole_food'),('Apple pieces','whole_food'),
        ('Apple juice','juice'),('Apples raw Fuji','cultivar')
    ) v(label,form) RETURNING id,canonical_name
), sources AS (
    INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
    SELECT id,id,'ryft',id::text,'selection-fixture','RYFT' FROM foods RETURNING id,food_id
)
INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized)
SELECT f.id,s.id,'en',f.canonical_name,lower(f.canonical_name) FROM foods f JOIN sources s ON f.id=s.food_id`); err != nil {
		t.Fatal(err)
	}
	lookup := func(label string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := root.QueryRow(ctx, "SELECT id FROM nutrition.foods WHERE canonical_name=$1", label).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pieces, slicesID, juice, cultivar := lookup("Apple pieces"), lookup("Apple slices"), lookup("Apple juice"), lookup("Apples raw Fuji")
	search := func(account uuid.UUID, query string, limit, offset int) model.FoodSearchPage {
		t.Helper()
		page, err := st.SearchFoods(ctx, account, model.FoodSearch{Query: query, Language: "de-DE", Market: "DE", Limit: limit, Offset: offset})
		if err != nil || len(page.Foods) == 0 {
			t.Fatalf("search %q: foods=%+v err=%v", query, page.Foods, err)
		}
		return page
	}
	for _, id := range []uuid.UUID{juice, pieces, pieces} {
		if err := st.PutFoodFavorite(ctx, a, id); err != nil {
			t.Fatal(err)
		}
	}
	page := search(a, "Apple", 2, 0)
	apple := page.Foods[0]
	if len(page.Foods) != 2 || apple.ID != fixtureApple || page.Foods[1].ID != pieces || !isFavorite(page.Foods[1]) ||
		apple.Subtitle != "Mit Schale, roh" || apple.DefaultPortion == nil || apple.DefaultPortion.PortionID == nil ||
		*apple.DefaultPortion.PortionID != medium || apple.DefaultPortion.ShortLabel != "Mittel" ||
		apple.DefaultPortion.GramWeight == nil || apple.DefaultPortion.Kcal == nil ||
		*apple.DefaultPortion.GramWeight != 182 || *apple.DefaultPortion.Kcal != 94.64 ||
		apple.ReferencePortion == nil || apple.ReferencePortion.GramWeight == nil ||
		*apple.ReferencePortion.GramWeight != 100 || apple.LastUsedPortion != nil {
		t.Fatalf("unexpected starter selections: %+v", page.Foods)
	}
	if page.NextOffset == nil || *page.NextOffset != 2 || search(a, "Apple", 2, *page.NextOffset).Foods[0].ID == fixtureApple {
		t.Fatal("unstable offset")
	}
	if search(a, "Apfelsaft", 1, 0).Foods[0].ID != juice || search(a, "Apfel Fuji", 1, 0).Foods[0].ID != cultivar {
		t.Fatal("explicit juice or cultivar intent was displaced")
	}
	if search(a, "Apfe", 1, 0).Foods[0].ID != fixtureApple {
		t.Fatal("German staple prefix no longer prioritizes whole apple")
	}
	detail, err := st.FoodForLanguage(ctx, a, fixtureApple, "de-DE")
	if err != nil || detail.Name != "Apfel" || len(detail.Portions) != 3 || detail.Portions[0].PortionKey != "small" ||
		detail.Portions[1].PortionKey != "medium" || detail.Portions[2].PortionKey != "large" {
		t.Fatalf("portion order: %+v, %v", detail.Portions, err)
	}
	mealInput := model.NutritionMealInput{ClientRequestID: uuid.New(), ConsumedAt: time.Now().UTC(),
		Timezone: "UTC", MealType: "snack", InputMethod: "manual_search",
		Items: []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: .5, Unit: "portion", Portion: &medium, Language: "de-DE"}}}
	meal, _, err := st.CreateNutritionMeal(ctx, a, mealInput)
	if err != nil {
		t.Fatal(err)
	}
	item := meal.Items[0]
	if item.FoodName != "Apfel" || item.PortionLabel == nil || *item.PortionLabel != "Ein mittelgroßer Apfel" ||
		item.PortionShortLabel == nil || *item.PortionShortLabel != "Mittel" || *item.WeightG != 91 {
		t.Fatalf("incorrect saved portion: %+v", item)
	}
	other := search(b, "Apple", 20, 0)
	if slices.ContainsFunc(other.Foods, func(food model.FoodSummary) bool { return isFavorite(food) || food.LastUsedPortion != nil }) {
		t.Fatal("another account received a preference or saved selection")
	}
	previous := search(a, "Apple", 1, 0).Foods[0].LastUsedPortion
	if previous == nil || previous.PortionID == nil || *previous.PortionID != medium || previous.Quantity != .5 {
		t.Fatalf("missing last-used portion: %+v", previous)
	}
	if _, err := root.Exec(ctx, "UPDATE nutrition.portions SET labels='{\"de\":\"Neu\"}',weight_g=190 WHERE id=$1", medium); err != nil {
		t.Fatal(err)
	}
	previous = search(a, "Apple", 1, 0).Foods[0].LastUsedPortion
	if previous == nil || previous.PortionID != nil || previous.Unit != "g" || previous.Quantity != 91 {
		t.Fatalf("changed measures must preserve old grams on reuse: %+v", previous)
	}
	if _, err := root.Exec(ctx, "DELETE FROM nutrition.portions WHERE id=$1", medium); err != nil {
		t.Fatal(err)
	}
	keep := []model.NutritionMealItemInput{{ItemID: &item.ID}}
	patched, err := st.PatchNutritionMeal(ctx, a, meal.ID, model.NutritionMealPatch{Revision: meal.Revision, Items: &keep})
	if err != nil || patched.Items[0].FoodName != item.FoodName || *patched.Items[0].PortionLabel != *item.PortionLabel ||
		*patched.Items[0].PortionShortLabel != *item.PortionShortLabel ||
		*patched.Items[0].WeightG != 91 || patched.Items[0].Nutrients["energy_kcal"].Amount != 47.32 {
		t.Fatalf("retired portion changed the historical snapshot: %+v %v", patched, err)
	}
	mealInput.ClientRequestID = uuid.New()
	if _, _, err := st.CreateNutritionMeal(ctx, a, mealInput); !errors.Is(err, nutrition.ErrInvalid) {
		t.Fatalf("retired ID still saves: %v", err)
	}
	detail, err = st.FoodForLanguage(ctx, a, fixtureApple, "de")
	if err != nil || detail.LastUsedSelection == nil || detail.LastUsedSelection.PortionID != nil || detail.LastUsedSelection.Quantity != 91 {
		t.Fatalf("detail falls back to safe last-used grams: %+v %v", detail.LastUsedSelection, err)
	}
	if _, err := root.Exec(ctx, "UPDATE nutrition.meal_items SET created_at=now()-interval '91 days' WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	if search(a, "Apple", 1, 0).Foods[0].LastUsedPortion != nil {
		t.Fatal("selection from outside the 90-day window was reused")
	}
	if err := st.DeleteFoodFavorite(ctx, b, pieces); err != nil {
		t.Fatal(err)
	}
	if !isFavorite(search(a, "Apple pieces", 1, 0).Foods[0]) {
		t.Fatal("another user removed the favorite")
	}
	if err := st.DeleteFoodFavorite(ctx, a, pieces); err != nil {
		t.Fatal(err)
	}
	if isFavorite(search(a, "Apple pieces", 1, 0).Foods[0]) {
		t.Fatal("favorite was not removed")
	}
	for _, id := range []uuid.UUID{pieces, pieces, slicesID} {
		mealInput.ClientRequestID = uuid.New()
		mealInput.Items = []model.NutritionMealItemInput{{FoodID: id, Quantity: 100, Unit: "g"}}
		if _, _, err := st.CreateNutritionMeal(ctx, a, mealInput); err != nil {
			t.Fatal(err)
		}
	}
	if search(a, "Apple", 2, 0).Foods[1].ID != pieces {
		t.Fatal("the higher 90-day use count did not rank first among equally relevant foods")
	}
	if err := st.nutritionTx(ctx, a, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO nutrition.food_favorites(user_id,food_id) VALUES($1,$2)", b, fixtureApple)
		return err
	}); err == nil {
		t.Fatal("RLS allowed a favorite for another account")
	}
}

func isFavorite(food model.FoodSummary) bool {
	return food.Favorited != nil && *food.Favorited
}
