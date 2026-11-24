package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/nutrition/score"
)

const n2rLookback = 13

type n2rProfile struct {
	model.N2RProfile
	Location          *time.Location
	Calibrated        float64
	LastCalibratedDay *time.Time
}

func readN2RProfile(ctx context.Context, tx pgx.Tx, user uuid.UUID, lock bool) (*n2rProfile, error) {
	stmt := `SELECT p.body_mass_kg,p.sex,p.sweat_rate_l_per_h,p.sweat_na_mg_per_l,p.caffeine_half_life_h,
		coalesce(to_char(p.expected_bedtime,'HH24:MI'),''),p.next_workout_start_at,u.tz,coalesce(p.calibrated_half_life_h,0),p.last_calibrated_day
		FROM nutrition.n2r_profiles p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1`
	if lock {
		stmt += ` FOR UPDATE OF p`
	}
	var p n2rProfile
	var zone string
	err := tx.QueryRow(ctx, stmt, user).Scan(&p.BodyMassKG, &p.Sex, &p.SweatRateLPH, &p.SweatSodiumMgL, &p.CaffeineHalfH,
		&p.ExpectedBedtime, &p.NextWorkoutAt, &zone, &p.Calibrated, &p.LastCalibratedDay)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Location, err = time.LoadLocation(zone)
	return &p, err
}

// N2RProfile reads the account's settings if they have enrolled.
func (s *Store) N2RProfile(ctx context.Context, user uuid.UUID) (*model.N2RProfile, error) {
	var result *model.N2RProfile
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p, err := readN2RProfile(ctx, tx, user, false)
		if p != nil {
			result = &p.N2RProfile
		}
		return err
	})
	return result, err
}

func validN2RSetting(v *float64, low, high float64) bool { return v == nil || *v >= low && *v <= high }

// PutN2RProfile enrolls the account or replaces its settings.
func (s *Store) PutN2RProfile(ctx context.Context, user uuid.UUID, p model.N2RProfile) (model.N2RProfile, error) {
	if p.Sex == "" {
		p.Sex = "unspecified"
	}
	if !validN2RSetting(&p.BodyMassKG, 25, 350) || !slices.Contains([]string{"female", "male", "unspecified"}, p.Sex) ||
		!validN2RSetting(p.SweatRateLPH, 0, 5) || !validN2RSetting(p.SweatSodiumMgL, 0, 5000) || !validN2RSetting(p.CaffeineHalfH, 1, 12) {
		return model.N2RProfile{}, nutrition.ErrInvalid
	}
	if p.ExpectedBedtime != "" {
		clock, err := time.Parse("15:04", p.ExpectedBedtime)
		if err != nil || clock.Format("15:04") != p.ExpectedBedtime {
			return model.N2RProfile{}, nutrition.ErrInvalid
		}
	}
	if p.NextWorkoutAt != nil && (p.NextWorkoutAt.Year() < 1900 || p.NextWorkoutAt.Year() > 2200) {
		return model.N2RProfile{}, nutrition.ErrInvalid
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nutrition.n2r_profiles AS p
			(user_id,body_mass_kg,sex,sweat_rate_l_per_h,sweat_na_mg_per_l,caffeine_half_life_h,expected_bedtime,next_workout_start_at)
			VALUES ($1,$2,$3,$4,$5,$6,nullif($7,'')::time,$8)
			ON CONFLICT (user_id) DO UPDATE SET body_mass_kg=excluded.body_mass_kg,sex=excluded.sex,
				sweat_rate_l_per_h=excluded.sweat_rate_l_per_h,sweat_na_mg_per_l=excluded.sweat_na_mg_per_l,
				caffeine_half_life_h=excluded.caffeine_half_life_h,expected_bedtime=excluded.expected_bedtime,next_workout_start_at=excluded.next_workout_start_at,
				calibrated_half_life_h=CASE WHEN p.caffeine_half_life_h IS NOT DISTINCT FROM excluded.caffeine_half_life_h
				THEN p.calibrated_half_life_h END,updated_at=now()`, user, p.BodyMassKG, p.Sex, p.SweatRateLPH, p.SweatSodiumMgL, p.CaffeineHalfH, p.ExpectedBedtime, p.NextWorkoutAt)
		return err
	})
	return p, err
}

// DeleteN2RProfile removes settings and their derived night records.
func (s *Store) DeleteN2RProfile(ctx context.Context, user uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM nutrition.n2r_profiles WHERE user_id=$1`, user)
		return err
	})
}

type n2rHistory struct {
	Profile  n2rProfile
	Meals    map[string][]model.NutritionMeal
	Workouts []model.Workout
	Bedtimes map[string]*time.Time
	Omega    map[string]float64
	Glucose  map[uuid.UUID]model.NutritionMealGlucoseScore
	BaseKcal float64
	Coaching coachingData
}

func parseN2RDay(date string, loc *time.Location) time.Time {
	day, _ := time.ParseInLocation(time.DateOnly, date, loc)
	return day
}

func expectedBedtime(day time.Time, p n2rProfile) *time.Time {
	clock, err := time.Parse("15:04", p.ExpectedBedtime)
	if err != nil {
		return nil
	}
	if clock.Hour() < 12 {
		day = day.AddDate(0, 0, 1)
	}
	bed := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, p.Location)
	return &bed
}

func n2rMeals(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, to string, asOf time.Time) ([]model.NutritionMeal, error) {
	rows, err := tx.Query(ctx, `SELECT `+mealFields+` FROM nutrition.meals
		WHERE user_id=$1 AND local_date BETWEEN $2::date AND $3::date AND consumed_at <= $4 ORDER BY consumed_at,id`, user, from, to, asOf)
	if err != nil {
		return nil, err
	}
	meals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.NutritionMeal, error) { return scanNutritionMeal(r) })
	if err != nil || len(meals) == 0 {
		return meals, err
	}
	return meals, nutritionMealItems(ctx, tx, user, meals)
}

func readN2RHistory(ctx context.Context, tx pgx.Tx, user uuid.UUID, p n2rProfile, from, to string, asOf time.Time) (n2rHistory, error) {
	if p.NextWorkoutAt != nil && p.NextWorkoutAt.Before(asOf) {
		p.NextWorkoutAt = nil
	}
	h := n2rHistory{Profile: p, Meals: map[string][]model.NutritionMeal{}, Omega: map[string]float64{}}
	first, last := parseN2RDay(from, p.Location), parseN2RDay(to, p.Location)
	history := first.AddDate(0, 0, -n2rLookback)
	start := history.AddDate(0, 0, -1)
	meals, err := n2rMeals(ctx, tx, user, start.Format(time.DateOnly), to, asOf)
	if err != nil {
		return h, err
	}
	for _, m := range meals {
		h.Meals[m.LocalDate] = append(h.Meals[m.LocalDate], m)
	}
	goals, err := readNutritionGoals(ctx, tx, user)
	if err != nil {
		return h, err
	}
	h.BaseKcal = goals.Targets["energy_kcal"]
	h.Workouts, err = listWorkoutsBetween(ctx, tx, user, start, last.AddDate(0, 0, 2), false)
	if err != nil {
		return h, err
	}
	h.Workouts = slices.DeleteFunc(h.Workouts, func(w model.Workout) bool { return w.End.After(asOf) })
	coaching, err := readCoachingProfile(ctx, tx, user, false)
	if err != nil {
		return h, err
	}
	h.Coaching, err = loadCoaching(ctx, tx, user, p, history, last, asOf, coaching)
	if err != nil {
		return h, err
	}
	if coaching != nil {
		if coaching.SweatRate != nil {
			h.Profile.SweatRateLPH = coaching.SweatRate
		}
		if coaching.Parameters.SweatSodium != nil {
			h.Profile.SweatSodiumMgL = coaching.Parameters.SweatSodium
		} else if coaching.GoalProfile.SweatSodium != nil {
			h.Profile.SweatSodiumMgL = coaching.GoalProfile.SweatSodium
		}
		h.Workouts = nil
		for _, w := range h.Coaching.workouts {
			if w.End.After(start) {
				m := w.Model()
				if state, ok := h.Coaching.plans[w.Start.In(p.Location).Format(time.DateOnly)]; ok {
					net := energy.Workout(w, state.Physiology).NetKcal
					m.EnergyKcal = &net
				}
				h.Workouts = append(h.Workouts, m)
			}
		}
	}
	h.Bedtimes, err = nutritionBedtimes(ctx, tx, user, from, to)
	if err != nil {
		return h, err
	}
	for d := history; !d.After(last); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		target := score.EnergyTarget(h.BaseKcal, h.Workouts, d, d.AddDate(0, 0, 1))
		if state, ok := h.Coaching.plans[key]; ok {
			target = state.Plan.EnergyKcal
		}
		if v := score.EPAIntake(h.Meals[key], target); v != nil {
			h.Omega[key] = *v
		}
	}
	current := slices.DeleteFunc(slices.Clone(meals), func(m model.NutritionMeal) bool { return m.LocalDate < from })
	if len(current) > 0 {
		neighbors, samples, err := nutritionGlucoseInputs(ctx, tx, user, current[0].ConsumedAt, current[len(current)-1].ConsumedAt)
		if err != nil {
			return h, err
		}
		samples = slices.DeleteFunc(samples, func(g model.NutritionGlucoseSample) bool { return g.Timestamp.After(asOf) })
		neighbors = slices.DeleteFunc(neighbors, func(t time.Time) bool { return t.After(asOf) })
		h.Glucose = score.GlucoseByMeal(current, neighbors, samples)
	}
	return h, nil
}

func (h n2rHistory) input(date string, measuredBed bool) score.N2RInput {
	day := parseN2RDay(date, h.Profile.Location)
	bed := expectedBedtime(day, h.Profile)
	if actual := h.Bedtimes[date]; measuredBed && actual != nil {
		bed = actual
	}
	var history []score.OmegaDay
	for age := 1; age <= n2rLookback; age++ {
		if mg, ok := h.Omega[day.AddDate(0, 0, -age).Format(time.DateOnly)]; ok {
			history = append(history, score.OmegaDay{Age: age, Mg: mg})
		}
	}
	input := score.N2RInput{Profile: h.Profile.N2RProfile, BaseKcal: h.BaseKcal, Start: day, End: day.AddDate(0, 0, 1),
		Bedtime: bed, Meals: h.Meals[date], PriorMeals: h.Meals[day.AddDate(0, 0, -1).Format(time.DateOnly)],
		Workouts: h.Workouts, Glucose: h.Glucose, Omega: history, CalibratedHalfH: h.Profile.Calibrated}
	if state, ok := h.Coaching.plans[date]; ok {
		input.Coaching = &state.Plan
		input.AlcoholSensitivity = h.Coaching.profile.Alcohol
		input.CoachingHalfH = &h.Coaching.profile.Caffeine
		input.Profile.BodyMassKG = state.Physiology.MassKG
	}
	return input
}

func addN2RDays(ctx context.Context, tx pgx.Tx, user uuid.UUID, days []model.NutritionDaily, now time.Time) error {
	p, err := readN2RProfile(ctx, tx, user, false)
	if err != nil || p == nil {
		return err
	}
	h, err := readN2RHistory(ctx, tx, user, *p, days[0].LocalDate, days[len(days)-1].LocalDate, now)
	if err != nil {
		return err
	}
	today := now.In(p.Location).Format(time.DateOnly)
	for i := range days {
		value := score.N2R(h.input(days[i].LocalDate, days[i].LocalDate < today))
		days[i].N2R = &value
	}
	return nil
}
