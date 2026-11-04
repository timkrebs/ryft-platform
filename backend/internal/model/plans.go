package model

import (
	"time"

	"github.com/google/uuid"
)

// KnownPlanGoals is the set of Plan.Goal values internal/train validates.
var KnownPlanGoals = map[string]bool{"strength": true, "hypertrophy": true, "weightlifting": true, "general": true}

// Plan target kinds.
const (
	TargetPct1RM      = "pct_1rm"
	TargetRPE         = "rpe"
	TargetKG          = "kg"
	TargetBodyweight  = "bodyweight"
	ProgressionLinear = "linear"
	ProgressionPct    = "percent"
	ProgressionNone   = "none"
)

// KnownTargetKinds lists the PlanExercise.TargetKind values. Production validation is the
// switch in internal/train.checkTarget, which also checks each kind's range; this set is not
// consulted there.
var KnownTargetKinds = map[string]bool{TargetPct1RM: true, TargetRPE: true, TargetKG: true, TargetBodyweight: true}

// KnownProgressions is the set of Progression.Type values internal/train validates.
var KnownProgressions = map[string]bool{ProgressionLinear: true, ProgressionPct: true, ProgressionNone: true}

// Progression is a plan's rule for moving kg targets.
type Progression struct {
	Type        string   `json:"type"`
	IncrementKG *float64 `json:"increment_kg,omitempty"`
	OnFail      *string  `json:"on_fail,omitempty"`
	DeloadAfter *int     `json:"deload_after,omitempty"`
}

// PlanExercise is one prescribed exercise of a PlanDay: Sets of RepsMin to RepsMax against
// a target.
type PlanExercise struct {
	ID            uuid.UUID `json:"id"`
	ExerciseID    uuid.UUID `json:"exercise_id"`
	Exercise      *Exercise `json:"exercise"` // resolved on read, ignored on PUT
	Position      int       `json:"position"`
	Sets          int       `json:"sets"`
	RepsMin       int       `json:"reps_min"`
	RepsMax       int       `json:"reps_max"`
	TargetKind    string    `json:"target_kind"`
	TargetValue   *float64  `json:"target_value"`
	RestSec       int       `json:"rest_sec"`
	SupersetGroup *int      `json:"superset_group"`
	Notes         string    `json:"notes"`
}

// PlanDay is one training day of a Plan. A nil Week means the day repeats every week.
type PlanDay struct {
	ID        uuid.UUID      `json:"id"`
	Week      *int           `json:"week"` // nil = every week
	DayIndex  int            `json:"day_index"`
	Name      string         `json:"name"`
	Exercises []PlanExercise `json:"exercises"`
}

// Plan is a training plan; the body of PUT /v1/plans/{id} (Slug, IsBuiltin and
// PlanExercise.Exercise are ignored there).
type Plan struct {
	ID          uuid.UUID    `json:"id"`
	Slug        *string      `json:"slug"`
	Name        string       `json:"name"`
	Goal        string       `json:"goal"`
	Weeks       *int         `json:"weeks"` // nil repeats forever
	DaysPerWeek int          `json:"days_per_week"`
	Description string       `json:"description"`
	Progression *Progression `json:"progression"`
	IsBuiltin   bool         `json:"is_builtin"`
	Archived    bool         `json:"archived"`
	Days        []PlanDay    `json:"days"`
	// UpdatedAt and ImportedAt (nil unless the training import wrote it) say whether the app
	// changed an imported plan since (D126). Server-only, never serialised.
	UpdatedAt  time.Time  `json:"-"`
	ImportedAt *time.Time `json:"-"`
}

// PlanSummary is a Plan without its days, for the plans list.
type PlanSummary struct {
	ID            uuid.UUID `json:"id"`
	Slug          *string   `json:"slug"`
	Name          string    `json:"name"`
	Goal          string    `json:"goal"`
	Weeks         *int      `json:"weeks"`
	DaysPerWeek   int       `json:"days_per_week"`
	Description   string    `json:"description"`
	IsBuiltin     bool      `json:"is_builtin"`
	Archived      bool      `json:"archived"`
	DayCount      int       `json:"day_count"`
	ExerciseCount int       `json:"exercise_count"`
}

// Enrollment is where a user stands in a plan: CurrentWeek counts from 1,
// CurrentDayIndex from 0.
type Enrollment struct {
	ID              uuid.UUID `json:"id"`
	PlanID          uuid.UUID `json:"plan_id"`
	StartedAt       time.Time `json:"started_at"`
	CurrentWeek     int       `json:"current_week"`      // 1-based
	CurrentDayIndex int       `json:"current_day_index"` // 0-based
	Status          string    `json:"status"`
}

// EnrollPost is the optional body of POST /v1/plans/{id}/enroll (T8): where the
// enrolment starts or is moved to. A nil field takes its default.
type EnrollPost struct {
	StartedAt       *time.Time `json:"started_at"`
	CurrentWeek     *int       `json:"current_week"`
	CurrentDayIndex *int       `json:"current_day_index"`
}

// Set says whether any field is given (none: the enrolment as before T8).
func (e EnrollPost) Set() bool {
	return e.StartedAt != nil || e.CurrentWeek != nil || e.CurrentDayIndex != nil
}

// PlansOverview is GET /v1/plans: the built-in plans, the user's own and the active
// enrollment (null when there is none).
type PlansOverview struct {
	Builtin []PlanSummary `json:"builtin"`
	Mine    []PlanSummary `json:"mine"`
	Active  *Enrollment   `json:"active"`
}

// PlanTarget is today's prescription for one plan exercise, with the sets the user logged
// last time (LastTime).
type PlanTarget struct {
	PlanExerciseID uuid.UUID `json:"plan_exercise_id"`
	ExerciseID     uuid.UUID `json:"exercise_id"`
	Sets           int       `json:"sets"`
	RepsMin        int       `json:"reps_min"`
	RepsMax        int       `json:"reps_max"`
	TargetKind     string    `json:"target_kind"`
	TargetKG       *float64  `json:"target_kg"`
	RestSec        int       `json:"rest_sec"`
	LastTime       []Set     `json:"last_time"`
}

// PlanToday is GET /v1/plans/today: the enrollment's current day with a target per
// exercise.
type PlanToday struct {
	Enrollment Enrollment   `json:"enrollment"`
	Day        PlanDay      `json:"day"`
	Targets    []PlanTarget `json:"targets"`
}
