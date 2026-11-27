package store

import (
	"context"
	"fmt"
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
	// A single-word exact variant name is specific too, although it is a prefix of a staple name.
	favorite(c.milk15)
	if ids := search("Vollmilch"); len(ids) == 0 || ids[0] != c.milk35 {
		t.Errorf("single-word exact variant lost to a favorite of another variant: %v", ids)
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

// TestNutritionRankingBLSGerman pins the order the full BLS 4.0 import (D296) must keep. The
// markets are the production ones: curated staples and SR Legacy rows have none, BLS rows have DE.
// BLS rows have no group key and are no staples. The "Möhre" case needs 0061: a BLS exact name and
// a staple's exact alias tie at tier 7, and the staple has to win before preferred_market is read.
func TestNutritionRankingBLSGerman(t *testing.T) {
	ctx := context.Background()
	st, root, user, _ := nutritionTestStore(t)
	sources := 0
	add := func(provider, canonical, group, form string, markets []string, names ...rankingName) uuid.UUID {
		t.Helper()
		id := uuid.New()
		staple := group != ""
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.foods(id,canonical_name,food_type,quality,markets,
				is_canonical_staple,canonical_group_key,food_form)
			VALUES ($1,$2,'generic','source_reported',$3,$4,NULLIF($5,''),NULLIF($6,''))`,
			id, canonical, markets, staple, group, form); err != nil {
			t.Fatal(err)
		}
		sources++
		external, license, attribution := id.String(), "USDA-PUBLIC-DOMAIN", (*string)(nil)
		if provider == "bls" { // nutrition_bls_attribution: a BLS code, CC-BY-4.0 and the named publisher
			external, license, attribution = fmt.Sprintf("T%06d", sources), "CC-BY-4.0", ptr("Max Rubner-Institut")
		}
		if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,
				license_identifier,attribution) VALUES ($1,$1,$2,$3,'ranking-fixture',$4,$5)`,
			id, provider, external, license, attribution); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized,alias)
				VALUES ($1,$1,$2,$3,$4,$5)`, id, n.language, n.label, nutrition.Normalize(n.label), n.alias); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	if _, err := root.Exec(ctx, `UPDATE nutrition.foods SET is_canonical_staple=true,canonical_group_key='apple',
		food_form='whole_raw' WHERE id=$1`, fixtureApple); err != nil {
		t.Fatal(err)
	}
	none, de := []string{}, []string{"DE"}
	de1 := func(label string) rankingName { return rankingName{"de", label, false} }
	en1 := func(label string) rankingName { return rankingName{"en", label, false} }
	alias := func(language string, labels ...string) []rankingName {
		out := make([]rankingName, len(labels))
		for i, label := range labels {
			out[i] = rankingName{language, label, true}
		}
		return out
	}
	names := func(preferred []rankingName, aliases ...[]rankingName) []rankingName {
		for _, a := range aliases {
			preferred = append(preferred, a...)
		}
		return preferred
	}
	carrot := add("usda", "Carrot", "carrot", "whole_raw", none, names(
		[]rankingName{de1("Karotte"), en1("Carrot")},
		alias("de", "Karotten", "Möhre", "Möhren", "Mohrrübe", "Rüebli"), alias("en", "Carrots"))...)
	cucumber := add("usda", "Cucumber", "cucumber", "whole_raw", none, names(
		[]rankingName{de1("Salatgurke"), en1("Cucumber")},
		alias("de", "Gurke", "Gurken", "Salatgurken"), alias("en", "Cucumbers"))...)
	blsCarrot := add("bls", "Möhre", "", "", de, de1("Möhre"), en1("Carrot"))
	blsCarrotRaw := add("bls", "Möhre, roh", "", "raw", de, names(
		[]rankingName{de1("Möhre, roh"), en1("Carrot, raw")}, alias("de", "Karotte, roh"))...)
	blsCucumber := add("bls", "Gurke, roh", "", "raw", de, de1("Gurke, roh"), en1("Cucumber, raw"))
	blsLentils := add("bls", "Linsen, getrocknet", "", "", de, de1("Linsen, getrocknet"), en1("Lentils, dried"))
	blsLentilSoup := add("bls", "Linsensuppe", "", "", de, de1("Linsensuppe"), en1("Lentil soup"))
	blsApple := add("bls", "Apfel, roh", "", "raw", de, de1("Apfel, roh"), en1("Apple, raw"))
	srLentils := add("usda", "Lentils, raw", "", "", none, en1("Lentils, raw"))
	srCucumber := add("usda", "Cucumber, with peel, raw", "", "", none, en1("Cucumber, with peel, raw"))
	srApples := add("usda", "Apples, raw, with skin", "", "", none, en1("Apples, raw, with skin"))
	add("usda", "Carrots, raw", "", "", none, en1("Carrots, raw")) // a candidate for "Karotte" (tier 2), never first

	search := func(query, language, market string) []uuid.UUID {
		t.Helper()
		page, err := st.SearchFoods(ctx, user, model.FoodSearch{Query: query, Language: language, Market: market, Limit: 30})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		ids := make([]uuid.UUID, len(page.Foods))
		for i, food := range page.Foods {
			ids[i] = food.ID
		}
		return ids
	}
	first := func(query, language, market string, want uuid.UUID) []uuid.UUID {
		t.Helper()
		ids := search(query, language, market)
		if len(ids) == 0 || ids[0] != want {
			t.Errorf("%q (%s, %s): want %s first, got %v", query, language, market, want, ids)
		}
		return ids
	}
	ahead := func(query string, ids []uuid.UUID, before, after uuid.UUID) {
		t.Helper()
		i, j := slices.Index(ids, before), slices.Index(ids, after)
		if i < 0 || j < 0 || i > j {
			t.Errorf("%q: want %s ahead of %s, positions %d and %d", query, before, after, i, j)
		}
	}

	// German, market DE.
	ids := first("Gurke", "de-DE", "DE", cucumber) // the group is named: the staple is tier 10
	ahead("Gurke", ids, blsCucumber, srCucumber)   // BLS (tier 4) before the SR row, reached by the alternate (tier 2)

	ids = first("Linsen", "de-DE", "DE", blsLentils) // no staple: BLS prefix matches (tier 4) before SR (tier 2)
	ahead("Linsen", ids, blsLentils, srLentils)
	ahead("Linsen", ids, blsLentilSoup, srLentils)

	ids = first("Apfel", "de-DE", "DE", fixtureApple)
	ahead("Apfel", ids, blsApple, srApples)
	if !slices.Contains(ids, blsApple) {
		t.Errorf("BLS Apfel, roh is no result: %v", ids)
	}

	first("Karotte", "de-DE", "DE", carrot) // the group is named by the alternate "carrot"

	// An exact BLS name against an exact staple alias: tier 7 for both. Fails on 0059, where
	// preferred_market (BLS has DE, the staple has none) decides before the staple flag.
	ids = first("Möhre", "de-DE", "DE", carrot)
	if len(ids) < 2 || ids[1] != blsCarrot {
		t.Errorf("Möhre: want the BLS Möhre second, got %v", ids)
	}
	ahead("Möhre", ids, blsCarrot, blsCarrotRaw)

	// English: the BLS English names compete with SR as equals; market DE breaks the tie for BLS.
	ids = search("lentils", "en-US", "DE")
	ahead("lentils (DE)", ids, blsLentils, srLentils)
	ids = search("lentils", "en-US", "US")
	for _, id := range []uuid.UUID{blsLentils, srLentils} {
		if !slices.Contains(ids, id) {
			t.Errorf("lentils (US): %s missing from %v", id, ids)
		}
	}
	first("apple", "en-US", "DE", fixtureApple)
}
