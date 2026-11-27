package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/evaluation"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/wearables"
)

const coachingHistoryDays = 45

type coachingProfile struct {
	model.GoalProfile
	evaluation.Parameters
	active     time.Time
	switched   *time.Time
	calibrated *time.Time
	birthYear  *int
	maxHR      int
}

func readCoachingProfile(ctx context.Context, tx pgx.Tx, user uuid.UUID, lock bool) (*coachingProfile, error) {
	var p coachingProfile
	var data []byte
	sql := `SELECT p.settings,p.active_from,p.switched_on,p.gme,p.tissue_density,coalesce(p.caffeine_half_h,5),
		p.alcohol_sensitivity,p.sweat_rate_lph,p.sweat_sodium_mg_l,p.last_calibrated,u.birth_year,u.max_hr
		FROM nutrition.user_goal_profiles p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1`
	if lock {
		sql += " FOR UPDATE OF p"
	}
	err := tx.QueryRow(ctx, sql, user).Scan(&data, &p.active, &p.switched, &p.GME, &p.Density, &p.Caffeine, &p.Alcohol,
		&p.SweatRate, &p.Parameters.SweatSodium, &p.calibrated, &p.birthYear, &p.maxHR)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &p.GoalProfile); err != nil {
		return nil, err
	}
	return &p, nil
}

// CoachingGoal returns account-scoped coaching settings.
func (s *Store) CoachingGoal(ctx context.Context, user uuid.UUID) (*model.GoalProfile, error) {
	var out *model.GoalProfile
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p, err := readCoachingProfile(ctx, tx, user, false)
		if p != nil {
			out = &p.GoalProfile
		}
		return err
	})
	return out, err
}

// PutCoachingGoal enrolls an adult account or replaces its goal settings.
func (s *Store) PutCoachingGoal(ctx context.Context, user uuid.UUID, p model.GoalProfile, at time.Time) (model.GoalProfile, error) {
	if !periodization.ValidProfile(p) {
		return p, nutrition.ErrInvalid
	}
	data, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	err = s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var birth *int
		var zone string
		if err := tx.QueryRow(ctx, `SELECT birth_year,tz FROM users WHERE id=$1`, user).Scan(&birth, &zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		at = at.In(loc)
		if birth == nil || at.Year()-*birth < 19 {
			return nutrition.ErrInvalid
		}
		_, err = tx.Exec(ctx, `INSERT INTO nutrition.user_goal_profiles AS c(user_id,settings,goal,active_from)
			VALUES($1,$2::jsonb,$3,$4::date) ON CONFLICT(user_id) DO UPDATE
			SET settings=excluded.settings,goal=excluded.goal,
			switched_on=CASE WHEN c.goal<>excluded.goal THEN excluded.active_from ELSE c.switched_on END`,
			user, string(data), p.Goal, at.Format(time.DateOnly))
		return err
	})
	return p, err
}

// DeleteCoachingGoal removes the coaching enrollment and associated history.
func (s *Store) DeleteCoachingGoal(ctx context.Context, user uuid.UUID) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Serialize opt-out with a delivery applying under this goal before removing its data.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM nutrition.user_goal_profiles WHERE user_id=$1 FOR UPDATE`, user); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM nutrition.canonical_workouts WHERE user_id=$1`, user); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM nutrition.user_goal_profiles WHERE user_id=$1`, user)
		return err
	})
}

// PutCoachingFeedback validates and replaces feedback for one local day.
func (s *Store) PutCoachingFeedback(ctx context.Context, user uuid.UUID, date string, f model.CoachingFeedback, at time.Time) (model.CoachingFeedback, error) {
	if !periodization.ValidFeedback(f) {
		return f, nutrition.ErrInvalid
	}
	data, err := json.Marshal(f)
	if err != nil {
		return f, err
	}
	err = s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		p, err := readCoachingProfile(ctx, tx, user, true)
		if err != nil {
			return err
		}
		if p == nil {
			return ErrNotFound
		}
		var zone string
		if err := tx.QueryRow(ctx, `SELECT tz FROM users WHERE id=$1`, user).Scan(&zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		day, err := time.ParseInLocation(time.DateOnly, date, loc)
		today := parseN2RDay(at.In(loc).Format(time.DateOnly), loc)
		if err != nil || day.Before(today.AddDate(0, 0, -45)) || day.After(today.AddDate(0, 0, 28)) ||
			f.Session != nil && f.Session.Start.In(loc).Format(time.DateOnly) != date {
			return nutrition.ErrInvalid
		}
		_, err = tx.Exec(ctx, `INSERT INTO nutrition.coaching_feedback(user_id,day,data) VALUES($1,$2::date,$3::jsonb)
			ON CONFLICT(user_id,day) DO UPDATE SET data=excluded.data`, user, date, string(data))
		return err
	})
	return f, err
}

type coachingPlanDay struct {
	Plan          model.CoachingPlan
	Load          model.CoachingLoad
	Readiness     string
	Reasons       []string
	Physiology    energy.Physiology
	Adaptive      energy.Adaptive
	Recovery      periodization.Recovery
	Feedback      model.CoachingFeedback
	Source, Sport string
}

type coachingData struct {
	profile   *coachingProfile
	workouts  []wearables.Workout
	days      []energy.Day
	plans     map[string]coachingPlanDay
	forecasts map[string]float64
}

// readWorkoutFacts reads the workouts of the coaching load. With ratings (the flag recovery.checkin is on) each
// carries the user's session RPE (D293); without, none does, whatever is stored.
func readWorkoutFacts(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, to time.Time, ratings bool) ([]wearables.Workout, error) {
	rows, err := tx.Query(ctx, `SELECT source_provider,external_id,coalesce(parent_workout_id,''),'' AS label,'' AS bundle,
		was_user_entered,start_at,end_at,tz_offset_sec,sport_type,duration_sec,active_kcal_raw,mechanical_work_kj,distance_m,avg_hr,max_hr,
		normalized_power_w,ftp_w,rr_intervals_available,0::float8,0::float8
		FROM nutrition.canonical_workouts WHERE user_id=$1 AND start_at >= $2 AND end_at <= $3
		UNION ALL
		SELECT CASE WHEN w.source=$4 THEN 'MANUAL' ELSE 'APPLE_HEALTHKIT' END,w.id::text,'',w.source,coalesce(w.coaching_source_bundle_id,''),
		coalesce(w.coaching_user_entered,false),w.start_at,w.end_at,coalesce(w.coaching_tz_offset_sec,0),w.activity,
		extract(epoch from w.end_at-w.start_at)::float8,coalesce(w.energy_kcal,0),0,coalesce(w.distance_m,0),coalesce(w.avg_hr,0),
		coalesce(w.max_hr,0),0,0,false,coalesce(w.strain,0),
		CASE WHEN $5::bool THEN coalesce((SELECT r.session_rpe::float8 FROM public.workout_ratings r
			WHERE r.user_id=w.user_id AND r.workout_id=w.id),0) ELSE 0 END
		FROM public.workouts w WHERE w.user_id=$1 AND w.start_at >= $2 AND w.end_at <= $3`,
		user, from, to, model.SessionSource, ratings)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (wearables.Workout, error) {
		var w wearables.Workout
		err := row.Scan(&w.Provider, &w.ExternalID, &w.ParentID, &w.Source, &w.Bundle, &w.UserEntered, &w.Start, &w.End, &w.OffsetSec, &w.Sport, &w.DurationSec,
			&w.RawKcal, &w.WorkKJ, &w.DistanceM, &w.AvgHR, &w.MaxHR, &w.NormalizedPower, &w.FTP, &w.HasRR, &w.Strain, &w.RPE)
		if w.Source == "" {
			w.Source = string(w.Provider)
		}
		return w, err
	})
	if err != nil {
		return nil, err
	}
	return wearables.Reconcile(out), nil
}

type intakeRow struct {
	kcal, carb, protein, fat *float64
}

func readCoachIntake(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, to string, asOf time.Time) (map[string]intakeRow, error) {
	rows, err := tx.Query(ctx, `WITH items AS (
			SELECT m.local_date,i.nutrients->'energy_kcal'->>'amount' AS kcal,
			coalesce(i.nutrients->'carbohydrates'->>'amount',i.nutrients->'carbohydrates_total'->>'amount') AS carb,
			i.nutrients->'protein'->>'amount' AS protein,i.nutrients->'fat'->>'amount' AS fat
			FROM nutrition.meals m JOIN nutrition.meal_items i ON i.meal_id=m.id AND i.user_id=m.user_id
			WHERE m.user_id=$1 AND m.local_date BETWEEN $2::date AND $3::date AND m.consumed_at<=$4)
		SELECT local_date::text,CASE WHEN count(kcal)=count(*) THEN sum(kcal::float8) END,
		CASE WHEN count(carb)=count(*) THEN sum(carb::float8) END,
		CASE WHEN count(protein)=count(*) THEN sum(protein::float8) END,
		CASE WHEN count(fat)=count(*) THEN sum(fat::float8) END FROM items GROUP BY local_date`, user, from, to, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]intakeRow{}
	for rows.Next() {
		var day string
		var x intakeRow
		if err := rows.Scan(&day, &x.kcal, &x.carb, &x.protein, &x.fat); err != nil {
			return nil, err
		}
		out[day] = x
	}
	return out, rows.Err()
}

func readDailyFeedback(ctx context.Context, tx pgx.Tx, user uuid.UUID, start, end string) (map[string]model.CoachingFeedback, error) {
	rows, err := tx.Query(ctx, `SELECT day::text,data FROM nutrition.coaching_feedback WHERE user_id=$1 AND day BETWEEN $2::date AND $3::date`, user, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.CoachingFeedback{}
	for rows.Next() {
		var day string
		var data []byte
		var f model.CoachingFeedback
		if err := rows.Scan(&day, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		out[day] = f
	}
	return out, rows.Err()
}

func readWeights(ctx context.Context, tx pgx.Tx, user uuid.UUID, from, asOf time.Time, loc *time.Location) (map[string]float64, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (day) day::text,value FROM (
		SELECT (start_at AT TIME ZONE $4)::date AS day,value,start_at,id FROM public.samples
		WHERE user_id=$1 AND type='body_mass' AND unit='kg' AND value BETWEEN 25 AND 350 AND start_at BETWEEN $2 AND $3
				UNION ALL SELECT (at_time AT TIME ZONE $4)::date,mass_kg,at_time,0
		FROM nutrition.wearable_weights WHERE user_id=$1 AND at_time BETWEEN $2 AND $3
		) x ORDER BY day,start_at DESC,id DESC`, user, from, asOf, loc.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var day string
		var value float64
		if err := rows.Scan(&day, &value); err != nil {
			return nil, err
		}
		out[day] = value
	}
	return out, rows.Err()
}

type frozenCoachDay struct {
	goal, version    string
	state            coachingPlanDay
	tdee, projection float64
	score            *model.N2RScore
}

func readFrozenCoachDays(ctx context.Context, tx pgx.Tx, user uuid.UUID, start, end string, asOf time.Time) (map[string]frozenCoachDay, error) {
	rows, err := tx.Query(ctx, `SELECT day::text,tdee_kcal,projected_kcal,plan,workout_source,sport,n2r_projection,goal,model_version FROM nutrition.daily_energy_states
	WHERE user_id=$1 AND day BETWEEN $2::date AND $3::date AND captured_at<=$4`, user, start, end, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]frozenCoachDay{}
	for rows.Next() {
		var day string
		var x frozenCoachDay
		var plan, prediction []byte
		if err := rows.Scan(&day, &x.tdee, &x.projection, &plan, &x.state.Source, &x.state.Sport, &prediction, &x.goal, &x.version); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(plan, &x.state.Plan); err != nil {
			return nil, err
		}
		if prediction != nil {
			if err := json.Unmarshal(prediction, &x.score); err != nil {
				return nil, err
			}
		}
		out[day] = x
	}
	return out, rows.Err()
}

type coachNight struct {
	b       model.N2RBiometrics
	score   *model.N2RScore
	resting *float64
}

func readCoachNights(ctx context.Context, tx pgx.Tx, user uuid.UUID, start, end string, asOf time.Time) (map[string]coachNight, error) {
	rows, err := tx.Query(ctx, `SELECT coalesce(b.wake_date,d.day)::text,b.ln_rmssd_7d_cv,b.ln_rmssd_delta,b.rhr_delta,
	b.swc,b.n2r_score_breakdown,d.rhr FROM
	(SELECT * FROM nutrition.daily_biometric_baselines WHERE user_id=$1 AND wake_date BETWEEN $2::date AND $3::date AND processed_at<=$4) b
	FULL JOIN (SELECT day,rhr FROM public.daily_scores WHERE user_id=$1 AND day BETWEEN $2::date AND $3::date
	AND (sleep_end IS NULL OR sleep_end<=$4)) d ON d.day=b.wake_date`, user, start, end, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]coachNight{}
	for rows.Next() {
		var day string
		var x coachNight
		var data []byte
		if err := rows.Scan(&day, &x.b.RMSSDCV, &x.b.RMSSDDelta, &x.b.RHRDelta, &x.b.SWC, &data, &x.resting); err != nil {
			return nil, err
		}
		x.b.WakeDate = day
		if data != nil {
			if err := json.Unmarshal(data, &x.score); err != nil {
				return nil, err
			}
		}
		out[day] = x
	}
	return out, rows.Err()
}

// loadCoaching builds the days of the coaching load, readiness and plan. withRecovery says the flag recovery.checkin
// is on: only then do a current check-in count for readiness where the HRV path yields UNKNOWN (D294) and a
// session RPE for the load of a workout without heart rate or power (D293).
func loadCoaching(ctx context.Context, tx pgx.Tx, user uuid.UUID, p n2rProfile, first, last, asOf time.Time, profile *coachingProfile, withRecovery bool) (coachingData, error) {
	var out coachingData
	if profile == nil || profile.birthYear == nil {
		return out, nil
	}
	out.profile = profile
	start := first.AddDate(0, 0, -coachingHistoryDays)
	firstKey, lastKey := start.Format(time.DateOnly), last.Format(time.DateOnly)
	workouts, err := readWorkoutFacts(ctx, tx, user, start.Add(-48*time.Hour), asOf, withRecovery)
	if err != nil {
		return out, err
	}
	out.workouts = workouts
	var checkins map[string]*periodization.CheckinState
	if withRecovery {
		if checkins, err = readCheckinStates(ctx, tx, user, firstKey, lastKey); err != nil {
			return out, err
		}
	}
	intakes, err := readCoachIntake(ctx, tx, user, firstKey, lastKey, asOf)
	if err != nil {
		return out, err
	}
	weights, err := readWeights(ctx, tx, user, start, asOf, p.Location)
	if err != nil {
		return out, err
	}
	feedback, err := readDailyFeedback(ctx, tx, user, firstKey, lastKey)
	if err != nil {
		return out, err
	}
	nights, err := readCoachNights(ctx, tx, user, firstKey, lastKey, asOf)
	if err != nil {
		return out, err
	}
	frozen, err := readFrozenCoachDays(ctx, tx, user, firstKey, lastKey, asOf)
	if err != nil {
		return out, err
	}
	out.plans = map[string]coachingPlanDay{}
	out.forecasts = map[string]float64{}
	active := profile.active.Format(time.DateOnly)
	lastMass := p.BodyMassKG
	for day := start; !day.After(last); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		next := day.AddDate(0, 0, 1)
		f := feedback[key]
		i := intakes[key]
		input := energy.Day{Date: day, Intake: i.kcal, Carbs: i.carb, Protein: i.protein, Fat: i.fat, Fasting: f.Fasting, IntakeConfirmed: f.IntakeComplete}
		if w, ok := weights[key]; ok {
			input.Weight = &w
			lastMass = w
		}
		if f.TrackedCycles >= 2 && f.CycleWaterKG != nil {
			input.WaterOffset = *f.CycleWaterKG
		}
		if input.Fasting && input.Intake == nil {
			zero := 0.
			input.Intake = &zero
			input.Carbs = &zero
		}
		age := day.Year() - *profile.birthYear
		phys := energy.Physiology{MassKG: lastMass, RMR: energy.MifflinStJeor(lastMass, profile.HeightCM, age, p.Sex), GME: profile.GME, FTP: coachingVal(profile.FTP, 0), MaxHR: coachingVal(profile.HRMax, float64(profile.maxHR)), Sex: p.Sex}
		bio := nights[key].b
		prior := nights[day.AddDate(0, 0, -1).Format(time.DateOnly)].b
		phys.RestHR = coachingVal(profile.HRRest, coachingVal(nights[key].resting, coachingVal(nights[day.AddDate(0, 0, -1).Format(time.DateOnly)].resting, 0)))
		minutes, peak, power := 0., 0., 0.
		source, sport := "none", "none"
		for _, w := range workouts {
			if !w.Start.Before(next) || !day.Before(w.End) {
				continue
			}
			clip := w.Slice(maxTimeForCoach(day, w.Start), minTimeForCoach(next, w.End))
			x := energy.RatedWorkout(clip, phys, w.RPE)
			input.NetExercise += x.NetKcal
			input.Load += x.Load()
			minutes += clip.Seconds() / 60
			reserve, _ := energy.Reserve(clip, phys)
			peak = max(reserve, peak)
			if clip.Priority() == 1 {
				power += x.NetKcal
			}
			if source == "none" || x.NetKcal > 0 && clip.Seconds() >= minutes*30 {
				source, sport = string(clip.Provider), normalizedCohortSport(clip.Sport)
			}
		}
		input.HighStrain = peak >= .75 || minutes >= 90
		if key == lastKey {
			if morning, err := morningN2R(ctx, tx, user, day, asOf); err != nil {
				return out, err
			} else if morning != nil {
				bio = *morning
			}
			if prior.RMSSDCV == nil {
				if previous, err := morningN2R(ctx, tx, user, day.AddDate(0, 0, -1), asOf); err != nil {
					return out, err
				} else if previous != nil {
					prior = *previous
				}
			}
		}
		var recovery periodization.Recovery
		recovery.Today, recovery.Prior = &bio, &prior
		recovery.Fatigue = f.Fatigue
		if n := nights[key].score; n != nil {
			recovery.YesterdayTags = n.Tags
		}
		if f.MeasuredRMR != nil && phys.RMR > 0 {
			v := *f.MeasuredRMR / phys.RMR
			recovery.RMRRatio = &v
		}
		for offset := 0; offset < 7; offset++ {
			b := nights[day.AddDate(0, 0, -offset).Format(time.DateOnly)].b
			if b.RMSSDDelta == nil || b.SWC == nil || *b.SWC <= 0 || *b.RMSSDDelta > -3*(*b.SWC) {
				break
			}
			recovery.PersistentHRVDays++
		}
		ffm := 0.
		if profile.BodyFatPct != nil {
			ffm = lastMass * (1 - *profile.BodyFatPct/100)
		}
		ea := periodization.CheckAvailability(out.days, lastMass, ffm, p.Sex, recovery)
		input.PowerKcal = power
		out.days = append(out.days, input)
		if frozenDay, ok := frozen[key]; ok {
			out.forecasts[key] = frozenDay.projection
		}
		if day.Before(first) || key < active || phys.RMR <= 0 {
			continue
		}
		prev := frozen[day.AddDate(0, 0, -1).Format(time.DateOnly)].tdee
		var switched time.Time
		if profile.switched != nil {
			switched = parseN2RDay(profile.switched.Format(time.DateOnly), p.Location)
		}
		priorTDEE := phys.RMR * energy.BaselineActivity
		if lastMass > 0 && key >= active && profile.RaceDate != nil {
			race, parseErr := time.ParseInLocation(time.DateOnly, *profile.RaceDate, p.Location)
			if parseErr == nil && !day.Before(race.AddDate(0, 0, -2)) && day.Before(race.AddDate(0, 0, 26)) {
				switched = maxTimeForCoach(switched, race.AddDate(0, 0, -2))
			}
		}
		adapt := energy.Learn(out.days, priorTDEE, prev, profile.Density, switched)
		ready, reasons := periodization.ReadinessWithCheckin(recovery, ea, checkins[key])
		netWorkout := input.NetExercise
		if f.Session != nil && minutes == 0 && f.Session.Class != periodization.Rest && f.Session.Start.After(asOf) {
			w := wearables.Workout{Sport: f.Session.Sport, Start: f.Session.Start, End: f.Session.Start.Add(time.Duration(f.Session.Minutes * float64(time.Minute)))}
			netWorkout = energy.Workout(w, phys).NetKcal
		}
		var lost *float64
		if f.PreMass != nil && f.PostMass != nil {
			v := *f.PreMass - *f.PostMass
			lost = &v
		}
		plan := periodization.Plan(periodization.Input{Profile: profile.GoalProfile, Day: day, Sex: p.Sex, Mass: lastMass, FFM: ffm,
			Base: adapt.Base, NetExercise: netWorkout, Minutes: minutes, PeakReserve: peak, Availability: ea, Readiness: ready, ReadinessReasons: reasons,
			SweatRate:   coachingVal(profile.SweatRate, coachingVal(p.SweatRateLPH, .8)),
			SweatSodium: coachingVal(profile.Parameters.SweatSodium, coachingVal(profile.GoalProfile.SweatSodium, coachingVal(p.SweatSodiumMgL, 900))), Session: f.Session, LostMass: lost, WeightChange: adapt.WeightChange})
		state := coachingPlanDay{Plan: plan, Load: energy.Load(out.days), Readiness: ready, Reasons: reasons,
			Physiology: phys, Adaptive: adapt, Recovery: recovery, Feedback: f, Source: source, Sport: sport}
		if saved, ok := frozen[key]; ok && key != asOf.In(p.Location).Format(time.DateOnly) {
			state.Plan = saved.state.Plan
		}
		out.plans[key] = state
	}
	return out, nil
}

func coachingVal(v *float64, fallback float64) float64 {
	if v != nil {
		return *v
	}
	return fallback
}
func maxTimeForCoach(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTimeForCoach(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func normalizedCohortSport(s string) string {
	switch wearables.Sport(s) {
	case "cycling", "running", "swimming", "rowing", "strength", "walking", "hiit":
		return wearables.Sport(s)
	}
	return "other"
}
