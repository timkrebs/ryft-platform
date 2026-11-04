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

// KnownSampleTypes is the set of Sample.Type values ingest stores. Any other type is
// dropped and counted (ignored.unknown_type), so an app that reads a newer HealthKit type
// keeps syncing instead of failing.
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

// KnownSleepStages is the set of SleepSegment.Stage values ingest stores; others are
// dropped and counted like unknown sample types.
var KnownSleepStages = map[string]bool{
	SleepInBed: true, SleepAsleep: true, SleepCore: true, SleepDeep: true, SleepREM: true, SleepAwake: true,
}

// Sample is one HealthKit quantity reading in a POST /v1/sync upload. Value is in the unit
// noted at its Type's constant; ID is the handle a later deletion names (D144).
type Sample struct {
	Type   string    `json:"type"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Value  float64   `json:"value"`
	Unit   string    `json:"unit"`
	Source string    `json:"source"`
	// The HealthKit source's bundle identifier (input only, for User.sources).
	SourceBundleID *string `json:"source_bundle_id,omitempty"`
	// The HealthKit object's UUID (HKObject.uuid): the handle a deletion names. Optional.
	ID *uuid.UUID `json:"id,omitempty"`
}

// SampleSpillsToNextDay says whether a sample of this type also changes the
// score of the day after its start (the night's HRV, respiration and SpO2 belong
// to the morning).
func SampleSpillsToNextDay(typ string) bool {
	return typ == SampleHRVSDNN || typ == SampleRespiratoryRate || typ == SampleSpO2
}

// SleepSegment is one HealthKit sleep-analysis interval of one stage in a POST /v1/sync
// upload.
type SleepSegment struct {
	Start          time.Time  `json:"start"`
	End            time.Time  `json:"end"`
	Stage          string     `json:"stage"`
	Source         string     `json:"source"`
	SourceBundleID *string    `json:"source_bundle_id,omitempty"` // input only
	ID             *uuid.UUID `json:"id,omitempty"`               // HKObject.uuid, optional
}

// Source kinds (UserSource.kinds); closed in v1.
const (
	KindEnergy  = "energy"
	KindHR      = "hr"
	KindHRV     = "hrv"
	KindResp    = "resp"
	KindSleep   = "sleep"
	KindSpO2    = "spo2"
	KindSteps   = "steps"
	KindWorkout = "workout"
)

// SampleKinds maps a sample type to the kind a source is shown with. VO₂max
// counts as heart rate: Apple estimates it from heart rate on walks and runs.
var SampleKinds = map[string]string{
	SampleHeartRate: KindHR, SampleRestingHR: KindHR, SampleVO2Max: KindHR, SampleHRVSDNN: KindHRV,
	SampleRespiratoryRate: KindResp, SampleSpO2: KindSpO2, SampleActiveEnergy: KindEnergy, SampleSteps: KindSteps,
}

// UserSource is an app or device that sent data recently (User.sources).
type UserSource struct {
	Name     string    `json:"name"`
	BundleID *string   `json:"bundle_id"`
	Kinds    []string  `json:"kinds"` // sorted
	LastSeen time.Time `json:"last_seen"`
}

// Workout is a workout as uploaded by POST /v1/sync and as served by the api. The fields
// marked read-only are the server's and are ignored on upload.
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
	// Read-only: the scoring model of strain (ignored on upload).
	ModelVersion *string `json:"model_version,omitempty"`
	// Read-only: the sources of other recordings of this workout the server hid
	// (cross-source duplicates), distinct and sorted; omitted when none.
	AlsoRecordedBy []string `json:"also_recorded_by,omitempty"`
	// Read-only, only with GET /v1/workouts?include_hidden=true: the primary a hidden duplicate is hidden behind.
	DuplicateOf *uuid.UUID `json:"duplicate_of,omitempty"`
	// Input only, as on Sample.
	SourceBundleID *string `json:"source_bundle_id,omitempty"`
}

// SyncRequest is the batch the iOS app POSTs to ingest /v1/sync.
type SyncRequest struct {
	DeviceID   uuid.UUID       `json:"device_id"`
	DeviceName string          `json:"device_name,omitempty"`
	AppVersion *string         `json:"app_version,omitempty"` // e.g. "0.9.0 (41)", kept on the device row
	OSVersion  *string         `json:"os_version,omitempty"`  // e.g. "iOS 26.1"
	Samples    []Sample        `json:"samples"`
	Sleep      []SleepSegment  `json:"sleep"`
	Workouts   []Workout       `json:"workouts"`
	Deleted    []DeletedRecord `json:"deleted,omitempty"`
}

// MaxWorkouts is the most entries of SyncRequest.Workouts (422 above). The app pages 20 000
// records of any kind (a page of only workouts would be an extreme backfill, a real account
// has a few hundred a year), so the bound is far above real use and below a fold's cost.
const MaxWorkouts = 5000

// MaxDeleted is the most entries of SyncRequest.Deleted (422 above).
const MaxDeleted = 2000

// Kinds of a DeletedRecord. Others are skipped, not refused: a later app keeps syncing.
const (
	DeletedSample  = "sample"
	DeletedSleep   = "sleep"
	DeletedWorkout = "workout"
)

// DeletedRecord is a HealthKit object that no longer exists.
type DeletedRecord struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
}

// SyncDeleted counts the entries of SyncRequest.Deleted that removed a row.
type SyncDeleted struct {
	Samples  int `json:"samples"`
	Sleep    int `json:"sleep"`
	Workouts int `json:"workouts"`
}

// SyncResponse answers POST /v1/sync. Samples, Sleep, Workouts and Days are the original
// fields and stay for older apps (the contract is additive-only); Accepted, Ignored and
// Scoring say what was kept, what was not and why, and when scores will be there (D91).
type SyncResponse struct {
	Samples  int      `json:"samples"`
	Sleep    int      `json:"sleep"`
	Workouts int      `json:"workouts"`
	Days     []string `json:"days_touched"` // YYYY-MM-DD, local to the user's tz
	// What was stored, what was not and why, and when the scores will be there (D91).
	Accepted SyncCounts  `json:"accepted"`
	Ignored  SyncIgnored `json:"ignored"`
	Scoring  SyncScoring `json:"scoring"`
	Deleted  SyncDeleted `json:"deleted"`
}

// SyncCounts are records per kind.
type SyncCounts struct {
	Samples  int `json:"samples"`
	Sleep    int `json:"sleep"`
	Workouts int `json:"workouts"`
}

// IgnoredCounts are records not stored, by reason.
type IgnoredCounts struct {
	Duplicate     int `json:"duplicate"`      // identical to one stored
	OutsideWindow int `json:"outside_window"` // > 24 h in the future, or ended > 400 days ago
	UnknownType   int `json:"unknown_type"`   // a sample type or sleep stage the server does not know
	UnknownID     int `json:"unknown_id"`     // a deletion that names nothing deletable for this user
}

// SyncIgnored is IgnoredCounts per kind.
type SyncIgnored struct {
	Samples  IgnoredCounts `json:"samples"`
	Sleep    IgnoredCounts `json:"sleep"`
	Workouts IgnoredCounts `json:"workouts"`
}

// SyncScoring is what scoring will do with a sync.
type SyncScoring struct {
	QueuedDays        []string `json:"queued_days"`
	ExpectedWithinSec int      `json:"expected_within_sec"`
}

// Scoring states (DailyScore.scoring_state, SyncStatus.today.state); closed in v1.
const (
	ScoringScored          = "scored"
	ScoringPending         = "pending"
	ScoringWaitingForSleep = "waiting_for_sleep"
	ScoringStale           = "stale"
)

// TodayState is SyncStatus.today.
type TodayState struct {
	Day          string     `json:"day"`
	State        string     `json:"state"`
	ScoredAt     *time.Time `json:"scored_at"`
	ModelVersion string     `json:"model_version"`
}

// SyncStatus is GET /v1/me/sync.
type SyncStatus struct {
	Gate              *string              `json:"gate"`
	LastSyncAt        *time.Time           `json:"last_sync_at"`
	LastSyncDevice    *string              `json:"last_sync_device"`
	Latest            map[string]time.Time `json:"latest"`
	Today             TodayState           `json:"today"`
	PendingDays       []string             `json:"pending_days"`
	ExpectedWithinSec int                  `json:"expected_within_sec"`
	ResyncRequestedAt *time.Time           `json:"resync_requested_at"`
}

// RecomputeRequest is POST /v1/recompute: the range of local days (YYYY-MM-DD) to score
// again. The api bounds its length and allows one request per user per minute.
type RecomputeRequest struct {
	From string `json:"from"` // YYYY-MM-DD
	To   string `json:"to"`
}
