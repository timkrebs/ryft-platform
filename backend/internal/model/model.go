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
	HRVBaselineMS    *float64       `json:"hrv_baseline_ms"` // 30-day baseline hrv_ms is scored against
	RHRBaseline      *float64       `json:"rhr_baseline"`    // 30-day baseline rhr is scored against
	Calibrating      bool           `json:"calibrating"`
	Details          map[string]any `json:"details"`
	ComputedAt       time.Time      `json:"computed_at"`
}

type DayDetail struct {
	Score    DailyScore   `json:"score"`
	Sleep    *SleepDetail `json:"sleep"`  // null when no sleep was recorded for the night
	Strain   *DayStrain   `json:"strain"` // null when there was no heart-rate data
	Workouts []Workout    `json:"workouts"`
}

// DayStrain is the heart-rate load behind DailyScore.strain.
type DayStrain struct {
	TRIMP   int   `json:"trimp"`    // Banister training impulse
	HRMin   int   `json:"hr_min"`   // minutes of usable heart-rate data
	ZoneMin []int `json:"zone_min"` // minutes in HRR zones 50-60, 60-70, 70-80, 80-90, 90+ %
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
	Quality      int          `json:"quality"`    // 0..100, = sleep performance for now
	QualityLabel string       `json:"quality_label"`
	HasStages    bool         `json:"has_stages"` // false: the source recorded no stages, deep/light/rem are 0
}

// SleepInterval is one bar of the hypnogram.
type SleepInterval struct {
	Stage string    `json:"stage"` // deep | light | rem | awake | asleep
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type SleepDetail struct {
	SleepSummary
	Segments []SleepInterval `json:"segments"` // stage timeline, oldest first
}

// SleepDay is one night in a SleepRange.
type SleepDay struct {
	Day string `json:"day"`
	SleepSummary
}

// SleepRange backs the Sleep screen's Week / Month tabs.
type SleepRange struct {
	Range         string       `json:"range"` // week | month
	From          string       `json:"from"`
	To            string       `json:"to"`
	Days          []SleepDay   `json:"days"` // nights with data only, oldest first
	Nights        int          `json:"nights"`
	AvgTotalMin   *int         `json:"avg_total_min"`
	AvgInBedMin   *int         `json:"avg_in_bed_min"`
	AvgEfficiency *int         `json:"avg_efficiency"`
	AvgQuality    *int         `json:"avg_quality"`
	AvgStages     []SleepStage `json:"avg_stages"` // over nights with stages; empty when none
}

// Trend metrics (GET /v1/trends?metric=).
const (
	MetricReadiness = "readiness" // DailyScore.readiness
	MetricRecovery  = "recovery"  // DailyScore.recovery
	MetricStrain    = "strain"    // DailyScore.strain
	MetricSleep     = "sleep"     // DailyScore.sleep_performance (%)
	MetricSleepMin  = "sleep_min" // DailyScore.sleep_actual_min (minutes asleep)
	MetricHRV       = "hrv"       // DailyScore.hrv_ms
	MetricRHR       = "rhr"       // DailyScore.rhr
)

var KnownTrendMetrics = map[string]bool{
	MetricReadiness: true, MetricRecovery: true, MetricStrain: true, MetricSleep: true,
	MetricSleepMin: true, MetricHRV: true, MetricRHR: true,
}

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
