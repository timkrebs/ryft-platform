package store

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

type rankingName struct {
	language, label string
	alias           bool
}

type rankingCatalog struct {
	milk35, milk15, apple, juice, applesauce, milkRice uuid.UUID
}

// seedRankingCatalog stores the bundled staples (scripts/etl/staples.v1.json) with the
// names, aliases, groups and forms the importer writes. The seed apple becomes the apple
// staple; the rest is inserted here.
func seedRankingCatalog(t *testing.T, root *pgxpool.Pool) rankingCatalog {
	t.Helper()
	ctx := context.Background()
	add := func(canonical, brand, foodType, group, form string, staple bool, names ...rankingName) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.foods(id,canonical_name,brand,food_type,quality,markets,
				is_canonical_staple,canonical_group_key,food_form)
			VALUES ($1,$2,$3,$4,'source_reported',ARRAY['DE'],$5,NULLIF($6,''),NULLIF($7,''))`,
			id, canonical, brand, foodType, staple, group, form); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
			VALUES ($1,$1,'ryft',$2,'ranking-fixture','RYFT')`, id, id.String()); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized,normalized_brand,alias)
				VALUES ($1,$1,$2,$3,$4,$5,$6)`, id, n.language, n.label, nutrition.Normalize(n.label),
				nutrition.Normalize(brand), n.alias); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	if _, err := root.Exec(ctx, `UPDATE nutrition.foods SET is_canonical_staple=true,canonical_group_key='apple',
		food_form='whole_raw' WHERE id=$1`, fixtureApple); err != nil {
		t.Fatal(err)
	}
	return rankingCatalog{
		apple: fixtureApple,
		milk35: add("Whole milk 3.5%", "", "generic", "milk", "liquid", true,
			rankingName{"de", "Vollmilch 3,5 %", false}, rankingName{"en", "Whole milk 3.5%", false},
			rankingName{"de", "Milch", true}, rankingName{"de", "Vollmilch", true}, rankingName{"de", "Milch 3,5 Prozent", true},
			rankingName{"en", "Milk", true}, rankingName{"en", "Whole milk", true}),
		milk15: add("Low-fat milk 1.5%", "", "generic", "milk", "liquid", true,
			rankingName{"de", "Fettarme Milch 1,5 %", false}, rankingName{"en", "Low-fat milk 1.5%", false},
			rankingName{"de", "Fettarme Milch", true}, rankingName{"de", "Milch 1,5 Prozent", true},
			rankingName{"en", "Low-fat milk", true}, rankingName{"en", "Semi-skimmed milk", true}),
		juice: add("Apple juice", "", "generic", "apple", "juice", false,
			rankingName{"de", "Apfelsaft", false}, rankingName{"en", "Apple juice", false},
			rankingName{"de", "Apfelsaft ungesüßt", true}, rankingName{"en", "Unsweetened apple juice", true}),
		applesauce: add("Apfelmus Marke X", "Marke X", "branded", "", "", false,
			rankingName{"de", "Apfelmus Marke X", false}),
		milkRice: add("Milchreis Marke X", "Marke X", "branded", "", "", false,
			rankingName{"de", "Milchreis Marke X", false}),
	}
}

func TestNutritionRankingGroupsAndVariants(t *testing.T) {
	ctx := context.Background()
	st, root, user, _ := nutritionTestStore(t)
	c := seedRankingCatalog(t, root)
	search := func(query string) []uuid.UUID {
		t.Helper()
		page, err := st.SearchFoods(ctx, user, model.FoodSearch{Query: query, Language: "de-DE", Market: "DE", Limit: 20})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		ids := make([]uuid.UUID, len(page.Foods))
		for i, food := range page.Foods {
			ids[i] = food.ID
		}
		return ids
	}
	favorite := func(ids ...uuid.UUID) {
		t.Helper()
		if _, err := root.Exec(ctx, "DELETE FROM nutrition.food_favorites WHERE user_id=$1", user); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if err := st.PutFoodFavorite(ctx, user, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	ahead := func(query string, ids []uuid.UUID, first, second uuid.UUID) {
		t.Helper()
		i, j := slices.Index(ids, first), slices.Index(ids, second)
		if i < 0 || j < 0 || i > j {
			t.Errorf("%q: want %s ahead of %s, positions %d and %d", query, first, second, i, j)
		}
	}

	// (a) An exact variant alias is a specific query: the favorite 3.5 % milk must not win.
	favorite(c.milk35)
	if ids := search("Milch 1,5 Prozent"); len(ids) == 0 || ids[0] != c.milk15 {
		t.Errorf("exact variant alias lost to a favorite of another variant: %v", ids)
	}

	// (b) A broad query promotes the whole group, and personalization orders its staples.
	favorite(c.milk15)
	ids := search("Milch")
	if len(ids) < 3 || ids[0] != c.milk15 || ids[1] != c.milk35 {
		t.Errorf("broad milk query with favorite 1.5 %%: %v", ids)
	}
	ahead("Milch", ids, c.milk35, c.milkRice)
	favorite(c.milk35)
	if ids := search("Milch"); len(ids) < 2 || ids[0] != c.milk35 || ids[1] != c.milk15 {
		t.Errorf("broad milk query with favorite 3.5 %%: %v", ids)
	}
	if ids := search("milk"); len(ids) < 2 || ids[0] != c.milk35 || ids[1] != c.milk15 {
		t.Errorf("English alias of the group: %v", ids)
	}

	// (c) The group's other forms rank above unrelated brand prefixes.
	favorite()
	ids = search("Apfel")
	if len(ids) == 0 || ids[0] != c.apple {
		t.Errorf("broad apple query: %v", ids)
	}
	if !slices.Contains(ids, c.applesauce) {
		t.Fatalf("the branded Apfelmus is no candidate; the ordering check below would pass vacuously: %v", ids)
	}
	ahead("Apfel", ids, c.juice, c.applesauce)
	favorite(c.applesauce)
	ahead("Apfel", search("Apfel"), c.juice, c.applesauce) // a favorite does not lift a lower tier

	// (d) An explicit juice query is an exact match and leads.
	favorite(c.applesauce, c.apple)
	if ids := search("Apfelsaft"); len(ids) == 0 || ids[0] != c.juice {
		t.Errorf("juice query: %v", ids)
	}
}
