// Package model holds the wire types shared by the ingest and api services and
// mirrored 1:1 by the iOS app (see ../app/Ryft/Networking/Models.swift).
package model

import (
	"time"

	"github.com/google/uuid"
)

// Sample types. Keep in sync with HealthKitManager.swift.
const (
	SampleHeartRate       = "heart_rate"       // bpm
	SampleHRVSDNN         = "hrv_sdnn"         // ms
	SampleRestingHR       = "resting_hr"       // bpm, one per day from HealthKit
	SampleRespiratoryRate = "respiratory_rate" // breaths/min
	SampleSpO2            = "spo2"             // percent 0..100
	SampleVO2Max          = "vo2max"           // ml/kg/min
	SampleActiveEnergy    = "active_energy"    // kcal
	SampleSteps           = "steps"            // count
)

var KnownSampleTypes = map[string]bool{
	SampleHeartRate: true, SampleHRVSDNN: true, SampleRestingHR: true,
	SampleRespiratoryRate: true, SampleSpO2: true, SampleVO2Max: true,
	SampleActiveEnergy: true, SampleSteps: true,
}

// Sleep stages, mirroring HKCategoryValueSleepAnalysis.
const (
	SleepInBed  = "inbed"
	SleepAsleep = "asleep" // unspecified stage (older devices / Garmin without stages)
	SleepCore   = "core"
	SleepDeep   = "deep"
	SleepREM    = "rem"
	SleepAwake  = "awake"
)

var KnownSleepStages = map[string]bool{
	SleepInBed: true, SleepAsleep: true, SleepCore: true, SleepDeep: true, SleepREM: true, SleepAwake: true,
}

// Readiness states, shown under the greeting on the Today screen.
const (
	StateBalanced    = "balanced"    // readiness ≥ 67
	StateStrained    = "strained"    // 34..66
	StateDepleted    = "depleted"    // < 34
	StateCalibrating = "calibrating" // baselines not established yet, or no score
)

type Sample struct {
	Type   string    `json:"type"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Value  float64   `json:"value"`
	Unit   string    `json:"unit"`
	Source string    `json:"source"`
}

type SleepSegment struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Stage  string    `json:"stage"`
	Source string    `json:"source"`
}

type Workout struct {
	ID         uuid.UUID `json:"id"`
	Activity   string    `json:"activity"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	AvgHR      *float64  `json:"avg_hr,omitempty"`
	MaxHR      *float64  `json:"max_hr,omitempty"`
	EnergyKcal *float64  `json:"energy_kcal,omitempty"`
	DistanceM  *float64  `json:"distance_m,omitempty"`
	Source     string    `json:"source"`
	Strain     *float64  `json:"strain,omitempty"` // read-only, filled by scoring
	// Read-only, filled by scoring: the muscular load of the strength session
	// attached to this workout, and that session.
	MuscularLoad *float64   `json:"muscular_load,omitempty"`
	SessionID    *uuid.UUID `json:"session_id,omitempty"`
}

// SyncRequest is the batch the iOS app POSTs to ingest /v1/sync.
type SyncRequest struct {
	DeviceID   uuid.UUID      `json:"device_id"`
	DeviceName string         `json:"device_name,omitempty"`
	Samples    []Sample       `json:"samples"`
	Sleep      []SleepSegment `json:"sleep"`
	Workouts   []Workout      `json:"workouts"`
}

type SyncResponse struct {
	Samples  int      `json:"samples"`
	Sleep    int      `json:"sleep"`
	Workouts int      `json:"workouts"`
	Days     []string `json:"days_touched"` // YYYY-MM-DD, local to the user's tz
}

// DailyScore is what the app renders on the Today / Trends screens.
type DailyScore struct {
	Day              string   `json:"day"`       // YYYY-MM-DD
	Readiness        *int     `json:"readiness"` // 1..100, the Today headline
	State            string   `json:"state"`     // State* constant
	Recovery         *int     `json:"recovery"`
	Strain           *float64 `json:"strain"`
	Steps            *int     `json:"steps"`        // day total, null without step samples
	ActiveKcal       *int     `json:"active_kcal"`  // day total of active energy, null without samples
	StrengthMin      *int     `json:"strength_min"` // minutes of completed strength sessions, null without one
	VolumeKG         *float64 `json:"volume_kg"`    // their Σ reps × kg, null without a session
	SleepPerformance *int     `json:"sleep_performance"`
	SleepNeedMin     *int     `json:"sleep_need_min"`
	SleepActualMin   *int     `json:"sleep_actual_min"`
	SleepDebtMin     *int     `json:"sleep_debt_min"`
	// The need term by term (M4): need = base + strain + debt − nap credit.
	SleepNeedBaseMin   *int           `json:"sleep_need_base_min"`
	SleepNeedStrainMin *int           `json:"sleep_need_strain_min"`
	SleepNeedDebtMin   *int           `json:"sleep_need_debt_min"`
	SleepNeedNapMin    *int           `json:"sleep_need_nap_min"` // yesterday's naps, credited to last night
	NapMin             *int           `json:"nap_min"`            // naps that day (credited to the next night)
	SleepQuality       *int           `json:"sleep_quality"`
	SleepConsistency   *int           `json:"sleep_consistency"`
	HRVms              *float64       `json:"hrv_ms"`
	RHR                *float64       `json:"rhr"`
	RespRate           *float64       `json:"resp_rate"`
	SpO2               *float64       `json:"spo2"`
	HRVBaselineMS      *float64       `json:"hrv_baseline_ms"` // 30-day baseline hrv_ms is scored against
	RHRBaseline        *float64       `json:"rhr_baseline"`    // 30-day baseline rhr is scored against
	BaselineDays       int            `json:"baseline_days"`   // nights of history behind the baselines, 0..30
	Calibrating        bool           `json:"calibrating"`     // baseline_days < 7
	Details            map[string]any `json:"details"`
	ComputedAt         time.Time      `json:"computed_at"`
}

type DayDetail struct {
	Score    DailyScore   `json:"score"`
	Sleep    *SleepDetail `json:"sleep"`  // null when no sleep was recorded for the night
	Strain   *DayStrain   `json:"strain"` // null when there was no heart-rate data
	Workouts []Workout    `json:"workouts"`
}

// DayStrain is the load behind DailyScore.strain: heart rate plus the muscular
// load of the day's strength sessions.
type DayStrain struct {
	TRIMP         int              `json:"trimp"`          // Banister training impulse
	HRMin         int              `json:"hr_min"`         // minutes of usable heart-rate data
	ZoneMin       []int            `json:"zone_min"`       // minutes in HRR zones 50-60, 60-70, 70-80, 80-90, 90+ %
	Steps         *int             `json:"steps"`          // = DailyScore.steps
	ActiveKcal    *int             `json:"active_kcal"`    // = DailyScore.active_kcal
	MuscularTRIMP int              `json:"muscular_trimp"` // strength sessions; strain = f(trimp + muscular_trimp)
	Sessions      []SessionSummary `json:"sessions"`       // the day's completed sessions, oldest first
}

// HRSample is one point of a workout's heart-rate curve.
type HRSample struct {
	T   time.Time `json:"t"`
	BPM int       `json:"bpm"`
}

// WorkoutDetail backs the workout screen: the workout plus its heart-rate
// curve (≤ 600 points) and load. ZoneMin and TRIMP are null until scored.
type WorkoutDetail struct {
	Workout
	HR      []HRSample `json:"hr"`
	ZoneMin []int      `json:"zone_min"`
	TRIMP   *int       `json:"trimp"`
}

// API sleep stages. HealthKit "core" is shown as "light" in the app.
const (
	StageDeep   = "deep"
	StageLight  = "light"
	StageREM    = "rem"
	StageAwake  = "awake"
	StageAsleep = "asleep" // hypnogram only: asleep, stage unknown
)

// Sleep quality labels (SleepSummary.QualityLabel).
const (
	QualityGood = "good" // quality ≥ 80
	QualityFair = "fair" // 60..79
	QualityPoor = "poor" // < 60
)

// SleepStage is one row of the stage breakdown. Deep/light/rem pct are shares
// of the staged sleep and sum to 100; awake pct is its share of time in bed.
type SleepStage struct {
	Stage string `json:"stage"`
	Min   int    `json:"min"`
	Pct   int    `json:"pct"`
}

// SleepSummary is one night as the Sleep screen shows it.
type SleepSummary struct {
	Start        time.Time    `json:"start"`
	End          time.Time    `json:"end"`
	TotalMin     int          `json:"total_min"`  // asleep
	InBedMin     int          `json:"in_bed_min"` // asleep + awake + time to fall asleep
	Efficiency   int          `json:"efficiency"` // total / in bed, 0..100
	Stages       []SleepStage `json:"stages"`     // always deep, light, rem, awake in that order
	Quality      int          `json:"quality"`    // 0..100: 0.6·performance + 0.2·efficiency + 0.2·restorative
	QualityLabel string       `json:"quality_label"`
	Restorative  int          `json:"restorative"` // 0..100, deep + REM against 40 % of sleep
	HasStages    bool         `json:"has_stages"`  // false: the source recorded no stages, deep/light/rem are 0
}

// SleepInterval is one bar of the hypnogram.
type SleepInterval struct {
	Stage string    `json:"stage"` // deep | light | rem | awake | asleep
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// SleepNeed is a night's need term by term (DayDetail.sleep.need).
type SleepNeed struct {
	Base      int `json:"base"`
	Strain    int `json:"strain"`
	Debt      int `json:"debt"`
	NapCredit int `json:"nap_credit"`
	Total     int `json:"total"`
}

type SleepDetail struct {
	SleepSummary
	Need     SleepNeed       `json:"need"`
	Segments []SleepInterval `json:"segments"` // stage timeline, oldest first
}

// SleepDay is one night in a SleepRange.
type SleepDay struct {
	Day string `json:"day"`
	SleepSummary
	Consistency *int `json:"consistency"` // = DailyScore.sleep_consistency
	NapMin      *int `json:"nap_min"`     // = DailyScore.nap_min
}

// SleepBedtime is one night's bed and wake time, for the bedtime strip.
type SleepBedtime struct {
	Day  string    `json:"day"`
	Bed  time.Time `json:"bed"`
	Wake time.Time `json:"wake"`
}

// SleepPlanBasis says where tonight's need comes from.
type SleepPlanBasis struct {
	Base        int `json:"base"`
	StrainTopup int `json:"strain_topup"`
	DebtTopup   int `json:"debt_topup"`
	NapCredit   int `json:"nap_credit"`
	Efficiency  int `json:"efficiency"`
}

// SleepPlan is GET /v1/sleep/plan: bedtimes for a wake time.
type SleepPlan struct {
	Wake       time.Time      `json:"wake"`
	NeedMin    int            `json:"need_min"`
	InBedMin   int            `json:"in_bed_min"`
	Efficiency int            `json:"efficiency"`
	Bedtime100 time.Time      `json:"bedtime_100"`
	Bedtime85  time.Time      `json:"bedtime_85"`
	Bedtime70  time.Time      `json:"bedtime_70"`
	Basis      SleepPlanBasis `json:"basis"`
}

// SleepRange backs the Sleep screen's Week / Month tabs.
type SleepRange struct {
	Range          string         `json:"range"` // week | month
	From           string         `json:"from"`
	To             string         `json:"to"`
	Days           []SleepDay     `json:"days"` // nights with data only, oldest first
	Nights         int            `json:"nights"`
	AvgTotalMin    *int           `json:"avg_total_min"`
	AvgInBedMin    *int           `json:"avg_in_bed_min"`
	AvgEfficiency  *int           `json:"avg_efficiency"`
	AvgQuality     *int           `json:"avg_quality"`
	AvgStages      []SleepStage   `json:"avg_stages"` // over nights with stages; empty when none
	AvgConsistency *int           `json:"avg_consistency"`
	Bedtimes       []SleepBedtime `json:"bedtimes"`
}

// Trend metrics (GET /v1/trends?metric=).
const (
	MetricReadiness   = "readiness"    // DailyScore.readiness
	MetricRecovery    = "recovery"     // DailyScore.recovery
	MetricStrain      = "strain"       // DailyScore.strain
	MetricSleep       = "sleep"        // DailyScore.sleep_performance (%)
	MetricSleepMin    = "sleep_min"    // DailyScore.sleep_actual_min (minutes asleep)
	MetricHRV         = "hrv"          // DailyScore.hrv_ms
	MetricRHR         = "rhr"          // DailyScore.rhr
	MetricSteps       = "steps"        // DailyScore.steps
	MetricActiveKcal  = "active_kcal"  // DailyScore.active_kcal
	MetricStrengthMin = "strength_min" // DailyScore.strength_min
	MetricVolumeKG    = "volume_kg"    // DailyScore.volume_kg
	MetricSleepQual   = "sleep_quality"
	MetricSleepCons   = "sleep_consistency"
	MetricNapMin      = "nap_min"
)

var KnownTrendMetrics = map[string]bool{
	MetricReadiness: true, MetricRecovery: true, MetricStrain: true, MetricSleep: true,
	MetricSleepMin: true, MetricHRV: true, MetricRHR: true, MetricSteps: true, MetricActiveKcal: true,
	MetricStrengthMin: true, MetricVolumeKG: true, MetricSleepQual: true, MetricSleepCons: true, MetricNapMin: true,
}

// Sleep baseline sources (User.sleep_base_source); closed in v1.
const (
	BaseDefault = "default"
	BaseLearned = "learned"
	BaseManual  = "manual"
)

type TrendPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

// Trend is one metric over a 7/30/90-day window for the Trends charts.
type Trend struct {
	Metric   string       `json:"metric"`
	Range    string       `json:"range"` // 7d | 30d | 90d
	From     string       `json:"from"`
	To       string       `json:"to"`
	Points   []TrendPoint `json:"points"` // days with a value only, oldest first
	Avg      *float64     `json:"avg"`
	Min      *float64     `json:"min"`
	Max      *float64     `json:"max"`
	Baseline *float64     `json:"baseline"` // hrv / rhr only: 30-day baseline as of the last scored day
}

// Insight severities, most urgent first.
const (
	SeverityWarn   = "warn"
	SeverityNotice = "notice"
	SeverityInfo   = "info"
)

// Insight is one card on the Insights screen.
type Insight struct {
	ID       string `json:"id"`       // stable per rule, e.g. "hrv-low-3d"
	Severity string `json:"severity"` // info | notice | warn
	Title    string `json:"title"`
	Body     string `json:"body"`
	Metric   string `json:"metric"` // trend metric to deep-link to
	Day      string `json:"day"`    // the day the insight is about
}

type User struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`     // display name; from the sign-in (Apple full name), may be empty
	Email        *string   `json:"email"`    // from the token; null when unknown
	Provider     *string   `json:"provider"` // sign-in provider, e.g. "apple"; null when unknown
	TZ           string    `json:"tz"`
	MaxHR        int       `json:"max_hr"`
	SleepBaseMin int       `json:"sleep_base_min"`
	// Where sleep_base_min comes from (default|learned|manual) and the latest
	// learned estimate, also while manual.
	SleepBaseSource     string     `json:"sleep_base_source"`
	SleepBaseLearnedMin *int       `json:"sleep_base_learned_min"`
	SleepBaseLearnedAt  *time.Time `json:"sleep_base_learned_at"`
}

type UserPatch struct {
	TZ           *string `json:"tz,omitempty"`
	MaxHR        *int    `json:"max_hr,omitempty"`
	SleepBaseMin *int    `json:"sleep_base_min,omitempty"` // sets the source to manual
	// false: back to the learned estimate (or the default); true: keep as manual.
	SleepBaseManual *bool `json:"sleep_base_manual,omitempty"`
}

type RecomputeRequest struct {
	From string `json:"from"` // YYYY-MM-DD
	To   string `json:"to"`
}

// IngestedEvent is published on NATS after a successful sync.
type IngestedEvent struct {
	UserID uuid.UUID `json:"user_id"` // the event is always scoped to one user
	Days   []string  `json:"days"`    // local days whose scores need (re)computing
	Reason string    `json:"reason"`
	// RequestID of the HTTP request that caused the event, so scoring's logs
	// can be correlated with it.
	RequestID string `json:"request_id,omitempty"`
}

// SessionCompletedEvent is published on NATS (ryft.session.completed) when a
// strength session is completed, so scoring rescores its day.
type SessionCompletedEvent struct {
	UserID    uuid.UUID `json:"user_id"`
	SessionID uuid.UUID `json:"session_id"`
	Day       string    `json:"day"` // local day of the session's start
	RequestID string    `json:"request_id,omitempty"`
}

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

var KnownExerciseKinds = map[string]bool{KindBarbell: true, KindDumbbell: true, KindKettlebell: true,
	KindBodyweight: true, KindMachine: true, KindCable: true, KindCardio: true}

var KnownLoadTypes = map[string]bool{LoadWeight: true, LoadBodyweight: true, LoadTime: true, LoadDistance: true}

// Muscles is the closed muscle vocabulary of primary/secondary_muscles.
var Muscles = []string{"chest", "lats", "upper_back", "traps", "front_delts", "side_delts", "rear_delts",
	"biceps", "triceps", "forearms", "abs", "obliques", "lower_back", "glutes", "quads", "hamstrings",
	"adductors", "calves"}

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

var KnownTargetKinds = map[string]bool{TargetPct1RM: true, TargetRPE: true, TargetKG: true, TargetBodyweight: true}

var KnownProgressions = map[string]bool{ProgressionLinear: true, ProgressionPct: true, ProgressionNone: true}

// Session and enrolment states.
const (
	SessionInProgress = "in_progress"
	SessionCompleted  = "completed"
	SessionDiscarded  = "discarded"

	EnrollmentActive    = "active"
	EnrollmentPaused    = "paused"
	EnrollmentCompleted = "completed"
)

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
	RPE         *float64   `json:"rpe"`
	DurationSec *int       `json:"duration_sec"`
	DistanceM   *float64   `json:"distance_m"`
	IsWarmup    bool       `json:"is_warmup"`
	CompletedAt *time.Time `json:"completed_at"`
	UpdatedAt   time.Time  `json:"updated_at"` // the app's clock; decides conflicts
}

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

type ExerciseHistorySession struct {
	SessionID uuid.UUID `json:"session_id"`
	Date      string    `json:"date"` // local day
	StartedAt time.Time `json:"started_at"`
	Sets      []Set     `json:"sets"`
}

// Progression is a plan's rule for moving kg targets.
type Progression struct {
	Type        string   `json:"type"`
	IncrementKG *float64 `json:"increment_kg,omitempty"`
	OnFail      *string  `json:"on_fail,omitempty"`
	DeloadAfter *int     `json:"deload_after,omitempty"`
}

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
}

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

type Enrollment struct {
	ID              uuid.UUID `json:"id"`
	PlanID          uuid.UUID `json:"plan_id"`
	StartedAt       time.Time `json:"started_at"`
	CurrentWeek     int       `json:"current_week"`      // 1-based
	CurrentDayIndex int       `json:"current_day_index"` // 0-based
	Status          string    `json:"status"`
}

type PlansOverview struct {
	Builtin []PlanSummary `json:"builtin"`
	Mine    []PlanSummary `json:"mine"`
	Active  *Enrollment   `json:"active"`
}

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

type PlanToday struct {
	Enrollment Enrollment   `json:"enrollment"`
	Day        PlanDay      `json:"day"`
	Targets    []PlanTarget `json:"targets"`
}
