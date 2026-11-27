package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/nutrition/score"
)

const maxGlucoseBatch = 500

// PutNutritionGlucose stores a batch atomically, keyed by account and normalized timestamp.
func (s *Store) PutNutritionGlucose(ctx context.Context, user uuid.UUID, samples []model.NutritionGlucoseSample) error {
	if len(samples) == 0 || len(samples) > maxGlucoseBatch {
		return nutrition.ErrInvalid
	}
	times := make([]string, 0, len(samples))
	values := make([]float64, 0, len(samples))
	seen := make(map[time.Time]bool, len(samples))
	for _, p := range samples {
		at := p.Timestamp.UTC().Truncate(time.Microsecond)
		if at.Year() < 1900 || at.Year() > 2200 || seen[at] || !score.ValidGlucose(p.GlucoseMgDL) {
			return nutrition.ErrInvalid
		}
		seen[at] = true
		times = append(times, at.Format(time.RFC3339Nano))
		values = append(values, p.GlucoseMgDL)
	}
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nutrition.glucose_samples(user_id,measured_at,glucose_mg_dl)
			SELECT $1, t.at::timestamptz, t.value FROM unnest($2::text[],$3::float8[]) t(at,value) ORDER BY t.at::timestamptz
			ON CONFLICT(user_id,measured_at) DO UPDATE SET glucose_mg_dl=excluded.glucose_mg_dl`, user, times, values)
		return err
	})
}

// DeleteNutritionGlucose removes the caller's CGM history without changing meals.
func (s *Store) DeleteNutritionGlucose(ctx context.Context, user uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM nutrition.glucose_samples WHERE user_id=$1`, user)
		return err
	})
}

func scoreNutritionDays(ctx context.Context, tx pgx.Tx, user uuid.UUID, days []model.NutritionDaily,
	index map[string]int, chrononutrition bool) error {
	from, to := days[0].LocalDate, days[len(days)-1].LocalDate
	rows, err := tx.Query(ctx, `SELECT `+mealFields+` FROM nutrition.meals
		WHERE user_id=$1 AND local_date BETWEEN $2::date AND $3::date ORDER BY consumed_at,id`, user, from, to)
	if err != nil {
		return err
	}
	meals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.NutritionMeal, error) {
		return scanNutritionMeal(row)
	})
	if err != nil {
		return err
	}
	if len(meals) == 0 {
		for i := range days {
			days[i].NutritionScores = score.UnscoredDaily()
		}
		return nil
	}
	if err := nutritionMealItems(ctx, tx, user, meals); err != nil {
		return err
	}
	neighbors, samples, err := nutritionGlucoseInputs(ctx, tx, user, meals[0].ConsumedAt, meals[len(meals)-1].ConsumedAt)
	if err != nil {
		return err
	}
	glucose := score.GlucoseByMeal(meals, neighbors, samples)
	var bedtimes map[string]*time.Time
	var localTimes map[uuid.UUID]time.Time
	if chrononutrition {
		// The meal-timing score reads measured sleep only: the contract puts a check-in's bedtime into N2R (D293).
		bedtimes, _, err = nutritionBedtimesWith(ctx, tx, user, from, to, false)
		if err != nil {
			return err
		}
		localTimes = nutritionMealLocalTimes(meals)
	}
	goals, err := readNutritionGoals(ctx, tx, user)
	if err != nil {
		return err
	}
	perDay := make([][]model.NutritionMeal, len(days))
	for _, m := range meals {
		i := index[m.LocalDate]
		perDay[i] = append(perDay[i], m)
	}
	for i := range days {
		var timing *score.Timing
		if chrononutrition {
			timing = &score.Timing{Bedtime: bedtimes[days[i].LocalDate], LocalTimes: localTimes}
		}
		days[i].NutritionScores = score.Daily(perDay[i], goals.Targets["energy_kcal"], glucose, timing)
	}
	return nil
}

func nutritionGlucoseInputs(ctx context.Context, tx pgx.Tx, user uuid.UUID, first, last time.Time) ([]time.Time, []model.NutritionGlucoseSample, error) {
	end := last.Add(score.GlucoseWindow)
	rows, err := tx.Query(ctx, `SELECT measured_at,glucose_mg_dl FROM nutrition.glucose_samples
		WHERE user_id=$1 AND measured_at BETWEEN $2 AND $3 ORDER BY measured_at`, user, first.Add(-score.BaselineWindow), end)
	if err != nil {
		return nil, nil, err
	}
	samples, err := pgx.CollectRows(rows, pgx.RowToStructByPos[model.NutritionGlucoseSample])
	if err != nil || len(samples) == 0 {
		return nil, samples, err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT consumed_at FROM nutrition.meals
		WHERE user_id=$1 AND consumed_at > $2 AND consumed_at < $3 ORDER BY consumed_at`, user, first, end)
	if err != nil {
		return nil, nil, err
	}
	neighbors, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	return neighbors, samples, err
}

func nutritionMealLocalTimes(meals []model.NutritionMeal) map[uuid.UUID]time.Time {
	times := make(map[uuid.UUID]time.Time, len(meals))
	locations := make(map[string]*time.Location)
	for _, m := range meals {
		location, seen := locations[m.Timezone]
		if !seen {
			location, _ = time.LoadLocation(m.Timezone)
			locations[m.Timezone] = location
		}
		if location != nil {
			times[m.ID] = m.ConsumedAt.In(location)
		}
	}
	return times
}

// nutritionBedtimesWith are the bedtimes of the nights that follow the days in [from, to], by day, with whose each is
// (model.N2RBedtimeWearable or N2RBedtimeCheckin). The precedence is the night's measured sleep start
// (daily_scores.sleep_start of the next day), then, with checkins (the caller says: the flag recovery.checkin is on
// and the reader is N2R), the next morning's check-in bedtime (D293); the expected bedtime is the profile's and is
// added by the caller.
func nutritionBedtimesWith(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, to string, checkins bool) (map[string]*time.Time, map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT to_char(day-1,'YYYY-MM-DD'), sleep_start FROM daily_scores
		WHERE user_id=$1 AND day BETWEEN $2::date+1 AND $3::date+1 AND sleep_start IS NOT NULL AND sleep_actual_min > 0`, user, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make(map[string]*time.Time)
	source := make(map[string]string)
	for rows.Next() {
		var date string
		var start time.Time
		if err := rows.Scan(&date, &start); err != nil {
			return nil, nil, err
		}
		out[date], source[date] = &start, model.N2RBedtimeWearable
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	if !checkins {
		return out, source, nil
	}
	reported, err := checkinBedtimes(ctx, tx, user, from, to)
	if err != nil {
		return nil, nil, err
	}
	for date, at := range reported {
		if _, measured := out[date]; !measured {
			bed := at
			out[date], source[date] = &bed, model.N2RBedtimeCheckin
		}
	}
	return out, source, nil
}
