package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition/score"
	"github.com/timkrebs/ryft/internal/periodization"
)

// N2RGuidance evaluates the account's current waking day and finds matching foods.
func (s *Store) N2RGuidance(ctx context.Context, user uuid.UUID, now time.Time) (model.N2RGuidance, error) {
	b, err := s.coachingBriefing(ctx, user, now, false)
	return model.N2RGuidance{Date: b.Date, Score: b.N2R, Biometrics: b.Biometrics, Cards: b.Cards}, err
}

// CoachingBriefing reuses the existing score and food guidance transaction.
func (s *Store) CoachingBriefing(ctx context.Context, user uuid.UUID, now time.Time) (model.CoachingBriefing, error) {
	return s.coachingBriefing(ctx, user, now, true)
}

func (s *Store) coachingBriefing(ctx context.Context, user uuid.UUID, now time.Time, briefing bool) (model.CoachingBriefing, error) {
	out := model.CoachingBriefing{Cards: []model.N2RGuidanceCard{}, Readiness: periodization.ReadinessUnknown, Reasons: []string{}}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		p, err := readN2RProfile(ctx, tx, user, false)
		if err != nil || p == nil {
			return err
		}
		today := now.In(p.Location).Format(time.DateOnly)
		out.Date = today
		if briefing {
			// Today's recovery, also when out.Date below is still yesterday; null while the flag is off.
			on, err := recoveryOn(ctx, tx, user)
			if err != nil {
				return err
			}
			if on {
				status, err := recoveryStatusTx(ctx, tx, user, p.Location, p, now)
				if err != nil {
					return err
				}
				out.Recovery = &status
			}
		}
		previous := parseN2RDay(today, p.Location).AddDate(0, 0, -1)
		if bed := expectedBedtime(previous, *p); bed != nil && !now.After(*bed) {
			out.Date = previous.Format(time.DateOnly)
		}
		fromDate := out.Date
		if briefing {
			fromDate = parseN2RDay(out.Date, p.Location).AddDate(0, 0, -1).Format(time.DateOnly)
		}
		h, err := readN2RHistory(ctx, tx, user, *p, fromDate, today, now)
		if err != nil {
			return err
		}
		in, bedtimeSource := h.inputSource(out.Date, false)
		if out.Date != today {
			// Early-morning intake before the expected sleep still informs live guidance.
			in.Meals = append(in.Meals[:len(in.Meals):len(in.Meals)], h.Meals[today]...)
		}
		result := score.N2R(in)
		result.BedtimeSource = bedtimeSource
		out.N2R = &result
		out.Biometrics, err = morningN2R(ctx, tx, user, in.Start, now)
		if err != nil {
			return err
		}
		latency, err := n2rSleepLatency(ctx, tx, user, out.Date, now)
		if err != nil {
			return err
		}
		var todayWorkouts []model.Workout
		from := in.Start
		if lastHour := now.Add(-time.Hour); lastHour.Before(from) {
			from = lastHour
		}
		for _, w := range h.Workouts {
			if !w.End.Before(from) && !w.End.After(now) {
				todayWorkouts = append(todayWorkouts, w)
			}
		}
		out.Cards = score.Guidance(score.GuidanceInput{Now: now, Score: result, Profile: in.Profile,
			Workouts: todayWorkouts, Meals: in.Meals, Morning: out.Biometrics, SleepLatencyMinutes: latency})
		if state, ok := h.Coaching.plans[out.Date]; ok {
			if fuel := state.Plan.Fuel; fuel != nil {
				for i := range out.Cards {
					if out.Cards[i].Code != score.GuideEndurance {
						continue
					}
					targets := out.Cards[i].Targets
					targets["post_workout_carb_budget_g"] = result.Budget.CarbAllowance
					if fuel.RehydrateL != nil {
						targets["post_workout_fluids_l"] = *fuel.RehydrateL
					}
					if fuel.SweatSodiumMg != nil {
						targets["workout_sweat_sodium_mg"] = *fuel.SweatSodiumMg
					}
				}
			}
			out.Plan = &state.Plan
			out.Readiness, out.Reasons, out.Load = state.Readiness, state.Reasons, state.Load
			out.Cards = append(coachingCards(state, result, now, h.Coaching.days, h.Coaching.plans[previous.Format(time.DateOnly)].Plan), out.Cards...)
			if briefing {
				yesterday := parseN2RDay(out.Date, p.Location).AddDate(0, 0, -1).Format(time.DateOnly)
				if yesterday >= fromDate {
					r := h.n2r(yesterday, true)
					out.Yesterday = &r
				}
			}
		}
		if len(out.Cards) == 0 {
			return nil
		}
		var vectorSchema *string
		if err := tx.QueryRow(ctx, `SELECT (SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
			WHERE e.extname='vector' AND to_regclass('nutrition.food_embeddings') IS NOT NULL
			AND has_schema_privilege(n.oid,'USAGE') AND has_table_privilege(to_regclass('nutrition.food_embeddings'),'SELECT'))`).Scan(&vectorSchema); err != nil {
			return err
		}
		for i := range out.Cards {
			card := &out.Cards[i]
			card.Foods, err = guidanceFoods(ctx, tx, card.Code, vectorSchema)
			if err != nil {
				return err
			}
		}
		return nil
	})
	out.ReadinessDisplay = periodization.ReadinessDisplay(out.Readiness)
	return out, err
}

type foodMatch struct{ text, criteria string }

var guidanceMatches = map[string]foodMatch{
	score.GuideEndurance: {`(sport & drink) | isotonic | electrolyte | isotonisch | sportgetrank | reiswaffel | (white & rice) | dextrose`,
		`coalesce(carbs, total_carbs) >= 5 AND fat <= 5`},
	score.GuideStrength: {`skyr | casein | quark | magerquark | (greek & yogurt)`,
		`protein >= 8 AND fat <= 3 AND coalesce(satfat, fat) <= .5 AND (added_sugar=0 OR added_sugar IS NULL AND sugar<=4 AND normalized ~ '\m(plain|natur|unsweetened|ungesusst|magerquark)\M')`},
	score.GuideCaffeine: {`decaf | decaffeinated | entkoffeiniert | krautertee | chamomile | kamillentee | rooibos | (herbal & tea)`,
		`caffeine <= 5 OR caffeine IS NULL AND normalized ~ '\m(decaf|decaffeinated|entkoffeiniert|krautertee|chamomile|kamillentee|rooibos)\M'`},
	score.GuideLate: {`broccoli | brokkoli | blumenkohl | cauliflower | zucchini | tofu | huhnerbrust | (chicken & breast)`,
		`fat <= 5 AND (protein >= 8 OR vegetable) AND (vegetable OR normalized !~ '\m(raw|roh)\M')`},
	score.GuideAlcohol: {`(mineral & water) | mineralwasser | electrolyte | isotonic | isotonisch`,
		`coalesce(alcohol,0)=0 AND normalized !~ '\m(beer|bier|wine|wein|vodka|spirit)\M' AND (sodium >= 20 OR normalized ~ '\m(water|wasser|mineralwasser)\M')`},
	score.GuideRed: {`oats | haferflocken | skyr | quark | banana | banane`, `protein >= 8 OR coalesce(carbs,total_carbs) >= 15`},
}

func guidanceFoods(ctx context.Context, tx pgx.Tx, code string, vectorSchema *string) ([]model.FoodSummary, error) {
	if code == "PLANNED_SESSION_FUEL" {
		code = score.GuideEndurance
	}
	if code == "EVENING_FUEL" {
		code = score.GuideLate
	}
	match, ok := guidanceMatches[code]
	if !ok {
		return []model.FoodSummary{}, nil
	}
	base := `WITH candidates AS MATERIALIZED (
		SELECT f.id,f.canonical_name,f.brand,f.food_type,f.quality,f.completeness,min(n.normalized) AS normalized
		FROM nutrition.food_names n JOIN nutrition.foods f ON f.id=n.food_id
		WHERE f.owner_user_id IS NULL AND f.status='active' AND n.document @@ to_tsquery('pg_catalog.simple',$1)
		GROUP BY f.id ORDER BY f.completeness DESC,f.id LIMIT 120
	), measured AS (
		SELECT c.*, v.*, EXISTS (SELECT FROM nutrition.classifications a WHERE a.food_id=c.id
			AND a.system='ahei_category' AND a.version='1' AND a.code='non_starchy_veg') AS vegetable
		FROM candidates c JOIN LATERAL (
			SELECT max(amount) FILTER (WHERE code='carbohydrates') AS carbs,
				max(amount) FILTER (WHERE code='carbohydrates_total') AS total_carbs,
				max(amount) FILTER (WHERE code='fat') AS fat,
				max(amount) FILTER (WHERE code='protein') AS protein,
				max(amount) FILTER (WHERE code='saturated_fat') AS satfat,
				max(amount) FILTER (WHERE code='added_sugar') AS added_sugar,
				max(amount) FILTER (WHERE code='total_sugar') AS sugar,
				max(amount) FILTER (WHERE code='caffeine') AS caffeine,
				max(amount) FILTER (WHERE code='alcohol') AS alcohol,
				max(amount) FILTER (WHERE code='sodium') AS sodium
			FROM nutrition.nutrient_values v WHERE v.food_id=c.id AND v.reference_amount=100 AND v.reference_unit IN ('g','ml')
			GROUP BY v.source_id,v.reference_unit
			ORDER BY count(*) DESC,v.source_id,v.reference_unit LIMIT 1
		) v ON true
	), eligible AS MATERIALIZED (SELECT * FROM measured WHERE ` + match.criteria + `)`
	query := base + ` SELECT id,canonical_name,brand,food_type,quality FROM eligible ORDER BY completeness DESC, id LIMIT 4`
	if vectorSchema != nil {
		operator := `OPERATOR(` + pgx.Identifier{*vectorSchema}.Sanitize() + `.<=>)`
		query = base + `, anchor AS (
			SELECT e.embedding FROM eligible c JOIN nutrition.food_embeddings e ON e.food_id=c.id
			WHERE e.model='text-embedding-3-small' ORDER BY c.completeness DESC,c.id LIMIT 1
		) SELECT c.id,c.canonical_name,c.brand,c.food_type,c.quality FROM eligible c
		LEFT JOIN nutrition.food_embeddings e ON e.food_id=c.id AND e.model='text-embedding-3-small'
		LEFT JOIN anchor a ON true ORDER BY e.embedding ` + operator + ` a.embedding NULLS LAST,c.completeness DESC,c.id LIMIT 4`
	}
	rows, err := tx.Query(ctx, query, match.text)
	if err != nil {
		return nil, err
	}
	foods, err := pgx.CollectRows(rows, scanFoodSummary)
	if foods == nil {
		foods = []model.FoodSummary{}
	}
	return foods, err
}
