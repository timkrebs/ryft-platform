package model

import "time"

// N2RProfile opts an account into contextual nutrition scoring.
type N2RProfile struct {
	BodyMassKG      float64    `json:"body_mass_kg"`
	Sex             string     `json:"sex"`
	SweatRateLPH    *float64   `json:"sweat_rate_l_per_h"`
	SweatSodiumMgL  *float64   `json:"sweat_na_mg_per_l"`
	CaffeineHalfH   *float64   `json:"caffeine_half_life_h"`
	ExpectedBedtime string     `json:"expected_bedtime"`
	NextWorkoutAt   *time.Time `json:"next_workout_start_at"`
}

// N2RBudget explains the daily energy, carbohydrate and sweat allowances.
type N2RBudget struct {
	Strain          string  `json:"strain"`
	WorkoutMinutes  float64 `json:"workout_minutes"`
	EnergyTarget    float64 `json:"energy_target_kcal"`
	WorkoutEnergy   float64 `json:"workout_energy_kcal"`
	CarbTarget      float64 `json:"carb_target_g"`
	SweatSodium     float64 `json:"sweat_sodium_mg"`
	SodiumExemption float64 `json:"sodium_exemption_mg"`
	CarbAllowance   float64 `json:"post_workout_carb_budget_g"`
}

// N2RScore exposes inputs, adjustments and the availability of a full score.
type N2RScore struct {
	Version         string                    `json:"version"`
	Score           *float64                  `json:"n2r_score"`
	Quality         *float64                  `json:"quality_score"`
	Components      NutritionQualityBreakdown `json:"components"`
	Glucose         *float64                  `json:"glucose_score"`
	Budget          N2RBudget                 `json:"budget"`
	LoggedKcal      float64                   `json:"logged_kcal"`
	LoggedCarb      float64                   `json:"logged_carb_g"`
	CarbComplete    bool                      `json:"logged_carb_complete"`
	CoveredCarb     float64                   `json:"covered_carb_g"`
	CoveredSugar    float64                   `json:"covered_added_sugar_g"`
	NetSugar        *float64                  `json:"net_added_sugar_g"`
	OmegaDepot      *float64                  `json:"omega_3_depot_mg"`
	FuelMultiplier  float64                   `json:"fuel_multiplier"`
	AlcoholGKG      float64                   `json:"alcohol_g_kg"`
	AlcoholPenalty  float64                   `json:"alcohol_penalty"`
	CaffeineBedMg   *float64                  `json:"caffeine_bed_mg"`
	CaffeineHalfH   float64                   `json:"effective_caffeine_half_life_h"`
	CaffeinePenalty float64                   `json:"caffeine_penalty"`
	LateMealPenalty float64                   `json:"late_meal_penalty"`
	AutoPenalty     float64                   `json:"autonomic_penalty"`
	Bedtime         *time.Time                `json:"bedtime"`
	Tags            []string                  `json:"tags"`
}

// N2RBiometrics compares a wake day's observations with the preceding baseline.
type N2RBiometrics struct {
	WakeDate   string   `json:"wake_date"`
	RMSSDMean  *float64 `json:"ln_rmssd_7d_mean"`
	RMSSDSD    *float64 `json:"ln_rmssd_7d_sd"`
	RMSSDCV    *float64 `json:"ln_rmssd_7d_cv"`
	SWC        *float64 `json:"swc"`
	RMSSDDelta *float64 `json:"ln_rmssd_delta"`
	RHRDelta   *float64 `json:"rhr_delta"`
	Change     string   `json:"change"`
}

// N2RGuidanceCard contains one triggered rule, its actions and matching foods.
type N2RGuidanceCard struct {
	Code    string             `json:"code"`
	Actions []string           `json:"actions"`
	Targets map[string]float64 `json:"targets,omitempty"`
	Foods   []FoodSummary      `json:"foods"`
}

// N2RGuidance contains the current waking day's score and prioritized cards.
type N2RGuidance struct {
	Date       string            `json:"local_date"`
	Score      *N2RScore         `json:"score"`
	Biometrics *N2RBiometrics    `json:"morning_biometrics"`
	Cards      []N2RGuidanceCard `json:"cards"`
}
