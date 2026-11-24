package model

import (
	"time"

	"github.com/google/uuid"
)

// NutritionQualityProfile classifies food by edible mass and identifies sugary drinks and juice.
type NutritionQualityProfile struct {
	NonStarchyVegetableFraction *float64 `json:"non_starchy_vegetable_fraction,omitempty"`
	WholeFruitFraction          *float64 `json:"whole_fruit_fraction,omitempty"`
	NutsLegumesFraction         *float64 `json:"nuts_legumes_fraction,omitempty"`
	RedProcessedMeatFraction    *float64 `json:"red_processed_meat_fraction,omitempty"`
	SugaryDrink                 *bool    `json:"sugary_drink,omitempty"`
}

// NutritionGlucoseSample is a CGM reading with an absolute timestamp and a fixed unit.
type NutritionGlucoseSample struct {
	Timestamp   time.Time `json:"timestamp"`
	GlucoseMgDL float64   `json:"glucose_mg_dl"`
}

// NutritionGlucoseUpload upserts a bounded batch of the caller's CGM readings.
type NutritionGlucoseUpload struct {
	Samples []NutritionGlucoseSample `json:"samples"`
}

// NutritionQualityBreakdown holds all ten components in points from zero to ten.
type NutritionQualityBreakdown struct {
	NonStarchyVegG        *float64 `json:"non_starchy_veg_g"`
	WholeFruitG           *float64 `json:"whole_fruit_g"`
	FiberG                *float64 `json:"fiber_g"`
	NutsLegumesG          *float64 `json:"nuts_legumes_g"`
	Omega3EPADHAMg        *float64 `json:"omega3_epa_dha_mg"`
	UnsaturatedFatKcalPct *float64 `json:"unsaturated_fat_kcal_pct"`
	AddedSugarG           *float64 `json:"added_sugar_g"`
	RedProcessedMeatG     *float64 `json:"red_processed_meat_g"`
	SodiumMg              *float64 `json:"sodium_mg"`
	TransFatKcalPct       *float64 `json:"trans_fat_kcal_pct"`
}

// NutritionMealGlucoseScore explains the score for one independently evaluable meal window.
type NutritionMealGlucoseScore struct {
	MealID        uuid.UUID `json:"meal_id"`
	WindowEnd     time.Time `json:"window_end"`
	SampleCount   int       `json:"sample_count"`
	BaselineMgDL  float64   `json:"baseline_mg_dl"`
	PeakDeltaMgDL float64   `json:"peak_delta_mg_dl"`
	IAUCMgDLMin   float64   `json:"iauc_mg_dl_min"`
	EndDeltaMgDL  float64   `json:"end_delta_mg_dl"`
	PeakScore     float64   `json:"peak_score"`
	AUCScore      float64   `json:"auc_score"`
	RecoveryScore float64   `json:"recovery_score"`
	GlucoseScore  float64   `json:"glucose_score"`
}

// NutritionScores reports versioned score components and their availability.
type NutritionScores struct {
	ScoreVersion           string                      `json:"score_version"`
	NutritionScore         *float64                    `json:"nutrition_score"`
	QualityScore           *float64                    `json:"quality_score"`
	GlucoseScore           *float64                    `json:"glucose_score"`
	Breakdown              NutritionQualityBreakdown   `json:"breakdown"`
	MealGlucoseScores      []NutritionMealGlucoseScore `json:"meal_glucose_scores"`
	ChrononutritionPenalty float64                     `json:"chrononutrition_penalty"`
}
