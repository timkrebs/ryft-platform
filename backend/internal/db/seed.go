package db

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// The built-in exercise library and plan templates (Train, migration 0011) and
// the journal's behaviors (migration 0016).
//
//go:embed seed/exercises.json seed/plans/*.json seed/behaviors.json
var seeds embed.FS

type seedBehavior struct {
	Slug      string `json:"slug"`
	NameEN    string `json:"name_en"`
	NameDE    string `json:"name_de"`
	Category  string `json:"category"`
	Kind      string `json:"kind"`
	DefaultOn bool   `json:"default_on"`
	Sort      int    `json:"sort"`
}

type seedExercise struct {
	Slug             string   `json:"slug"`
	NameEN           string   `json:"name_en"`
	NameDE           string   `json:"name_de"`
	Kind             string   `json:"kind"`
	PrimaryMuscles   []string `json:"primary_muscles"`
	SecondaryMuscles []string `json:"secondary_muscles"`
	Equipment        []string `json:"equipment"`
	LoadType         string   `json:"load_type"`
	BodyweightFactor float64  `json:"bodyweight_factor"`
	LoadFactor       float64  `json:"load_factor"`
	IsUnilateral     bool     `json:"is_unilateral"`
}

type seedPlan struct {
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	Goal        string             `json:"goal"`
	Weeks       *int               `json:"weeks"`
	DaysPerWeek int                `json:"days_per_week"`
	Description string             `json:"description"`
	Progression *model.Progression `json:"progression"`
	Days        []struct {
		Week      *int   `json:"week"`
		DayIndex  int    `json:"day_index"`
		Name      string `json:"name"`
		Exercises []struct {
			Exercise      string   `json:"exercise"` // slug
			Sets          int      `json:"sets"`
			RepsMin       int      `json:"reps_min"`
			RepsMax       int      `json:"reps_max"`
			TargetKind    string   `json:"target_kind"`
			TargetValue   *float64 `json:"target_value"`
			RestSec       int      `json:"rest_sec"`
			SupersetGroup *int     `json:"superset_group"`
			Notes         string   `json:"notes"`
		} `json:"exercises"`
	} `json:"days"`
}

// Builtins is the parsed seed content.
type Builtins struct {
	exercises []seedExercise
	plans     []seedPlan
	behaviors []seedBehavior
}

// LoadBuiltins parses and checks the embedded seeds.
func LoadBuiltins() (Builtins, error) {
	var b Builtins
	raw, err := seeds.ReadFile("seed/exercises.json")
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(raw, &b.exercises); err != nil {
		return b, fmt.Errorf("seed/exercises.json: %w", err)
	}
	slugs := map[string]bool{}
	for _, e := range b.exercises {
		ex := model.Exercise{NameEN: e.NameEN, NameDE: e.NameDE, Kind: e.Kind, LoadType: e.LoadType,
			PrimaryMuscles: e.PrimaryMuscles, SecondaryMuscles: e.SecondaryMuscles, Equipment: e.Equipment,
			BodyweightFactor: e.BodyweightFactor}
		if err := train.CheckExercise(ex); err != nil {
			return b, fmt.Errorf("seed exercise %q: %w", e.Slug, err)
		}
		if slugs[e.Slug] || e.Slug != train.Slug(e.Slug) || e.LoadFactor <= 0 {
			return b, fmt.Errorf("seed exercise %q: duplicate or malformed slug, or no load factor", e.Slug)
		}
		slugs[e.Slug] = true
	}
	files, err := fs.Glob(seeds, "seed/plans/*.json")
	if err != nil {
		return b, err
	}
	slices.Sort(files)
	for _, f := range files {
		raw, err := seeds.ReadFile(f)
		if err != nil {
			return b, err
		}
		var p seedPlan
		if err := json.Unmarshal(raw, &p); err != nil {
			return b, fmt.Errorf("%s: %w", f, err)
		}
		if p.Slug+".json" != path.Base(f) {
			return b, fmt.Errorf("%s: slug %q must match the file name", f, p.Slug)
		}
		if err := train.CheckPlan(p.asPlan()); err != nil {
			return b, fmt.Errorf("%s: %w", f, err)
		}
		for _, d := range p.Days {
			for _, e := range d.Exercises {
				if !slugs[e.Exercise] {
					return b, fmt.Errorf("%s: unknown exercise %q", f, e.Exercise)
				}
			}
		}
		b.plans = append(b.plans, p)
	}
	raw, err = seeds.ReadFile("seed/behaviors.json")
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(raw, &b.behaviors); err != nil {
		return b, fmt.Errorf("seed/behaviors.json: %w", err)
	}
	seen := map[string]bool{}
	for _, s := range b.behaviors {
		if seen[s.Slug] || s.Slug != behavior.Slug(s.Slug) || s.NameEN == "" || s.NameDE == "" ||
			!slices.Contains(model.BehaviorKinds, s.Kind) || !slices.Contains(model.BehaviorCategories, s.Category) || s.Category == "custom" {
			return b, fmt.Errorf("seed behavior %q: duplicate or malformed slug, a missing name, or an unknown kind or category", s.Slug)
		}
		seen[s.Slug] = true
	}
	return b, nil
}

func week(w *int) int {
	if w == nil {
		return 0
	}
	return *w
}

// asPlan is the plan with its fixed ids, as the API would serve it.
func (p seedPlan) asPlan() model.Plan {
	out := model.Plan{ID: train.BuiltinPlanID(p.Slug), Slug: &p.Slug, Name: p.Name, Goal: p.Goal, Weeks: p.Weeks,
		DaysPerWeek: p.DaysPerWeek, Description: p.Description, Progression: p.Progression, IsBuiltin: true}
	for _, d := range p.Days {
		day := model.PlanDay{ID: train.BuiltinPlanDayID(p.Slug, week(d.Week), d.DayIndex), Week: d.Week, DayIndex: d.DayIndex, Name: d.Name}
		for pos, e := range d.Exercises {
			day.Exercises = append(day.Exercises, model.PlanExercise{
				ID:         train.BuiltinPlanExerciseID(p.Slug, week(d.Week), d.DayIndex, pos),
				ExerciseID: train.BuiltinExerciseID(e.Exercise), Position: pos, Sets: e.Sets, RepsMin: e.RepsMin,
				RepsMax: e.RepsMax, TargetKind: e.TargetKind, TargetValue: e.TargetValue, RestSec: e.RestSec,
				SupersetGroup: e.SupersetGroup, Notes: e.Notes})
		}
		out.Days = append(out.Days, day)
	}
	return out
}

// Seed writes the built-in exercises and plans (user_id NULL) under the
// migration lock. Idempotent: rows are upserted by slug and fixed id and only
// touched when they changed; a built-in that left the seeds is archived (old
// sessions still refer to it), a plan day or plan exercise that left them is
// deleted (sessions keep their sets; their references become NULL).
func Seed(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	b, err := LoadBuiltins()
	if err != nil {
		return err
	}
	var changed int64
	err = locked(ctx, pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		slugs := make([]string, 0, len(b.exercises))
		for _, e := range b.exercises {
			slugs = append(slugs, e.Slug)
			batch.Queue(`INSERT INTO exercises (id, slug, name_en, name_de, kind, primary_muscles, secondary_muscles,
				equipment, load_type, bodyweight_factor, load_factor, is_unilateral)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
				ON CONFLICT (slug) WHERE user_id IS NULL DO UPDATE SET name_en=EXCLUDED.name_en, name_de=EXCLUDED.name_de,
				  kind=EXCLUDED.kind, primary_muscles=EXCLUDED.primary_muscles, secondary_muscles=EXCLUDED.secondary_muscles,
				  equipment=EXCLUDED.equipment, load_type=EXCLUDED.load_type, bodyweight_factor=EXCLUDED.bodyweight_factor,
				  load_factor=EXCLUDED.load_factor, is_unilateral=EXCLUDED.is_unilateral, archived=false, updated_at=now()
				WHERE (exercises.name_en, exercises.name_de, exercises.kind, exercises.primary_muscles, exercises.secondary_muscles,
				       exercises.equipment, exercises.load_type, exercises.bodyweight_factor, exercises.load_factor,
				       exercises.is_unilateral, exercises.archived)
				  IS DISTINCT FROM (EXCLUDED.name_en, EXCLUDED.name_de, EXCLUDED.kind, EXCLUDED.primary_muscles,
				       EXCLUDED.secondary_muscles, EXCLUDED.equipment, EXCLUDED.load_type, EXCLUDED.bodyweight_factor,
				       EXCLUDED.load_factor, EXCLUDED.is_unilateral, false)`,
				train.BuiltinExerciseID(e.Slug), e.Slug, e.NameEN, e.NameDE, e.Kind, e.PrimaryMuscles, nonNil(e.SecondaryMuscles),
				nonNil(e.Equipment), e.LoadType, e.BodyweightFactor, e.LoadFactor, e.IsUnilateral)
		}
		batch.Queue(`UPDATE exercises SET archived=true, updated_at=now() WHERE user_id IS NULL AND NOT archived AND NOT (slug = ANY($1))`, slugs)

		plans := make([]string, 0, len(b.plans))
		for _, sp := range b.plans {
			p := sp.asPlan()
			plans = append(plans, sp.Slug)
			prog, err := json.Marshal(p.Progression)
			if err != nil {
				return err
			}
			batch.Queue(`INSERT INTO plans (id, slug, name, goal, weeks, days_per_week, description, progression)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (slug) WHERE user_id IS NULL DO UPDATE SET name=EXCLUDED.name, goal=EXCLUDED.goal,
				  weeks=EXCLUDED.weeks, days_per_week=EXCLUDED.days_per_week, description=EXCLUDED.description,
				  progression=EXCLUDED.progression, archived=false, updated_at=now()
				WHERE (plans.name, plans.goal, plans.weeks, plans.days_per_week, plans.description, plans.progression, plans.archived)
				  IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.goal, EXCLUDED.weeks, EXCLUDED.days_per_week,
				       EXCLUDED.description, EXCLUDED.progression, false)`,
				p.ID, sp.Slug, p.Name, p.Goal, p.Weeks, p.DaysPerWeek, p.Description, prog)
			var dayIDs, exIDs []uuid.UUID
			for _, d := range p.Days {
				dayIDs = append(dayIDs, d.ID)
				batch.Queue(`INSERT INTO plan_days (id, plan_id, week, day_index, name) VALUES ($1,$2,$3,$4,$5)
					ON CONFLICT (id) DO UPDATE SET week=EXCLUDED.week, day_index=EXCLUDED.day_index, name=EXCLUDED.name, updated_at=now()
					WHERE (plan_days.week, plan_days.day_index, plan_days.name) IS DISTINCT FROM (EXCLUDED.week, EXCLUDED.day_index, EXCLUDED.name)`,
					d.ID, p.ID, d.Week, d.DayIndex, d.Name)
				for _, e := range d.Exercises {
					exIDs = append(exIDs, e.ID)
					batch.Queue(`INSERT INTO plan_exercises (id, plan_day_id, exercise_id, position, sets, reps_min, reps_max,
						target_kind, target_value, rest_sec, superset_group, notes)
						VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
						ON CONFLICT (id) DO UPDATE SET exercise_id=EXCLUDED.exercise_id, position=EXCLUDED.position, sets=EXCLUDED.sets,
						  reps_min=EXCLUDED.reps_min, reps_max=EXCLUDED.reps_max, target_kind=EXCLUDED.target_kind,
						  target_value=EXCLUDED.target_value, rest_sec=EXCLUDED.rest_sec, superset_group=EXCLUDED.superset_group,
						  notes=EXCLUDED.notes, updated_at=now()
						WHERE (plan_exercises.exercise_id, plan_exercises.position, plan_exercises.sets, plan_exercises.reps_min,
						       plan_exercises.reps_max, plan_exercises.target_kind, plan_exercises.target_value, plan_exercises.rest_sec,
						       plan_exercises.superset_group, plan_exercises.notes)
						  IS DISTINCT FROM (EXCLUDED.exercise_id, EXCLUDED.position, EXCLUDED.sets, EXCLUDED.reps_min, EXCLUDED.reps_max,
						       EXCLUDED.target_kind, EXCLUDED.target_value, EXCLUDED.rest_sec, EXCLUDED.superset_group, EXCLUDED.notes)`,
						e.ID, d.ID, e.ExerciseID, e.Position, e.Sets, e.RepsMin, e.RepsMax, e.TargetKind, e.TargetValue,
						e.RestSec, e.SupersetGroup, e.Notes)
				}
			}
			batch.Queue(`DELETE FROM plan_exercises WHERE plan_day_id IN (SELECT id FROM plan_days WHERE plan_id=$1) AND NOT (id = ANY($2))`, p.ID, exIDs)
			batch.Queue(`DELETE FROM plan_days WHERE plan_id=$1 AND NOT (id = ANY($2))`, p.ID, dayIDs)
		}
		batch.Queue(`UPDATE plans SET archived=true, updated_at=now() WHERE user_id IS NULL AND NOT archived AND NOT (slug = ANY($1))`, plans)

		behaviors := make([]string, 0, len(b.behaviors))
		for _, s := range b.behaviors {
			behaviors = append(behaviors, s.Slug)
			batch.Queue(`INSERT INTO behaviors (id, slug, name_en, name_de, category, kind, default_on, sort)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (slug) WHERE user_id IS NULL DO UPDATE SET name_en=EXCLUDED.name_en, name_de=EXCLUDED.name_de,
				  category=EXCLUDED.category, kind=EXCLUDED.kind, default_on=EXCLUDED.default_on, sort=EXCLUDED.sort,
				  archived=false, updated_at=now()
				WHERE (behaviors.name_en, behaviors.name_de, behaviors.category, behaviors.kind, behaviors.default_on,
				       behaviors.sort, behaviors.archived)
				  IS DISTINCT FROM (EXCLUDED.name_en, EXCLUDED.name_de, EXCLUDED.category, EXCLUDED.kind,
				       EXCLUDED.default_on, EXCLUDED.sort, false)`,
				behavior.BuiltinID(s.Slug), s.Slug, s.NameEN, s.NameDE, s.Category, s.Kind, s.DefaultOn, s.Sort)
		}
		// A built-in that left the seeds is archived: past journal entries keep it.
		batch.Queue(`UPDATE behaviors SET archived=true, updated_at=now() WHERE user_id IS NULL AND NOT archived AND NOT (slug = ANY($1))`, behaviors)

		res := tx.SendBatch(ctx, batch)
		for k := 0; k < batch.Len(); k++ {
			tag, err := res.Exec()
			if err != nil {
				_ = res.Close()
				return fmt.Errorf("seed statement %d: %w", k, err)
			}
			changed += tag.RowsAffected()
		}
		return res.Close()
	})
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "built-ins seeded", "exercises", len(b.exercises), "plans", len(b.plans), "behaviors", len(b.behaviors),
		"rows_changed", changed)
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Exercises are the built-in exercises as the store returns them (fixed ids,
// load factors), for tests that pin calibration on the real library.
func (b Builtins) Exercises() []model.Exercise {
	out := make([]model.Exercise, 0, len(b.exercises))
	for _, e := range b.exercises {
		out = append(out, model.Exercise{ID: train.BuiltinExerciseID(e.Slug), Slug: e.Slug, Name: e.NameEN, NameEN: e.NameEN,
			NameDE: e.NameDE, Kind: e.Kind, PrimaryMuscles: e.PrimaryMuscles, SecondaryMuscles: nonNil(e.SecondaryMuscles),
			Equipment: nonNil(e.Equipment), LoadType: e.LoadType, BodyweightFactor: e.BodyweightFactor,
			LoadFactor: e.LoadFactor, IsUnilateral: e.IsUnilateral})
	}
	return out
}
