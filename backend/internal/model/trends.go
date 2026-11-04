package model

// Trend metrics (GET /v1/trends?metric=).
const (
	MetricReadiness   = "readiness"    // DailyScore.readiness
	MetricRecovery    = "recovery"     // DailyScore.recovery
	MetricStrain      = "strain"       // DailyScore.strain
	MetricSleep       = "sleep"        // DailyScore.sleep_performance (%), not minutes (D12)
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

// KnownTrendMetrics is the set of ?metric= values GET /v1/trends accepts (cmd/api/trends.go
// answers 422 for any other). It has grown additively.
var KnownTrendMetrics = map[string]bool{
	MetricReadiness: true, MetricRecovery: true, MetricStrain: true, MetricSleep: true,
	MetricSleepMin: true, MetricHRV: true, MetricRHR: true, MetricSteps: true, MetricActiveKcal: true,
	MetricStrengthMin: true, MetricVolumeKG: true, MetricSleepQual: true, MetricSleepCons: true, MetricNapMin: true,
}

// TrendPoint is one day's value in a Trend or in an exercise's e1RM series.
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
