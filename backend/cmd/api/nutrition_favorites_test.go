package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func addCatalogFood(t *testing.T, exec func(context.Context, string, ...any) error, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := exec(context.Background(), `INSERT INTO nutrition.foods(id,canonical_name,food_type,quality)
		VALUES ($1,$2,'generic','source_reported')`, id, name); err != nil {
		t.Fatal(err)
	}
	return id
}

// The merge guard counts favourites: a source account whose only Nutrition data
// is a favourite, with both flags off, is refused and keeps the favourite.
func TestAdminMergeRefusesFavoritesOnlySource(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Seed(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	h := &handler{st: st, log: quiet, cache: newInsightCache(), bus: &fakeBus{}, accounts: &accounts{st: st, log: quiet}}
	from, to := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{from, to} {
		if _, err := st.EnsureUser(ctx, store.Identity{ID: id, Provider: "apple"}); err != nil {
			t.Fatal(err)
		}
	}
	food := addCatalogFood(t, func(ctx context.Context, sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}, "Merge fixture food")
	if err := st.PutFoodFavorite(ctx, from, food); err != nil {
		t.Fatal(err)
	}
	// Neither flag is set for from: only the favourite can cause the refusal.
	flagList, err := st.Flags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range flagList {
		if (f.Key == model.FlagNutrition || f.Key == model.FlagNutritionDiary) && f.Enabled {
			t.Fatalf("test needs %s off", f.Key)
		}
	}

	key := "test-admin-key-0123456789abcdef0123456789"
	srv := httpx.Instrument(quiet, nil, adminRoutes(h, adminOptions{Key: key, Timeout: 30 * time.Second}))
	post := func(query string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "/v1/admin/users/"+from.String()[:8]+"/merge-into/"+to.String()[:8]+query, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	favorites := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.food_favorites WHERE user_id=$1`, from).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code, body := post(""); code != http.StatusConflict || !strings.Contains(body, "Nutrition") {
		t.Fatalf("merge of a favourites-only source: %d %s", code, body)
	}
	if code, body := post("?force=1"); code != http.StatusConflict {
		t.Fatalf("force must not override the guard: %d %s", code, body)
	}
	if favorites() != 1 {
		t.Fatal("the refused merge deleted the favourite")
	}
	if _, _, err := st.GetUser(ctx, from); err != nil {
		t.Fatalf("the refused merge deleted the source account: %v", err)
	}
	// Control: without the favourite the same request passes the guard.
	if err := st.DeleteFoodFavorite(ctx, from, food); err != nil {
		t.Fatal(err)
	}
	if code, body := post("?dry_run=1"); code != http.StatusOK {
		t.Fatalf("dry run without the favourite: %d %s", code, body)
	}
}

func TestOwnerExportListsFavoritesOnFirstPageOnly(t *testing.T) {
	rg := newNutritionRig(t)
	ctx := context.Background()
	a, b := rg.account(), rg.account()
	foodA := addCatalogFood(t, func(ctx context.Context, sql string, args ...any) error {
		_, err := rg.pool.Exec(ctx, sql, args...)
		return err
	}, "Export fixture apple")
	foodB := addCatalogFood(t, func(ctx context.Context, sql string, args ...any) error {
		_, err := rg.pool.Exec(ctx, sql, args...)
		return err
	}, "Export fixture pear")
	for user, food := range map[uuid.UUID]uuid.UUID{a: foodA, b: foodB} {
		if err := rg.st.PutFoodFavorite(ctx, user, food); err != nil {
			t.Fatal(err)
		}
	}
	energy := 100.
	for _, id := range []string{"00000000-0000-4000-9000-000000000001", "20000000-0000-4000-9000-000000000001"} {
		custom, err := rg.st.CreateCustomFood(ctx, a, model.FoodCustomInput{ClientRequestID: uuid.New(), Name: "Meal food " + id,
			Nutrients: []model.FoodNutrientWrite{{Code: "energy_kcal", Amount: &energy, Unit: "kcal", ReferenceAmount: 100, ReferenceUnit: "g"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := rg.st.CreateNutritionMeal(ctx, a, model.NutritionMealInput{
			ClientRequestID: uuid.MustParse(id), ConsumedAt: time.Date(2021, 1, 1, 12, 0, 0, 0, time.UTC), Timezone: "UTC",
			MealType: "snack", InputMethod: "manual_entry",
			Items: []model.NutritionMealItemInput{{FoodID: custom.ID, Quantity: 100, Unit: "g"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(user uuid.UUID, query string) (model.NutritionMealExportPage, map[string]json.RawMessage) {
		t.Helper()
		r := rg.do(user, http.MethodGet, nutritionDiaryExportPath+query, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("status=%d %s", r.Code, r.Body.String())
		}
		var page model.NutritionMealExportPage
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(r.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		return page, raw
	}
	first, raw := get(a, "?limit=1")
	if _, ok := raw["favorites"]; !ok || len(first.Favorites) != 1 || first.Favorites[0].FoodID != foodA ||
		first.Favorites[0].FoodName != "Export fixture apple" || first.Favorites[0].FavoritedAt.IsZero() || first.NextAfter == nil {
		t.Fatalf("first page: %+v next=%v", first.Favorites, first.NextAfter)
	}
	later, raw := get(a, "?limit=1&after="+first.NextAfter.String())
	if _, ok := raw["favorites"]; ok || later.Favorites != nil {
		t.Fatal("a page after the cursor must omit favorites")
	}
	// Another account never sees a's favourites; with no meals its list is present and has only its own.
	other, _ := get(b, "")
	if len(other.Favorites) != 1 || other.Favorites[0].FoodID != foodB {
		t.Fatalf("another account's favourites: %+v", other.Favorites)
	}
}
