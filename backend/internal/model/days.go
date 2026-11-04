package model

import (
	"time"
)

// Readiness states, shown under the greeting on the Today screen.
const (
	StateBalanced    = "balanced"    // readiness ≥ 67
	StateStrained    = "strained"    // 34..66
	StateDepleted    = "depleted"    // < 34
	StateCalibrating = "calibrating" // baselines not established yet, or no score
)

// Why a day is calibrating: DailyScore.calibrating_reason, a closed enum
// (D113; the rule is scoring.CalibratingReason).
const (
	CalibratingShortHistory = "short_history" // under 7 nights with both signals yet; waiting ends it
	CalibratingNoHRV        = "no_hrv"        // 7+ days with data in the window, no overnight HRV there or that night
	CalibratingNoRHR        = "no_rhr"        // 7+ days with data in the window, no resting HR there or that day
)

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
	HRVBaselineMS      *float64       `json:"hrv_baseline_ms"`    // 30-day baseline hrv_ms is scored against
	RHRBaseline        *float64       `json:"rhr_baseline"`       // 30-day baseline rhr is scored against
	BaselineDays       int            `json:"baseline_days"`      // nights behind the baselines, 0..30; typed, details' copy stays frozen (D20)
	BaselineHRVDays    *int           `json:"baseline_hrv_days"`  // of the 30 scored days before, those with an HRV; nil: not known
	BaselineRHRDays    *int           `json:"baseline_rhr_days"`  // … with a resting HR; baseline_days = min of the two
	Calibrating        bool           `json:"calibrating"`        // baseline_days < 7, exactly (D20)
	CalibratingReason  *string        `json:"calibrating_reason"` // Calibrating* when calibrating; nil otherwise or not known
	Details            map[string]any `json:"details"`
	ComputedAt         time.Time      `json:"computed_at"`
	// The scoring model that computed the row; nil before versions existed.
	ModelVersion *string `json:"model_version"`
	// Set by the api on read: scored, pending, waiting_for_sleep (today), stale (today).
	ScoringState string `json:"scoring_state"`
}

// DayDetail is GET /v1/days/{day}: one day's score with its sleep, strain and workouts.
// Sleep and Strain are null, not absent, when the day has no data for them.
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
