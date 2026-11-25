package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

type foodPreference struct {
	subtitles map[string]string
	density   *float64
	favorite  bool
	last      model.NutritionMealItem
}

func loadFoodPreferences(ctx context.Context, tx pgx.Tx, user uuid.UUID, ids []string) (map[uuid.UUID]foodPreference, error) {
	rows, err := tx.Query(ctx, `SELECT f.id,f.subtitles,f.density_g_ml,fav.food_id IS NOT NULL,
		coalesce(last.quantity,0),coalesce(last.unit,''),last.portion_id,last.weight_g,last.volume_ml
		FROM nutrition.foods f
		LEFT JOIN nutrition.food_favorites fav ON fav.food_id=f.id AND fav.user_id=$2
		LEFT JOIN LATERAL (
			SELECT i.quantity,i.unit,i.portion_id,i.weight_g,i.volume_ml
			FROM nutrition.meal_items i WHERE i.user_id=$2 AND i.food_id=f.id
				AND i.created_at>=statement_timestamp()-interval '90 days'
			ORDER BY i.created_at DESC,i.id DESC LIMIT 1
		) last ON true
		WHERE f.id=ANY($1::text[]::uuid[]) AND f.status='active' AND (f.owner_user_id IS NULL OR f.owner_user_id=$2)`, ids, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	preferences := make(map[uuid.UUID]foodPreference, len(ids))
	for rows.Next() {
		var id uuid.UUID
		var p foodPreference
		if err := rows.Scan(&id, &p.subtitles, &p.density, &p.favorite,
			&p.last.Quantity, &p.last.Unit, &p.last.Portion, &p.last.WeightG, &p.last.VolumeML); err != nil {
			return nil, err
		}
		preferences[id] = p
	}
	return preferences, rows.Err()
}

func addFoodSelections(food *model.FoodDetail, p foodPreference, language string) {
	food.Subtitle = nutrition.CatalogLabel(p.subtitles, language)
	food.Favorited = &p.favorite
	food.DefaultPortion, food.LastUsedPortion, food.ReferencePortion = nutrition.CatalogSelections(*food, p.last, language)
}

// Search previews read only the returned page and load portion and nutrient
// rows in batches. The full detail query can reuse the same calculator.
func enrichFoodSearch(ctx context.Context, tx pgx.Tx, user uuid.UUID, page []model.FoodSummary, language string) error {
	if len(page) == 0 {
		return nil
	}
	ids := make([]string, len(page))
	food := make(map[uuid.UUID]*model.FoodDetail, len(page))
	for i, summary := range page {
		ids[i] = summary.ID.String()
		food[summary.ID] = &model.FoodDetail{FoodSummary: summary}
	}
	preferences, err := loadFoodPreferences(ctx, tx, user, ids)
	if err != nil {
		return err
	}
	lastPortions := make([]string, 0, len(preferences))
	for _, p := range preferences {
		if p.last.Portion != nil {
			lastPortions = append(lastPortions, p.last.Portion.String())
		}
	}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT p.food_id,`+foodPortionFields+`
		FROM nutrition.portions p WHERE p.food_id=ANY($1::text[]::uuid[])
			AND (p.is_default OR p.id=ANY($2::text[]::uuid[]))
		ORDER BY p.food_id,p.sort_order,p.label,p.id`, ids, lastPortions).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var id uuid.UUID
			var portion model.FoodPortion
			if err := scanFoodPortionWith(rows, &portion, &id); err != nil {
				return err
			}
			food[id].Portions = append(food[id].Portions, portion)
		}
		return rows.Err()
	})
	batch.Queue(`SELECT v.food_id,v.code,v.amount,n.unit,v.reference_amount,v.reference_unit,v.source_id,v.method,v.quality
		FROM nutrition.nutrient_values v JOIN nutrition.nutrients n USING(code)
		WHERE v.food_id=ANY($1::text[]::uuid[])`, ids).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var id uuid.UUID
			var n model.FoodNutrient
			if err := rows.Scan(&id, &n.Code, &n.Amount, &n.Unit, &n.ReferenceAmount, &n.ReferenceUnit, &n.SourceID, &n.Method, &n.Quality); err != nil {
				return err
			}
			food[id].Nutrients = append(food[id].Nutrients, n)
		}
		return rows.Err()
	})
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	for i, row := range page {
		f := food[row.ID]
		f.Density = preferences[row.ID].density
		addFoodSelections(f, preferences[row.ID], language)
		page[i] = f.FoodSummary
	}
	return nil
}

// PutFoodFavorite is idempotent and only accepts an active food visible to the owner.
func (s *Store) PutFoodFavorite(ctx context.Context, user, id uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `WITH eligible AS (
			SELECT id FROM nutrition.foods WHERE id=$2 AND status='active'
				AND (owner_user_id IS NULL OR owner_user_id=$1)
		), added AS (
			INSERT INTO nutrition.food_favorites(user_id,food_id)
			SELECT $1,id FROM eligible ON CONFLICT DO NOTHING
		)
		SELECT EXISTS (SELECT 1 FROM eligible)`, user, id).Scan(&exists)
		if err == nil && !exists {
			return ErrNotFound
		}
		return err
	})
}

// DeleteFoodFavorite removes only the authenticated owner's relationship.
func (s *Store) DeleteFoodFavorite(ctx context.Context, user, id uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM nutrition.food_favorites WHERE user_id=$1 AND food_id=$2`, user, id)
		return err
	})
}
