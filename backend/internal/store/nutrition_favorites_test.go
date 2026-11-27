package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

var fixtureOats = uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db02")

// A favourite alone is data a merge would delete, so it blocks the merge.
func TestHasNutritionDataCountsFavoritesOnly(t *testing.T) {
	ctx := context.Background()
	st, _, a, b := nutritionTestStore(t)
	if has, err := st.HasNutritionData(ctx, a); err != nil || has {
		t.Fatalf("empty account: has=%v err=%v", has, err)
	}
	if err := st.PutFoodFavorite(ctx, a, fixtureApple); err != nil {
		t.Fatal(err)
	}
	if has, err := st.HasNutritionData(ctx, a); err != nil || !has {
		t.Fatalf("a favourite must prevent an account merge: has=%v err=%v", has, err)
	}
	if has, err := st.HasNutritionData(ctx, b); err != nil || has {
		t.Fatalf("another account's favourite must not count: has=%v err=%v", has, err)
	}
	if err := st.DeleteFoodFavorite(ctx, a, fixtureApple); err != nil {
		t.Fatal(err)
	}
	if has, err := st.HasNutritionData(ctx, a); err != nil || has {
		t.Fatalf("after removing the favourite: has=%v err=%v", has, err)
	}
}

func TestExportNutritionMealsFavorites(t *testing.T) {
	ctx := context.Background()
	st, root, a, b := nutritionTestStore(t)

	// No favourites: the first page carries an empty list, not an omitted one.
	page, err := st.ExportNutritionMeals(ctx, a, uuid.Nil, 20)
	if err != nil || page.Favorites == nil || len(page.Favorites) != 0 {
		t.Fatalf("first page without favourites: %+v %v", page.Favorites, err)
	}

	// Two favourites with fixed times; the order is by time, then food.
	older := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)
	for food, at := range map[uuid.UUID]time.Time{fixtureOats: newer, fixtureApple: older} {
		if err := st.PutFoodFavorite(ctx, a, food); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Exec(ctx, `UPDATE nutrition.food_favorites SET created_at=$3 WHERE user_id=$1 AND food_id=$2`,
			a, food, at); err != nil {
			t.Fatal(err)
		}
	}
	// A favourite of another account never appears in a's export.
	if err := st.PutFoodFavorite(ctx, b, uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db03")); err != nil {
		t.Fatal(err)
	}
	// An inactive food still exports its ID.
	if _, err := root.Exec(ctx, `UPDATE nutrition.foods SET status='inactive' WHERE id=$1`, fixtureOats); err != nil {
		t.Fatal(err)
	}

	page, err = st.ExportNutritionMeals(ctx, a, uuid.Nil, 20)
	if err != nil || len(page.Favorites) != 2 {
		t.Fatalf("first page: %+v %v", page.Favorites, err)
	}
	if f := page.Favorites[0]; f.FoodID != fixtureApple || f.FoodName != "Apple" || !f.FavoritedAt.Equal(older) || f.FavoritedAt.Location() != time.UTC {
		t.Fatalf("first favourite: %+v", f)
	}
	if f := page.Favorites[1]; f.FoodID != fixtureOats || f.FoodName != "Rolled oats" || !f.FavoritedAt.Equal(newer) {
		t.Fatalf("second favourite: %+v", f)
	}

	// A page after the cursor leaves the field out.
	page, err = st.ExportNutritionMeals(ctx, a, uuid.New(), 20)
	if err != nil || page.Favorites != nil {
		t.Fatalf("later page must omit favourites: %+v %v", page.Favorites, err)
	}

	// Another account sees only its own.
	page, err = st.ExportNutritionMeals(ctx, b, uuid.Nil, 20)
	if err != nil || len(page.Favorites) != 1 || page.Favorites[0].FoodName != "Chicken breast" {
		t.Fatalf("other account: %+v %v", page.Favorites, err)
	}
}
