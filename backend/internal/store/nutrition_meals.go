package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/nutrition/score"
)

const mealFields = `id, client_request_id, consumed_at, timezone, local_date::text, meal_type, input_method,
	name, notes, revision, created_at, updated_at`

func scanNutritionMeal(row pgx.Row) (model.NutritionMeal, error) {
	m := model.NutritionMeal{Items: []model.NutritionMealItem{}}
	err := row.Scan(&m.ID, &m.ClientRequestID, &m.ConsumedAt, &m.Timezone, &m.LocalDate, &m.MealType, &m.InputMethod,
		&m.Name, &m.Notes, &m.Revision, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	m.ConsumedAt, m.CreatedAt, m.UpdatedAt = m.ConsumedAt.UTC(), m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return m, err
}

func nutritionMealItems(ctx context.Context, tx pgx.Tx, user uuid.UUID, meals []model.NutritionMeal) error {
	if len(meals) == 0 {
		return nil
	}
	ids := make([]string, len(meals))
	byID := make(map[uuid.UUID]*model.NutritionMeal, len(meals))
	for i := range meals {
		ids[i], byID[meals[i].ID] = meals[i].ID.String(), &meals[i]
	}
	rows, err := tx.Query(ctx, `SELECT meal_id, id, food_id, food_name, brand, quantity, unit, portion_id,
		weight_g, volume_ml, nutrients, sources, calculation_version, created_at, quality_profile FROM nutrition.meal_items
		WHERE user_id=$1 AND meal_id=ANY($2::text[]::uuid[]) ORDER BY meal_id,position`, user, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var i model.NutritionMealItem
		if err := rows.Scan(&id, &i.ID, &i.FoodID, &i.FoodName, &i.Brand, &i.Quantity, &i.Unit, &i.Portion,
			&i.WeightG, &i.VolumeML, &i.Nutrients, &i.Sources, &i.CalculationVersion, &i.CreatedAt, &i.QualityProfile); err != nil {
			return err
		}
		i.CreatedAt = i.CreatedAt.UTC()
		m := byID[id]
		m.Items = append(m.Items, i)
	}
	return rows.Err()
}

func readNutritionMeal(ctx context.Context, tx pgx.Tx, user, id uuid.UUID, lock bool) (model.NutritionMeal, error) {
	q := `SELECT ` + mealFields + ` FROM nutrition.meals WHERE user_id=$1 AND id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	m, err := scanNutritionMeal(tx.QueryRow(ctx, q, user, id))
	if err != nil {
		return m, err
	}
	meals := []model.NutritionMeal{m}
	err = nutritionMealItems(ctx, tx, user, meals)
	return meals[0], err
}

// A concurrent change to a row locked inside a repeatable read can require a fresh snapshot.
func (s *Store) nutritionSnapshot(ctx context.Context, user uuid.UUID, mode pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: mode}, fn)
		var pgErr *pgconn.PgError
		if attempt >= 2 || !errors.As(err, &pgErr) || pgErr.Code != "40001" {
			return err
		}
	}
}

// CreateNutritionMeal commits a meal once per account and client request key.
func (s *Store) CreateNutritionMeal(ctx context.Context, user uuid.UUID, in model.NutritionMealInput) (model.NutritionMeal, bool, error) {
	var out model.NutritionMeal
	if in.ClientRequestID == uuid.Nil || !validItemCount(in.Items) {
		return out, false, nutrition.ErrInvalid
	}
	in.ConsumedAt = in.ConsumedAt.UTC().Truncate(time.Microsecond)
	day, err := nutrition.ValidateMeal(in)
	if err != nil {
		return out, false, err
	}
	hash, err := nutritionHash(in)
	if err != nil {
		return out, false, nutrition.ErrInvalid
	}
	created := false
	err = s.nutritionSnapshot(ctx, user, pgx.ReadWrite, func(tx pgx.Tx) error {
		created = false
		tag, err := tx.Exec(ctx, `INSERT INTO nutrition.meal_requests(user_id,request_id) VALUES ($1,$2)
			ON CONFLICT DO NOTHING`, user, in.ClientRequestID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var old []byte
			var id uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id,request_hash FROM nutrition.meals WHERE user_id=$1 AND client_request_id=$2`,
				user, in.ClientRequestID).Scan(&id, &old)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !bytes.Equal(old, hash[:])) {
				return ErrNutritionConflict
			}
			if err != nil {
				return err
			}
			out, err = readNutritionMeal(ctx, tx, user, id, false)
			return err
		}
		out, err = scanNutritionMeal(tx.QueryRow(ctx, `INSERT INTO nutrition.meals
			(id,user_id,client_request_id,request_hash,consumed_at,timezone,local_date,meal_type,input_method,name,notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+mealFields,
			uuid.New(), user, in.ClientRequestID, hash[:], in.ConsumedAt, in.Timezone, day, in.MealType, in.InputMethod, in.Name, in.Notes))
		if err != nil {
			return err
		}
		out.Items, err = makeNutritionItems(ctx, tx, user, in.Items, nil, out.CreatedAt)
		if err != nil {
			return err
		}
		created = true
		return insertNutritionItems(ctx, tx, user, out.ID, out.Items)
	})
	return out, created && err == nil, err
}

func validItemCount(items []model.NutritionMealItemInput) bool {
	return len(items) >= 1 && len(items) <= 50
}

func makeNutritionItems(ctx context.Context, tx pgx.Tx, user uuid.UUID, inputs []model.NutritionMealItemInput,
	previous []model.NutritionMealItem, at time.Time) ([]model.NutritionMealItem, error) {
	old := make(map[uuid.UUID]model.NutritionMealItem, len(previous))
	for _, item := range previous {
		old[item.ID] = item
	}
	foods := make(map[uuid.UUID]model.FoodDetail)
	newItems := make([]model.NutritionMealItem, 0, len(inputs))
	for _, in := range inputs {
		if in.ItemID != nil {
			item, ok := old[*in.ItemID]
			if !ok || in.FoodID != uuid.Nil || in.Quantity != 0 || in.Unit != "" || in.Portion != nil {
				return nil, nutrition.ErrInvalid
			}
			newItems = append(newItems, item)
			delete(old, *in.ItemID)
			continue
		}
		if in.FoodID == uuid.Nil || !nutrition.Quantity(in.Quantity) {
			return nil, nutrition.ErrInvalid
		}
		f, ok := foods[in.FoodID]
		if !ok {
			var err error
			f, err = nutritionFood(ctx, tx, user, in.FoodID)
			if err != nil {
				return nil, err
			}
			foods[f.ID] = f
		}
		item, err := nutrition.Calculate(f, in)
		if err != nil {
			return nil, err
		}
		item.ID, item.CreatedAt = uuid.New(), at
		item.QualityProfile = f.QualityProfile
		newItems = append(newItems, item)
	}
	return newItems, nil
}

func insertNutritionItems(ctx context.Context, tx pgx.Tx, user, meal uuid.UUID, items []model.NutritionMealItem) error {
	batch := &pgx.Batch{}
	for pos, item := range items {
		nutrients, err := json.Marshal(item.Nutrients)
		if err != nil {
			return err
		}
		sources, err := json.Marshal(item.Sources)
		if err != nil {
			return err
		}
		var qualityProfile *string
		if item.QualityProfile != nil {
			data, err := json.Marshal(item.QualityProfile)
			if err != nil {
				return err
			}
			value := string(data)
			qualityProfile = &value
		}
		batch.Queue(`INSERT INTO nutrition.meal_items (id,meal_id,user_id,position,food_id,food_name,brand,quantity,unit,
			portion_id,weight_g,volume_ml,nutrients,sources,calculation_version,created_at,quality_profile)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14::jsonb,$15,$16,$17::jsonb)`,
			item.ID, meal, user, pos, item.FoodID, item.FoodName, item.Brand, item.Quantity, item.Unit, item.Portion,
			item.WeightG, item.VolumeML, string(nutrients), string(sources), item.CalculationVersion, item.CreatedAt, qualityProfile)
	}
	return tx.SendBatch(ctx, batch).Close()
}

// NutritionMeal reads the account’s saved meal and item snapshots.
func (s *Store) NutritionMeal(ctx context.Context, user, id uuid.UUID) (model.NutritionMeal, error) {
	var out model.NutritionMeal
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = readNutritionMeal(ctx, tx, user, id, false)
		return err
	})
	return out, err
}

// PatchNutritionMeal updates an expected revision and preserves retained snapshots.
func (s *Store) PatchNutritionMeal(ctx context.Context, user, id uuid.UUID, patch model.NutritionMealPatch) (model.NutritionMeal, error) {
	var out model.NutritionMeal
	if patch.Revision < 1 || (patch.Items != nil && !validItemCount(*patch.Items)) {
		return out, nutrition.ErrInvalid
	}
	err := s.nutritionSnapshot(ctx, user, pgx.ReadWrite, func(tx pgx.Tx) error {
		old, err := readNutritionMeal(ctx, tx, user, id, true)
		if err != nil {
			return err
		}
		if old.Revision != patch.Revision {
			return ErrNutritionConflict
		}
		in := model.NutritionMealInput{
			ConsumedAt: orValue(patch.ConsumedAt, old.ConsumedAt).UTC().Truncate(time.Microsecond),
			Timezone:   orValue(patch.Timezone, old.Timezone), MealType: orValue(patch.MealType, old.MealType),
			InputMethod: orValue(patch.InputMethod, old.InputMethod), Name: orValue(patch.Name, old.Name),
			Notes: orValue(patch.Notes, old.Notes),
		}
		day, err := nutrition.ValidateMeal(in)
		if err != nil {
			return err
		}
		out, err = scanNutritionMeal(tx.QueryRow(ctx, `UPDATE nutrition.meals SET
			consumed_at=$3,timezone=$4,local_date=$5,meal_type=$6,input_method=$7,name=$8,notes=$9,
			revision=revision+1,updated_at=clock_timestamp() WHERE user_id=$1 AND id=$2 RETURNING `+mealFields,
			user, id, in.ConsumedAt, in.Timezone, day, in.MealType, in.InputMethod, in.Name, in.Notes))
		if err != nil {
			return err
		}
		out.Items = old.Items
		if patch.Items == nil {
			return nil
		}
		items, err := makeNutritionItems(ctx, tx, user, *patch.Items, old.Items, out.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM nutrition.meal_items WHERE user_id=$1 AND meal_id=$2`, user, id); err != nil {
			return err
		}
		out.Items = items
		return insertNutritionItems(ctx, tx, user, id, items)
	})
	return out, err
}

func orValue[T any](v *T, fallback T) T {
	if v != nil {
		return *v
	}
	return fallback
}

// DeleteNutritionMeal erases an account’s meal and preserves its opaque retry key.
func (s *Store) DeleteNutritionMeal(ctx context.Context, user, id uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM nutrition.meals WHERE user_id=$1 AND id=$2`, user, id)
		return err
	})
}

// nutritionDateRange bounds a query over captured local dates.
func nutritionDateRange(from, to string) (time.Time, time.Time, error) {
	first, e1 := time.Parse(time.DateOnly, from)
	last, e2 := time.Parse(time.DateOnly, to)
	if e1 != nil || e2 != nil || first.Year() < 1899 || last.Year() > 2201 || last.Before(first) ||
		last.After(first.AddDate(0, 0, 30)) {
		return first, last, nutrition.ErrInvalid
	}
	return first, last, nil
}

// NutritionMeals lists saved meals over a bounded set of local dates.
func (s *Store) NutritionMeals(ctx context.Context, user uuid.UUID, from, to string, limit, offset int) (model.NutritionMealPage, error) {
	page := model.NutritionMealPage{Meals: []model.NutritionMeal{}}
	if _, _, err := nutritionDateRange(from, to); err != nil || limit < 1 || limit > 30 || offset < 0 || offset > 1000 {
		return page, nutrition.ErrInvalid
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+mealFields+` FROM nutrition.meals WHERE user_id=$1 AND local_date BETWEEN $2::date AND $3::date
			ORDER BY local_date DESC, consumed_at DESC, id DESC LIMIT $4 OFFSET $5`, user, from, to, limit+1, offset)
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
			if next := offset + limit; next <= 1000 {
				page.NextOffset = &next
			}
			page.Meals = page.Meals[:limit]
		}
		return nutritionMealItems(ctx, tx, user, page.Meals)
	})
	return page, err
}

// NutritionDaily sums known values and measures coverage among logged items.
func (s *Store) NutritionDaily(ctx context.Context, user uuid.UUID, from, to string, chrononutrition bool) ([]model.NutritionDaily, error) {
	return s.nutritionDaily(ctx, user, from, to, chrononutrition, nil, "", true)
}

// NutritionDailyWithN2R adds freshly computed scores only with consent to policy.
func (s *Store) NutritionDailyWithN2R(ctx context.Context, user uuid.UUID, from, to string, chrononutrition bool, now time.Time, policy string) ([]model.NutritionDaily, error) {
	return s.nutritionDaily(ctx, user, from, to, chrononutrition, &now, policy, true)
}

// NutritionDiaryTotals reads owner diary entries and skips all score inputs.
func (s *Store) NutritionDiaryTotals(ctx context.Context, user uuid.UUID, from, to string) ([]model.NutritionDaily, error) {
	return s.nutritionDaily(ctx, user, from, to, false, nil, "", false)
}

func (s *Store) nutritionDaily(ctx context.Context, user uuid.UUID, from, to string, chrononutrition bool, n2rNow *time.Time, policy string, scoreAllowed bool) ([]model.NutritionDaily, error) {
	first, last, err := nutritionDateRange(from, to)
	if err != nil {
		return nil, err
	}
	defer observeNutritionDB("daily", time.Now())
	index := map[string]int{}
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		index[day.Format(time.DateOnly)] = len(index)
	}
	var days []model.NutritionDaily
	mode := pgx.ReadOnly
	if policy != "" {
		mode = pgx.ReadWrite // FOR SHARE keeps the grant current until scoring finishes.
	}
	err = s.nutritionSnapshot(ctx, user, mode, func(tx pgx.Tx) error {
		consented := false
		if scoreAllowed {
			var err error
			if consented, err = coachingConsent(ctx, tx, user, policy); err != nil {
				return err
			}
		}
		days = make([]model.NutritionDaily, len(index))
		for key, i := range index {
			days[i] = model.NutritionDaily{LocalDate: key, Nutrients: map[string]model.NutritionTotal{},
				NutritionScores: score.UnscoredDaily()}
		}
		rows, err := tx.Query(ctx, `WITH meal AS (
			SELECT id, local_date FROM nutrition.meals WHERE user_id=$1 AND local_date BETWEEN $2::date AND $3::date
		), items AS (
			SELECT meal.local_date, i.nutrients FROM meal JOIN nutrition.meal_items i ON i.meal_id=meal.id WHERE i.user_id=$1
		), counts AS (
			SELECT meal.local_date, count(*) AS meals,
				(SELECT count(*) FROM items WHERE items.local_date=meal.local_date) AS items
			FROM meal GROUP BY meal.local_date
		), totals AS (
			SELECT local_date, n.key AS code, sum((n.value->>'amount')::numeric) AS subtotal, count(*) AS known
			FROM items CROSS JOIN LATERAL jsonb_each(items.nutrients) n WHERE n.key <> 'glycemic_index' GROUP BY local_date, n.key
		)
		SELECT c.local_date::text, c.meals, c.items, t.code, t.subtotal, t.known, n.unit
		FROM counts c LEFT JOIN totals t USING(local_date) LEFT JOIN nutrition.nutrients n ON n.code=t.code`, user, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day string
			var meals, items int
			var code, unit *string
			var subtotal *float64
			var known *int
			if err := rows.Scan(&day, &meals, &items, &code, &subtotal, &known, &unit); err != nil {
				return err
			}
			d := &days[index[day]]
			d.Meals, d.Items = meals, items
			if code != nil && unit != nil {
				d.Nutrients[*code] = model.NutritionTotal{
					KnownSubtotal: *subtotal, Unit: *unit, KnownItems: *known, Complete: *known == items,
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if !consented {
			return nil
		}
		if err := scoreNutritionDays(ctx, tx, user, days, index, chrononutrition); err != nil {
			return err
		}
		if n2rNow != nil {
			return addN2RDays(ctx, tx, user, days, *n2rNow)
		}
		return nil
	})
	return days, err
}

func readNutritionGoals(ctx context.Context, tx pgx.Tx, user uuid.UUID) (model.NutritionGoals, error) {
	out := model.NutritionGoals{NutritionGoalInput: model.NutritionGoalInput{Targets: map[string]float64{}}}
	err := tx.QueryRow(ctx, `SELECT revision,targets,created_at FROM nutrition.goal_revisions WHERE user_id=$1
		ORDER BY revision DESC LIMIT 1`, user).Scan(&out.Revision, &out.Targets, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

// NutritionGoals returns the latest target revision or an empty revision zero.
func (s *Store) NutritionGoals(ctx context.Context, user uuid.UUID) (model.NutritionGoals, error) {
	var out model.NutritionGoals
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = readNutritionGoals(ctx, tx, user)
		return err
	})
	return out, err
}

// PutNutritionGoals commits a new target revision or returns a matching retry.
func (s *Store) PutNutritionGoals(ctx context.Context, user uuid.UUID, input model.NutritionGoalInput) (model.NutritionGoals, error) {
	var out model.NutritionGoals
	if input.Revision < 0 || input.Revision >= 2147483647 || input.Targets == nil || len(input.Targets) > 45 {
		return out, nutrition.ErrInvalid
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Serializes first-time writes as well as revisions.
		if _, err := tx.Exec(ctx, lockUser, user); err != nil {
			return err
		}
		old, err := readNutritionGoals(ctx, tx, user)
		if err != nil {
			return err
		}
		if old.Revision == input.Revision+1 && maps.Equal(old.Targets, input.Targets) {
			out = old
			return nil
		}
		if old.Revision != input.Revision {
			return ErrNutritionConflict
		}
		units, err := registryUnits(ctx, tx)
		if err != nil {
			return err
		}
		for code, target := range input.Targets {
			if units[code] == "" || !nutrition.Quantity(target) {
				return nutrition.ErrInvalid
			}
		}
		targets, err := json.Marshal(input.Targets)
		if err != nil {
			return nutrition.ErrInvalid
		}
		out = model.NutritionGoals{NutritionGoalInput: model.NutritionGoalInput{Revision: old.Revision + 1, Targets: input.Targets}}
		return tx.QueryRow(ctx, `INSERT INTO nutrition.goal_revisions(user_id,revision,targets) VALUES ($1,$2,$3::jsonb)
			RETURNING created_at`, user, out.Revision, string(targets)).Scan(&out.CreatedAt)
	})
	return out, err
}
