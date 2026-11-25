package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

// ExportNutritionMeals returns owned diary records across all dates without
// recalculating scores or reading biometric inputs. The cursor uses the unique
// (user_id, client_request_id) index and remains usable if its meal is erased.
func (s *Store) ExportNutritionMeals(ctx context.Context, user, after uuid.UUID, limit int) (model.NutritionMealExportPage, error) {
	page := model.NutritionMealExportPage{Meals: []model.NutritionMeal{}}
	if limit < 1 || limit > 25 {
		return page, nutrition.ErrInvalid
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+mealFields+`
			FROM nutrition.meals WHERE user_id=$1 AND client_request_id>$2
			ORDER BY client_request_id LIMIT $3`, user, after, limit+1)
		if err != nil {
			return err
		}
		page.Meals, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.NutritionMeal, error) {
			return scanNutritionMeal(r)
		})
		if err != nil {
			return err
		}
		if len(page.Meals) > limit {
			after := page.Meals[limit-1].ClientRequestID
			page.NextAfter = &after
			page.Meals = page.Meals[:limit]
		}
		return nutritionMealItems(ctx, tx, user, page.Meals)
	})
	return page, err
}
