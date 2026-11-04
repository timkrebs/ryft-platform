package model

import (
	"time"
)

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

// SleepDetail is one night with its need term by term and its stage timeline
// (DayDetail.sleep).
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

// SleepRange backs the Sleep screen's Week / Month tabs (D7: the app fills gaps, days lists nights with data only).
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

// Sleep baseline sources (User.sleep_base_source); closed in v1.
const (
	BaseDefault = "default"
	BaseLearned = "learned"
	BaseManual  = "manual"
)
