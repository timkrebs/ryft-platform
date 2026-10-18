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
	Day              string         `json:"day"`       // YYYY-MM-DD
	Readiness        *int           `json:"readiness"` // 1..100, the Today headline
	State            string         `json:"state"`     // State* constant
	Recovery         *int           `json:"recovery"`
	Strain           *float64       `json:"strain"`
	SleepPerformance *int           `json:"sleep_performance"`
	SleepNeedMin     *int           `json:"sleep_need_min"`
	SleepActualMin   *int           `json:"sleep_actual_min"`
	SleepDebtMin     *int           `json:"sleep_debt_min"`
	HRVms            *float64       `json:"hrv_ms"`
	RHR              *float64       `json:"rhr"`
	RespRate         *float64       `json:"resp_rate"`
	SpO2             *float64       `json:"spo2"`
	Calibrating      bool           `json:"calibrating"`
	Details          map[string]any `json:"details"`
	ComputedAt       time.Time      `json:"computed_at"`
}

type DayDetail struct {
	Score    DailyScore `json:"score"`
	Workouts []Workout  `json:"workouts"`
}

type User struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	TZ           string    `json:"tz"`
	MaxHR        int       `json:"max_hr"`
	SleepBaseMin int       `json:"sleep_base_min"`
}

type UserPatch struct {
	TZ           *string `json:"tz,omitempty"`
	MaxHR        *int    `json:"max_hr,omitempty"`
	SleepBaseMin *int    `json:"sleep_base_min,omitempty"`
}

type RecomputeRequest struct {
	From string `json:"from"` // YYYY-MM-DD
	To   string `json:"to"`
}

// IngestedEvent is published on NATS after a successful sync.
type IngestedEvent struct {
	UserID uuid.UUID `json:"user_id"`
	Days   []string  `json:"days"` // local days whose scores need (re)computing
	Reason string    `json:"reason"`
}
