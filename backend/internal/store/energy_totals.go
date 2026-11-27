package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// NutritionEnergyTotals reads the owner's diary kcal per local date for a score
// range of any length (DailyScore.energy, D310): only days with a logged meal,
// each with its meal and item counts and the energy_kcal subtotal as
// NutritionDaily reports it (KnownSubtotal over the items that carry kcal,
// Complete when every item does). One pass grouped by date, only the energy key,
// so a long /v1/days range stays cheap; no score inputs are read.
func (s *Store) NutritionEnergyTotals(ctx context.Context, user uuid.UUID, from, to string) ([]model.NutritionDaily, error) {
	defer observeNutritionDB("energy_totals", time.Now())
	days := []model.NutritionDaily{}
	err := s.nutritionSnapshot(ctx, user, pgx.ReadOnly, func(tx pgx.Tx) error {
		days = days[:0]
		rows, err := tx.Query(ctx, `SELECT m.local_date::text, count(DISTINCT m.id), count(i.id),
				count(i.id) FILTER (WHERE i.nutrients->'energy_kcal' IS NOT NULL),
				coalesce(sum((i.nutrients->'energy_kcal'->>'amount')::numeric), 0)::float8
			FROM nutrition.meals m
			LEFT JOIN nutrition.meal_items i ON i.meal_id = m.id AND i.user_id = $1
			WHERE m.user_id = $1 AND m.local_date BETWEEN $2::date AND $3::date
			GROUP BY m.local_date ORDER BY m.local_date`, user, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d model.NutritionDaily
			var known int
			var subtotal float64
			if err := rows.Scan(&d.LocalDate, &d.Meals, &d.Items, &known, &subtotal); err != nil {
				return err
			}
			d.Nutrients = map[string]model.NutritionTotal{}
			if known > 0 {
				d.Nutrients["energy_kcal"] = model.NutritionTotal{KnownSubtotal: subtotal, Unit: "kcal",
					KnownItems: known, Complete: known == d.Items}
			}
			days = append(days, d)
		}
		return rows.Err()
	})
	return days, err
}
