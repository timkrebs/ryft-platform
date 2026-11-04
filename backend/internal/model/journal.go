package model

import (
	"time"

	"github.com/google/uuid"
)

// ---- Journal (M5) ----------------------------------------------------------------

// Behavior kinds and categories; closed in v1.
const (
	BehaviorYesNo = "yes_no"
	BehaviorCount = "count"
	BehaviorTime  = "time"
)

var (
	// BehaviorKinds is the closed set of Behavior.Kind values (API.md, Journal): the app decodes
	// it as a Swift enum, so a new member is a breaking change. cmd/api's journal validation and
	// the built-in seed (db.seed) check it.
	BehaviorKinds = []string{BehaviorYesNo, BehaviorCount, BehaviorTime}
	// BehaviorCategories is the closed set of Behavior.Category values (API.md, Journal), in
	// display order: store.Behaviors sorts by position in this slice. "custom" is for the
	// user's own behaviors; a built-in never uses it.
	BehaviorCategories = []string{"substances", "sleep", "recovery", "supplements", "health", "lifestyle", "custom"}
)

// Behavior is one journal question: a built-in (fixed id) or the user's own.
type Behavior struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"` // name_en or name_de, by Accept-Language
	NameEN    string    `json:"name_en"`
	NameDE    string    `json:"name_de"`
	Category  string    `json:"category"`
	Kind      string    `json:"kind"`
	DefaultOn bool      `json:"default_on"`
	Enabled   bool      `json:"enabled"`
	IsCustom  bool      `json:"is_custom"`
	Sort      int       `json:"sort"`
}

// BehaviorsPatch is PATCH /v1/behaviors.
type BehaviorsPatch struct {
	Enabled map[uuid.UUID]bool `json:"enabled"`
}

// BehaviorInput is POST /v1/behaviors.
type BehaviorInput struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	NameDE   *string   `json:"name_de"`
	Kind     string    `json:"kind"`
	Category *string   `json:"category"`
}

// JournalEntry is one behavior on one day.
type JournalEntry struct {
	BehaviorID uuid.UUID `json:"behavior_id"`
	ValueBool  bool      `json:"value_bool"`
	ValueNum   *int      `json:"value_num"`  // count behaviors
	ValueTime  *string   `json:"value_time"` // time behaviors, local "HH:MM"
}

// JournalDay is GET/PUT /v1/journal/{day}.
type JournalDay struct {
	Day        string         `json:"day"`
	Answered   bool           `json:"answered"`
	AnsweredAt *time.Time     `json:"answered_at"`
	Editable   bool           `json:"editable"`
	Entries    []JournalEntry `json:"entries"`
}

// JournalPut is the body of PUT /v1/journal/{day}: the whole day.
type JournalPut struct {
	Entries []JournalEntry `json:"entries"`
}

// BehaviorEffect is one metric's yes − no difference with its interval.
type BehaviorEffect struct {
	Delta       float64 `json:"delta"`
	CILo        float64 `json:"ci_lo"`
	CIHi        float64 `json:"ci_hi"`
	Significant bool    `json:"significant"`
	NYes        int     `json:"n_yes"`
	NNo         int     `json:"n_no"`
}

// ImpactNeeded is how many more days unlock a behavior.
type ImpactNeeded struct {
	Yes int `json:"yes"`
	No  int `json:"no"`
}

// Impact statuses.
const (
	ImpactLocked   = "locked"
	ImpactUnlocked = "unlocked"
)

// BehaviorImpact is one behavior's effect on the morning after.
type BehaviorImpact struct {
	BehaviorID       uuid.UUID       `json:"behavior_id"`
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	NYes             int             `json:"n_yes"`
	NNo              int             `json:"n_no"`
	Needed           ImpactNeeded    `json:"needed"`
	Recovery         *BehaviorEffect `json:"recovery"`
	HRVPct           *BehaviorEffect `json:"hrv_pct"`
	SleepPerformance *BehaviorEffect `json:"sleep_performance"`
}

// BehaviorImpacts is GET /v1/behaviors/impact.
type BehaviorImpacts struct {
	WindowDays int              `json:"window_days"`
	ComputedAt time.Time        `json:"computed_at"`
	Impacts    []BehaviorImpact `json:"impacts"`
}
