package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/energy"
	"github.com/timkrebs/ryft/internal/evaluation"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition/score"
	"github.com/timkrebs/ryft/internal/periodization"
)

const coachingModelVersion = "1.0.0"

type coachingDue struct {
	Owner uuid.UUID
	Day   string
}

// RefreshCoaching captures a bounded batch of due forecasts and evaluation records.
func (s *Store) RefreshCoaching(ctx context.Context, now time.Time, policy string, enabled func(uuid.UUID) bool) error {
	rows, err := s.pool.Query(ctx, `SELECT owner_id,day::text FROM nutrition.coaching_due($1,$2)`, now, policy)
	if err != nil {
		return err
	}
	due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[coachingDue])
	if err != nil {
		return err
	}
	var result error
	const batchSize = 8
	count := 0
	for _, d := range due {
		if !enabled(d.Owner) {
			continue
		}
		result = errors.Join(result, s.captureCoachingDay(ctx, d.Owner, d.Day, now, policy))
		count++
		if count >= batchSize || ctx.Err() != nil {
			break
		}
	}
	return result
}

func (s *Store) captureCoachingDay(ctx context.Context, user uuid.UUID, key string, now time.Time, policy string) error {
	return s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if ok, err := coachingConsent(ctx, tx, user, policy); err != nil || !ok {
			return err
		}
		profile, err := readCoachingProfile(ctx, tx, user, true)
		if err != nil || profile == nil {
			return err
		}
		n2r, err := readN2RProfile(ctx, tx, user, false)
		if err != nil || n2r == nil {
			return err
		}
		day := parseN2RDay(key, n2r.Location)
		yesterday := day.AddDate(0, 0, -1).Format(time.DateOnly)
		h, err := readN2RHistory(ctx, tx, user, *n2r, yesterday, key, now)
		if err != nil {
			return err
		}
		state, ok := h.Coaching.plans[key]
		if !ok {
			return nil
		}
		projection := score.N2R(h.input(key, false))
		b, err := json.Marshal(projection)
		if err != nil {
			return err
		}
		data, err := json.Marshal(state.Plan)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO nutrition.daily_energy_states AS saved
			(user_id,day,goal,workout_source,sport,tdee_kcal,projected_kcal,gme,tissue_density,plan,n2r_projection,captured_at,model_version)
			VALUES($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13)
			ON CONFLICT(user_id,day) DO UPDATE SET goal=excluded.goal,workout_source=excluded.workout_source,sport=excluded.sport,
			tdee_kcal=excluded.tdee_kcal,projected_kcal=excluded.projected_kcal,gme=excluded.gme,tissue_density=excluded.tissue_density,
			plan=excluded.plan,n2r_projection=excluded.n2r_projection,captured_at=excluded.captured_at,model_version=excluded.model_version WHERE saved.captured_at<excluded.captured_at`,
			user, key, profile.Goal, state.Source, state.Sport, state.Adaptive.TDEE, state.Adaptive.Base+energy.Compensation*state.Plan.NetExercise,
			profile.GME, profile.Density, string(data), string(b), now, coachingModelVersion)
		if err != nil {
			return err
		}
		if err := writeCoachingEvaluation(ctx, tx, user, day, now, h); err != nil {
			return err
		}
		if profile.calibrated != nil && profile.calibrated.Format(time.DateOnly) >= yesterday {
			return nil
		}
		return tuneCoaching(ctx, tx, user, yesterday, day, *profile, h.Coaching.plans[yesterday].Feedback)
	})
}

func writeCoachingEvaluation(ctx context.Context, tx pgx.Tx, user uuid.UUID, day, now time.Time, h n2rHistory) error {
	yesterday := day.AddDate(0, 0, -1).Format(time.DateOnly)
	today := day.Format(time.DateOnly)
	frozen, err := readFrozenCoachDays(ctx, tx, user, day.AddDate(0, 0, -2).Format(time.DateOnly), yesterday, now)
	if err != nil {
		return err
	}
	saved, has := frozen[yesterday]
	if !has {
		return nil
	}
	state := h.Coaching.plans[today]
	prior := h.Coaching.plans[yesterday]
	profile := h.Coaching.profile
	recovery := state.Recovery.Today
	var residual *evaluation.Residual
	days := h.Coaching.days
	n := len(days)
	if n > 14 && state.Adaptive.WeightChange != nil {
		var forecasts []evaluation.ForecastDay
		window := days[n-15 : n-1]
		median := energy.MedianLogged(window)
		for _, d := range window {
			if f := h.Coaching.forecasts[d.Date.Format(time.DateOnly)]; f > 0 && energy.CredibleIntake(d, median) {
				forecasts = append(forecasts, evaluation.ForecastDay{Intake: *d.Intake, TDEE: f})
			}
		}
		residual = evaluation.WeightResidual(forecasts, *state.Adaptive.WeightChange, profile.Density)
	}
	var bias, mape, predicted, actual *float64
	if residual != nil {
		bias, mape, predicted, actual = &residual.BiasKcalDay, residual.AbsolutePercentageError, &residual.PredictedKG, &residual.ActualKG
	}
	power := 0.
	for _, d := range days[max(0, n-15):max(0, n-1)] {
		power += d.PowerKcal / 14
	}
	var hrv, rhr, sws, rem, probability *float64
	var outcome, recovered *bool
	if recovery != nil {
		hrv, rhr = recovery.RMSSDDelta, recovery.RHRDelta
		if hrv != nil && rhr != nil && recovery.SWC != nil && *recovery.SWC > 0 {
			isRed := *hrv <= -3*(*recovery.SWC) && *rhr >= 2
			outcome = &isRed
		}
	}
	if err := tx.QueryRow(ctx, `WITH native AS (
		SELECT sws_min,rem_min FROM nutrition.wearable_nights WHERE user_id=$1 AND wake_at>=$2 AND wake_at<$3 AND wake_at<=$4
		) SELECT coalesce((SELECT max(sws_min) FROM native), legacy.deep),
		coalesce((SELECT max(rem_min) FROM native),legacy.rem)
		FROM (SELECT max(deep_min)::float8 AS deep,max(rem_min)::float8 AS rem FROM daily_scores
			WHERE user_id=$1 AND day=$5::date AND sleep_has_stages AND sleep_end<=$4) legacy`, user, day, day.AddDate(0, 0, 1), now, today).Scan(&sws, &rem); err != nil {
		return err
	}
	var n2r, alc, caff, late, exposure, dose *float64
	attributed := false
	if p := saved.score; p != nil {
		n2r, alc, caff, late, exposure, dose = p.Score, &p.AlcoholPenalty, &p.CaffeinePenalty, &p.LateMealPenalty, p.CaffeineBedMg, &p.AlcoholGKG
		if n2r != nil {
			v := 1 - *n2r/100
			probability = &v
		}
		for _, t := range []string{"alcohol_above_035gkg", "caffeine_bed_high", "late_satfat_meal"} {
			attributed = attributed || slices.Contains(p.Tags, t)
		}
	}
	older := frozen[day.AddDate(0, 0, -2).Format(time.DateOnly)].state.Plan.Availability.State
	priorAvailability := saved.state.Plan.Availability.State
	if (older == periodization.AvailabilityRed || older == periodization.AvailabilityYellow) && priorAvailability != "" && priorAvailability != periodization.AvailabilityUnknown {
		ok := priorAvailability == periodization.AvailabilityGreen
		recovered = &ok
	}
	adherence := map[string]float64{}
	if n >= 2 {
		d := days[n-2]
		for name, pair := range map[string]struct {
			actual *float64
			target float64
		}{
			"energy": {d.Intake, saved.state.Plan.EnergyKcal}, "carbs": {d.Carbs, saved.state.Plan.CarbsG},
			"protein": {d.Protein, saved.state.Plan.ProteinG}, "fat": {d.Fat, saved.state.Plan.FatG},
		} {
			if pair.actual != nil && pair.target > 0 {
				adherence[name] = *pair.actual / pair.target
			}
		}
	}
	f := prior.Feedback
	if f.GIDistress != nil {
		adherence["gi_distress"] = *f.GIDistress
	}
	if f.RPE != nil {
		adherence["rpe"] = *f.RPE
	}
	if f.IntraCarbs != nil && saved.state.Plan.Fuel != nil && saved.state.Plan.Fuel.CarbMinGPerH > 0 {
		minutes := coachingVal(f.WorkoutMinutes, 0)
		if minutes <= 0 && f.Session != nil {
			minutes = f.Session.Minutes
		}
		if minutes > 0 {
			adherence["intra_workout_carbs"] = *f.IntraCarbs / (saved.state.Plan.Fuel.CarbMinGPerH * minutes / 60)
		}
	}
	data, err := json.Marshal(adherence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO nutrition.model_evaluation_logs
	(user_id,day,goal,workout_source,sport,energy_bias,weight_ape,predicted_kg,actual_kg,intake_confidence,cycling_power_kcal,
	n2r_score,alcohol_penalty,caffeine_penalty,late_meal_penalty,caffeine_exposure,alcohol_dose,hrv_delta,rhr_delta,sws_min,rem_min,
	red_probability,red_outcome,confounder_attributed,reported_confounder,intervention_recovered,adherence,model_version)
	VALUES($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27::jsonb,$28)
	ON CONFLICT(user_id,day) DO UPDATE SET energy_bias=excluded.energy_bias,weight_ape=excluded.weight_ape,predicted_kg=excluded.predicted_kg,
	actual_kg=excluded.actual_kg,intake_confidence=excluded.intake_confidence,cycling_power_kcal=excluded.cycling_power_kcal,
	hrv_delta=excluded.hrv_delta,rhr_delta=excluded.rhr_delta,sws_min=excluded.sws_min,rem_min=excluded.rem_min,
	red_outcome=excluded.red_outcome,reported_confounder=excluded.reported_confounder,
	intervention_recovered=excluded.intervention_recovered,adherence=excluded.adherence`,
		user, yesterday, saved.goal, saved.state.Source, saved.state.Sport, bias, mape, predicted, actual, state.Adaptive.Confidence, power,
		n2r, alc, caff, late, exposure, dose, hrv, rhr, sws, rem, probability, outcome, attributed, f.ReportedConfounder, recovered, string(data), saved.version)
	return err
}

func tuneCoaching(ctx context.Context, tx pgx.Tx, user uuid.UUID, date string, now time.Time, p coachingProfile, f model.CoachingFeedback) error {
	rows, err := tx.Query(ctx, `SELECT energy_bias,intake_confidence,cycling_power_kcal,predicted_kg,actual_kg,
		coalesce(caffeine_exposure,0),coalesce(alcohol_dose,0),coalesce(late_meal_penalty,0),hrv_delta,rhr_delta,sws_min
		FROM nutrition.model_evaluation_logs WHERE user_id=$1 AND day BETWEEN $2::date-29 AND $2::date ORDER BY day`, user, date)
	if err != nil {
		return err
	}
	var evidence []evaluation.Evidence
	var nights []evaluation.Night
	for rows.Next() {
		var e evaluation.Evidence
		var n evaluation.Night
		if err := rows.Scan(&e.Bias, &e.Confidence, &e.PowerKcal, &e.PredictedKG, &e.ActualKG, &n.Caffeine, &n.Alcohol, &n.Late, &n.HRV, &n.RHR, &n.SWS); err != nil {
			rows.Close()
			return err
		}
		evidence = append(evidence, e)
		nights = append(nights, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	transition := p.switched != nil && now.Before(p.switched.AddDate(0, 0, 28))
	if p.RaceDate != nil {
		race, err := time.Parse(time.DateOnly, *p.RaceDate)
		transition = transition || err == nil && !now.Before(race.AddDate(0, 0, -2)) && now.Before(race.AddDate(0, 0, 26))
	}
	updated := evaluation.Sweat(evaluation.Recovery(evaluation.EnergyParameters(p.Parameters, evidence, transition), nights), f)
	_, err = tx.Exec(ctx, `UPDATE nutrition.user_goal_profiles SET gme=$3,tissue_density=$4,caffeine_half_h=$5,
		alcohol_sensitivity=$6,sweat_rate_lph=$7,sweat_sodium_mg_l=$8,last_calibrated=$2::date WHERE user_id=$1`,
		user, date, updated.GME, updated.Density, updated.Caffeine, updated.Alcohol, updated.SweatRate, updated.SweatSodium)
	return err
}

// CoachingCohorts reads only aggregate metrics exposed by the operator function.
func (s *Store) CoachingCohorts(ctx context.Context, since string) ([]model.EvaluationCohort, error) {
	rows, err := s.pool.Query(ctx, `SELECT * FROM nutrition.evaluation_cohorts($1::date)`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.EvaluationCohort])
}
