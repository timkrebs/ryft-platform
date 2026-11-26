package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

// itemRow reads one meal item as the database stores it, as canonical jsonb text.
func itemRow(t *testing.T, root *pgxpool.Pool, id uuid.UUID, withPosition bool) string {
	t.Helper()
	q := `SELECT to_jsonb(i) - 'position' FROM nutrition.meal_items i WHERE id=$1`
	if withPosition {
		q = `SELECT to_jsonb(i) FROM nutrition.meal_items i WHERE id=$1`
	}
	var raw string
	if err := root.QueryRow(context.Background(), q, id).Scan(&raw); err != nil {
		t.Fatalf("read item %s: %v", id, err)
	}
	return raw
}

// A meal PATCH copies retained item rows as stored (D285): columns and source fields this
// build does not model survive, and only the position moves.
func TestNutritionPatchCopiesRetainedItemsAsStored(t *testing.T) {
	ctx := context.Background()
	st, root, a, _ := nutritionTestStore(t)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	created, _, err := st.CreateNutritionMeal(ctx, a, model.NutritionMealInput{
		ClientRequestID: uuid.New(), ConsumedAt: at, Timezone: "Europe/Berlin", MealType: "snack", InputMethod: "manual_search",
		Items: []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: 150, Unit: "g"},
			{FoodID: uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db02"), Quantity: 10, Unit: "g"}}})
	if err != nil || len(created.Items) != 2 {
		t.Fatalf("create: %+v %v", created, err)
	}
	item1, item2 := created.Items[0].ID, created.Items[1].ID

	// What the next release writes: portion labels and a source with fields the typed model lacks.
	sources := `[{"provider":"bls","external_id":"M111200","license_identifier":"CC-BY-4.0",
		"attribution":"Max Rubner-Institut, Bundeslebensmittelschluessel","x_future":{"k":1}}]`
	if _, err := root.Exec(ctx, `UPDATE nutrition.meal_items SET portion_label_snapshot=$1,
		portion_short_label_snapshot=$2, sources=$3::jsonb WHERE id=$4`,
		"1 Glas Milch (200 ml)", "Glas", sources, item1); err != nil {
		t.Fatal(err)
	}
	before := itemRow(t, root, item1, false)

	notes := "evening"
	patch := []model.NutritionMealItemInput{{FoodID: fixtureApple, Quantity: 50, Unit: "g"}, {ItemID: &item1}}
	updated, err := st.PatchNutritionMeal(ctx, a, created.ID, model.NutritionMealPatch{Revision: created.Revision, Notes: &notes, Items: &patch})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.Revision != created.Revision+1 || updated.Notes != notes || len(updated.Items) != 2 || updated.Items[1].ID != item1 {
		t.Fatalf("patch result: %+v", updated)
	}
	newID := updated.Items[0].ID

	if after := itemRow(t, root, item1, false); after != before {
		t.Fatalf("retained item changed:\nbefore %s\nafter  %s", before, after)
	}
	var position, count int
	var attribution string
	var future int
	if err := root.QueryRow(ctx, `SELECT position, sources->0->>'attribution', (sources->0->'x_future'->>'k')::int
		FROM nutrition.meal_items WHERE id=$1`, item1).Scan(&position, &attribution, &future); err != nil {
		t.Fatal(err)
	}
	if position != 1 || attribution == "" || future != 1 {
		t.Fatalf("retained item: position %d attribution %q x_future %d", position, attribution, future)
	}
	if err := root.QueryRow(ctx, `SELECT count(*) FROM nutrition.meal_items WHERE meal_id=$1`, created.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("items after patch: %d %v", count, err)
	}
	if err := root.QueryRow(ctx, `SELECT count(*) FROM nutrition.meal_items WHERE id=$1`, item2).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed item still stored: %d %v", count, err)
	}
	var label, short *string
	var newPosition int
	if err := root.QueryRow(ctx, `SELECT position, portion_label_snapshot, portion_short_label_snapshot
		FROM nutrition.meal_items WHERE id=$1`, newID).Scan(&newPosition, &label, &short); err != nil {
		t.Fatal(err)
	}
	if newPosition != 0 || label != nil || short != nil {
		t.Fatalf("new item: position %d labels %v %v", newPosition, label, short)
	}

	// Retained items only, in their current order: every row stays as it is, position included.
	rows := map[uuid.UUID]string{item1: itemRow(t, root, item1, true), newID: itemRow(t, root, newID, true)}
	same := []model.NutritionMealItemInput{{ItemID: &newID}, {ItemID: &item1}}
	again, err := st.PatchNutritionMeal(ctx, a, created.ID, model.NutritionMealPatch{Revision: updated.Revision, Items: &same})
	if err != nil || again.Revision != updated.Revision+1 || len(again.Items) != 2 || again.Items[0].ID != newID || again.Items[1].ID != item1 {
		t.Fatalf("second patch: %+v %v", again, err)
	}
	for id, want := range rows {
		if got := itemRow(t, root, id, true); got != want {
			t.Fatalf("item %s changed by a PATCH of retained items:\nbefore %s\nafter  %s", id, want, got)
		}
	}
}
