package model

import "time"

// Recovery without a watch (docs/API.md → Recovery without a watch; D291–D296): a morning check-in, its recovery
// estimate and the session RPE of a workout.

// FlagRecoveryCheckin is the feature flag that serves the check-in and session RPE writes and the status route
// (GET /v1/config). While it is off for an account the two writes and the status answer 404, and stored check-ins
// and ratings count nowhere: a kill switch (D295).
const FlagRecoveryCheckin = "recovery.checkin"

// N2RScore.bedtime_source (a closed set, D293): the profile's expected_bedtime, the night's measured sleep start,
// or the bedtime of the next morning's check-in.
const (
	N2RBedtimeExpected = "expected"
	N2RBedtimeWearable = "wearable"
	N2RBedtimeCheckin  = "checkin"
)

// RecoveryCheckinPut is PUT /v1/recovery/checkins/{date}: what the user reports. Every field is required;
// tz_offset_sec and soreness are pointers so a missing one can be told from 0 and false (422).
type RecoveryCheckinPut struct {
	Bedtime               time.Time `json:"bedtime"`
	WakeTime              time.Time `json:"wake_time"`
	TZOffsetSec           *int      `json:"tz_offset_sec"`
	PerceivedSleepQuality int       `json:"perceived_sleep_quality"`
	PerceivedFreshness    int       `json:"perceived_freshness"`
	Soreness              *bool     `json:"soreness"`
}

// RecoveryComponents are the parts behind a check-in's recovery_score, each 0–100. A null part is left out of the
// score and the weights of the others are scaled up.
type RecoveryComponents struct {
	Wellness         int  `json:"wellness"`
	WellnessAbsolute int  `json:"wellness_absolute"`
	WellnessPersonal *int `json:"wellness_personal"`
	Duration         int  `json:"duration"`
	Regularity       *int `json:"regularity"`
	N2R              *int `json:"n2r"`
}

// RecoveryCheckin is a stored morning check-in with what was computed from it. confidence, superseded and
// n2r_bedtime_applied are derived when it is read.
type RecoveryCheckin struct {
	Date                  string             `json:"date"`
	Bedtime               time.Time          `json:"bedtime"`
	WakeTime              time.Time          `json:"wake_time"`
	TZOffsetSec           int                `json:"tz_offset_sec"`
	PerceivedSleepQuality int                `json:"perceived_sleep_quality"`
	PerceivedFreshness    int                `json:"perceived_freshness"`
	Soreness              bool               `json:"soreness"`
	SleepDurationMin      int                `json:"sleep_duration_min"`
	RecoveryScore         int                `json:"recovery_score"`
	Components            RecoveryComponents `json:"components"`
	AcuteDrop             bool               `json:"acute_drop"`
	Confounders           []string           `json:"confounders"`
	Confidence            string             `json:"confidence"`
	Superseded            bool               `json:"superseded"`
	N2RBedtimeApplied     bool               `json:"n2r_bedtime_applied"`
	ModelVersion          string             `json:"model_version"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

// The tiers of RecoveryStatus.source and its confidence (closed enums, D291).
const (
	RecoverySourceWearable = "wearable"
	RecoverySourceCheckin  = "checkin"
	RecoverySourceNone     = "none"

	RecoveryConfidenceHigh   = "high"
	RecoveryConfidenceMedium = "medium"
	RecoveryConfidenceLow    = "low"

	// RecoveryBasisCheckins7d and RecoveryBasisExpectedBedtime are RecoveryStatus.suggestion_basis (closed).
	RecoveryBasisCheckins7d      = "checkins_7d"
	RecoveryBasisExpectedBedtime = "expected_bedtime"
)

// RecoveryStatus is GET /v1/recovery/status and CoachingBriefing.recovery: today's recovery, from which source,
// and whether to ask for a check-in.
type RecoveryStatus struct {
	Date              string           `json:"date"`
	Source            string           `json:"source"`
	Confidence        *string          `json:"confidence"`
	RecoveryScore     *int             `json:"recovery_score"`
	NeedsCheckin      bool             `json:"needs_checkin"`
	Checkin           *RecoveryCheckin `json:"checkin"`
	SuggestedBedtime  *string          `json:"suggested_bedtime"`
	SuggestedWakeTime *string          `json:"suggested_wake_time"`
	SuggestionBasis   *string          `json:"suggestion_basis"`
}

// WorkoutRPEPut is PUT /v1/workouts/{id}/rpe: the session RPE (Foster et al. 2001), 1–10 in steps of 0.5. A
// pointer so a missing number is a 422, not a 0.
type WorkoutRPEPut struct {
	SessionRPE *float64 `json:"session_rpe"`
}
