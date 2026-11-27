package model

import "time"

// GoalProfile holds adult coaching settings. Dates describe the user's local calendar.
type GoalProfile struct {
	Goal           string   `json:"goal"`
	RaceDate       *string  `json:"race_date,omitempty"`
	HeightCM       float64  `json:"height_cm"`
	BodyFatPct     *float64 `json:"body_fat_pct,omitempty"`
	FTP            *float64 `json:"ftp_w,omitempty"`
	HRMax          *float64 `json:"hr_max,omitempty"`
	HRRest         *float64 `json:"hr_rest,omitempty"`
	GutTrained     bool     `json:"gut_trained"`
	Recreational   bool     `json:"recreational"`
	SweatSodium    *float64 `json:"sweat_na_mg_per_l,omitempty"`
	GoalAdjustment *float64 `json:"goal_adjustment,omitempty"`
	TrainLow       bool     `json:"train_low"`
}

// PlannedSession supplies today's intended work even before any device has synced.
type PlannedSession struct {
	Start   time.Time `json:"start_at"`
	Sport   string    `json:"sport_type"`
	Minutes float64   `json:"duration_min"`
	Class   string    `json:"class"`
}

// CoachingFeedback records the plan, intake confidence and optional post-session observations.
type CoachingFeedback struct {
	IntakeComplete bool            `json:"intake_complete"`
	Fasting        bool            `json:"is_fasting_day"`
	Session        *PlannedSession `json:"planned_session,omitempty"`
	WorkoutMinutes *float64        `json:"workout_minutes,omitempty"`
	PreMass        *float64        `json:"pre_workout_mass_kg,omitempty"`
	PostMass       *float64        `json:"post_workout_mass_kg,omitempty"`
	FluidL         *float64        `json:"fluid_during_workout_l,omitempty"`
	UrineL         *float64        `json:"urine_during_workout_l,omitempty"`
	SweatPatch     *float64        `json:"sweat_patch_na_mg_per_l,omitempty"`
	IntraCarbs     *float64        `json:"intra_workout_carbs_g,omitempty"`
	GIDistress     *float64        `json:"gi_distress_0_10,omitempty"`
	RPE            *float64        `json:"rpe_0_10,omitempty"`
	Fatigue        *float64        `json:"fatigue_0_10,omitempty"`
	MeasuredRMR    *float64        `json:"measured_rmr_kcal,omitempty"`
	// A phase offset must come from personal cycle data with at least two tracked cycles.
	CycleWaterKG       *float64 `json:"cycle_water_kg,omitempty"`
	ReportedConfounder *bool    `json:"reported_overnight_confounder,omitempty"`
	TrackedCycles      int      `json:"tracked_cycles,omitempty"`
}

// FuelingPlan expresses session carbohydrates and rehydration quantities.
type FuelingPlan struct {
	CarbMinGPerH         float64  `json:"carbs_min_g_per_h"`
	CarbMaxGPerH         float64  `json:"carbs_max_g_per_h"`
	GlucoseFructoseRatio string   `json:"glucose_fructose_ratio,omitempty"`
	PreRaceMinG          *float64 `json:"pre_race_min_g,omitempty"`
	PreRaceMaxG          *float64 `json:"pre_race_max_g,omitempty"`
	RehydrateL           *float64 `json:"post_workout_fluids_l,omitempty"`
	SweatSodiumMg        *float64 `json:"sweat_sodium_mg,omitempty"`
}

// Availability gives the guardrail state and available evidence.
type Availability struct {
	State   string   `json:"state"`
	Energy  *float64 `json:"energy_kcal_per_kg_ffm,omitempty"`
	Carbs   *float64 `json:"carbs_g_per_kg,omitempty"`
	Markers []string `json:"markers"`
}

// CoachingPlan holds the daily energy and macro targets for a goal.
type CoachingPlan struct {
	EnergyKcal       float64      `json:"energy_target_kcal"`
	ProteinG         float64      `json:"protein_target_g"`
	CarbsG           float64      `json:"carb_target_g"`
	FatG             float64      `json:"fat_target_g"`
	NetExercise      float64      `json:"net_exercise_kcal"`
	SessionClass     string       `json:"session_class"`
	DeficitSuspended bool         `json:"deficit_suspended"`
	TrainLow         bool         `json:"train_low"`
	Fuel             *FuelingPlan `json:"fueling,omitempty"`
	Availability     Availability `json:"availability"`
}

// CoachingLoad gives the relative volumes of recent and longer-term training.
type CoachingLoad struct {
	Acute   float64  `json:"acute_7d"`
	Chronic float64  `json:"chronic_28d_weekly"`
	Ratio   *float64 `json:"acwr"`
	Balance float64  `json:"tsb"`
}

// CoachingBriefing combines one waking day’s nutrition, readiness and guidance.
type CoachingBriefing struct {
	Date             string            `json:"local_date"`
	Plan             *CoachingPlan     `json:"plan"`
	Readiness        string            `json:"readiness"`
	ReadinessDisplay string            `json:"readiness_display"` // Readiness with AUTONOMIC_FATIGUE shown as LOW_RECOVERY_UNEXPLAINED (D294); always present, whatever the flag
	Reasons          []string          `json:"readiness_reasons"`
	Load             CoachingLoad      `json:"training_load"`
	N2R              *N2RScore         `json:"n2r"`
	Yesterday        *N2RScore         `json:"yesterday_n2r"`
	Biometrics       *N2RBiometrics    `json:"morning_biometrics"`
	Cards            []N2RGuidanceCard `json:"cards"`
	Recovery         *RecoveryStatus   `json:"recovery"` // today's status, also when local_date is still yesterday; null while recovery.checkin is off (D291)
}

// EvaluationCohort reports operator-facing aggregate evaluation results.
type EvaluationCohort struct {
	Dimension           string   `json:"dimension"`
	Cohort              string   `json:"cohort"`
	Accounts            int      `json:"accounts"`
	Predictions         int      `json:"predictions"`
	EnergyMAE           *float64 `json:"tdee_mae_kcal_per_day"`
	EnergyBias          *float64 `json:"tdee_bias_kcal_per_day"`
	WeightMAPE          *float64 `json:"weight_change_mape_pct"`
	RecoveryCorrelation *float64 `json:"n2r_to_hrv_correlation"`
	Brier               *float64 `json:"red_recovery_brier"`
	ConfounderPrecision *float64 `json:"confounder_precision"`
	LEARecovery         *float64 `json:"lea_intervention_recovery_rate"`
}
