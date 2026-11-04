package model

import (
	"time"

	"github.com/google/uuid"
)

// ---- Train (M3b): exercises, plans, sessions -----------------------------------

// Exercise kinds and load types; closed in v1.
const (
	KindBarbell    = "barbell"
	KindDumbbell   = "dumbbell"
	KindKettlebell = "kettlebell"
	KindBodyweight = "bodyweight"
	KindMachine    = "machine"
	KindCable      = "cable"
	KindCardio     = "cardio"

	LoadWeight     = "weight"     // reps × kg
	LoadBodyweight = "bodyweight" // reps; weight_kg is added load
	LoadTime       = "time"       // duration_sec
	LoadDistance   = "distance"   // distance_m
)

// KnownExerciseKinds is the set of Exercise.Kind values internal/train validates (and the
// api's list filter checks).
var KnownExerciseKinds = map[string]bool{KindBarbell: true, KindDumbbell: true, KindKettlebell: true,
	KindBodyweight: true, KindMachine: true, KindCable: true, KindCardio: true}

// KnownLoadTypes is the set of Exercise.LoadType values internal/train validates.
var KnownLoadTypes = map[string]bool{LoadWeight: true, LoadBodyweight: true, LoadTime: true, LoadDistance: true}

// Muscles is the closed muscle vocabulary of primary/secondary_muscles.
var Muscles = []string{"chest", "lats", "upper_back", "traps", "front_delts", "side_delts", "rear_delts",
	"biceps", "triceps", "forearms", "abs", "obliques", "lower_back", "glutes", "quads", "hamstrings",
	"adductors", "calves"}

// Session and enrolment states.
const (
	SessionInProgress = "in_progress"
	SessionCompleted  = "completed"
	SessionDiscarded  = "discarded"

	// Where a session came from (D125).
	OriginApp    = "app"
	OriginImport = "import"

	EnrollmentActive    = "active"
	EnrollmentPaused    = "paused"
	EnrollmentCompleted = "completed"
)

// KnownSessionStatuses is the set of Session.Status values internal/train validates (and the
// api's list filter checks).
var KnownSessionStatuses = map[string]bool{SessionInProgress: true, SessionCompleted: true, SessionDiscarded: true}

// Personal record kinds.
const (
	PRE1RM      = "e1rm"
	PRMaxWeight = "max_weight"
	PRMaxReps   = "max_reps"
	PRMaxVolume = "max_volume"
)

// SessionSource is the workouts.source of a workout created for a session that
// has no matching HealthKit workout.
const SessionSource = "ryft-session"

// Exercise is a catalogue entry, built in or the user's own (IsCustom). Name is NameEN or
// NameDE by Accept-Language; LoadFactor is scoring's and never goes on the wire.
type Exercise struct {
	ID               uuid.UUID `json:"id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"` // name_en or name_de, by Accept-Language
	NameEN           string    `json:"name_en"`
	NameDE           string    `json:"name_de"`
	Kind             string    `json:"kind"`
	PrimaryMuscles   []string  `json:"primary_muscles"`
	SecondaryMuscles []string  `json:"secondary_muscles"`
	Equipment        []string  `json:"equipment"`
	LoadType         string    `json:"load_type"`
	BodyweightFactor float64   `json:"bodyweight_factor"`
	IsUnilateral     bool      `json:"is_unilateral"`
	IsCustom         bool      `json:"is_custom"`
	Archived         bool      `json:"archived"`
	LoadFactor       float64   `json:"-"` // scoring only (SCORING.md → Strength)
}

// ExerciseInput is the body of POST /v1/exercises.
type ExerciseInput struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	NameDE           *string   `json:"name_de,omitempty"`
	Kind             string    `json:"kind"`
	PrimaryMuscles   []string  `json:"primary_muscles"`
	SecondaryMuscles []string  `json:"secondary_muscles,omitempty"`
	Equipment        []string  `json:"equipment,omitempty"`
	LoadType         string    `json:"load_type"`
	BodyweightFactor *float64  `json:"bodyweight_factor,omitempty"`
	IsUnilateral     *bool     `json:"is_unilateral,omitempty"`
}

// ExercisePatch is the body of PATCH /v1/exercises/{id}.
type ExercisePatch struct {
	Name             *string   `json:"name,omitempty"`
	NameDE           *string   `json:"name_de,omitempty"`
	Kind             *string   `json:"kind,omitempty"`
	PrimaryMuscles   *[]string `json:"primary_muscles,omitempty"`
	SecondaryMuscles *[]string `json:"secondary_muscles,omitempty"`
	Equipment        *[]string `json:"equipment,omitempty"`
	LoadType         *string   `json:"load_type,omitempty"`
	BodyweightFactor *float64  `json:"bodyweight_factor,omitempty"`
	IsUnilateral     *bool     `json:"is_unilateral,omitempty"`
	Archived         *bool     `json:"archived,omitempty"`
}

// PR is a personal record: the best value of one kind for one exercise.
type PR struct {
	ExerciseID uuid.UUID `json:"exercise_id"`
	Kind       string    `json:"kind"`
	Value      float64   `json:"value"`
	SetID      uuid.UUID `json:"set_id"`
	SessionID  uuid.UUID `json:"session_id"`
	AchievedAt time.Time `json:"achieved_at"`
}

// Set is one logged set (SessionSet in Swift).
type Set struct {
	ID          uuid.UUID  `json:"id"`
	SetIndex    int        `json:"set_index"`
	Reps        *int       `json:"reps"`
	WeightKG    *float64   `json:"weight_kg"`
	AssistKG    *float64   `json:"assist_kg"` // bodyweight sets: the support, 0-200; comes off the moved weight, no record (D134)
	RPE         *float64   `json:"rpe"`
	DurationSec *int       `json:"duration_sec"`
	DistanceM   *float64   `json:"distance_m"`
	IsWarmup    bool       `json:"is_warmup"`
	CompletedAt *time.Time `json:"completed_at"`
	UpdatedAt   time.Time  `json:"updated_at"` // the app's clock; decides conflicts
}

// SessionExercise is one exercise of a logged Session. PlanExerciseID is set when it was done
// against a plan day's prescription; Position is its order within the session.
type SessionExercise struct {
	ID             uuid.UUID  `json:"id"`
	ExerciseID     uuid.UUID  `json:"exercise_id"`
	PlanExerciseID *uuid.UUID `json:"plan_exercise_id"`
	Position       int        `json:"position"`
	Notes          string     `json:"notes"`
	Sets           []Set      `json:"sets"`
}

// Session is a logged strength session: the body of PUT /v1/sessions/{id} and
// the response of every session endpoint. MuscularTRIMP, Strain, VolumeKG,
// WorkoutID, PRs and UpdatedAt are read-only.
type Session struct {
	ID            uuid.UUID         `json:"id"`
	Title         string            `json:"title"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       *time.Time        `json:"ended_at"`
	Status        string            `json:"status"`
	PlanDayID     *uuid.UUID        `json:"plan_day_id"`
	WorkoutID     *uuid.UUID        `json:"workout_id"`
	Notes         string            `json:"notes"`
	BodyweightKG  *float64          `json:"bodyweight_kg"`
	MuscularTRIMP *float64          `json:"muscular_trimp"`
	Strain        *float64          `json:"strain"`
	VolumeKG      *float64          `json:"volume_kg"`
	DeviceID      *uuid.UUID        `json:"device_id"`
	Exercises     []SessionExercise `json:"exercises"`
	PRs           []PR              `json:"prs"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// Origin is "app" or "import"; ImportSource is set exactly for "import" (D125). Read-only:
	// the server owns both and PUT /v1/sessions/{id} ignores them.
	Origin       string  `json:"origin"`
	ImportSource *string `json:"import_source"`
	// ImportedAt is when the training import last wrote the session (nil for the app's): an
	// UpdatedAt after it means the app changed it since (D126). Server-only, never serialised.
	ImportedAt *time.Time `json:"-"`
}

// SessionSummary is one row of GET /v1/sessions and DayStrain.sessions.
type SessionSummary struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	Status        string     `json:"status"`
	PlanDayID     *uuid.UUID `json:"plan_day_id"`
	WorkoutID     *uuid.UUID `json:"workout_id"`
	DurationMin   *int       `json:"duration_min"`
	ExerciseCount int        `json:"exercise_count"`
	SetCount      int        `json:"set_count"` // completed working sets
	VolumeKG      *float64   `json:"volume_kg"`
	MuscularTRIMP *float64   `json:"muscular_trimp"`
	Strain        *float64   `json:"strain"`
	PRCount       int        `json:"pr_count"`
	Origin        string     `json:"origin"`
	ImportSource  *string    `json:"import_source"`
}

// SessionComplete is the body of POST /v1/sessions/{id}/complete.
type SessionComplete struct {
	BodyweightKG *float64   `json:"bodyweight_kg,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

// ExerciseHistory backs the exercise detail screen.
type ExerciseHistory struct {
	ExerciseID uuid.UUID                `json:"exercise_id"`
	PRs        []PR                     `json:"prs"`
	Sessions   []ExerciseHistorySession `json:"sessions"` // newest first
	E1RM       []TrendPoint             `json:"e1rm"`     // best per day, oldest first
}

// ExerciseHistorySession is one past session's sets of an exercise, newest first in
// ExerciseHistory, so the exercise screen shows progress without fetching whole sessions.
type ExerciseHistorySession struct {
	SessionID uuid.UUID `json:"session_id"`
	Date      string    `json:"date"` // local day
	StartedAt time.Time `json:"started_at"`
	Sets      []Set     `json:"sets"`
}
