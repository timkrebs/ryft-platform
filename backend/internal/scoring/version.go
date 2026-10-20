package scoring

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// ModelVersion names the scoring model: YEAR.MONTH.N. Bump it with every change
// that moves a stored number (a formula, a constant, a source rule), add the
// entry to changelog.json in the same commit, and rescore everyone after the
// deploy (docs/SCORING.md → Model version, D80). Scoring writes it on every
// daily_scores row and workout.
const ModelVersion = "2026.10.1"

//go:embed changelog.json
var changelogJSON []byte

// ModelChange is one changelog entry.
type ModelChange struct {
	Version   string   `json:"version"`
	Date      string   `json:"date"`
	SummaryEN string   `json:"summary_en"`
	SummaryDE string   `json:"summary_de"`
	Affects   []string `json:"affects"`
}

// Changelog is the model's history, newest first; checked when parsed.
func Changelog() ([]ModelChange, error) {
	var out []ModelChange
	if err := json.Unmarshal(changelogJSON, &out); err != nil {
		return nil, fmt.Errorf("changelog.json: %w", err)
	}
	if len(out) == 0 || out[0].Version != ModelVersion {
		return nil, fmt.Errorf("changelog.json: the newest entry must be ModelVersion %s", ModelVersion)
	}
	for k, c := range out {
		if _, err := time.Parse("2006-01-02", c.Date); err != nil || c.SummaryEN == "" || c.SummaryDE == "" || len(c.Affects) == 0 {
			return nil, fmt.Errorf("changelog.json: entry %s needs a date, both summaries and what it affects", c.Version)
		}
		if k > 0 && (c.Version >= out[k-1].Version || c.Date > out[k-1].Date) {
			return nil, fmt.Errorf("changelog.json: entries newest first (%s after %s)", c.Version, out[k-1].Version)
		}
		for _, m := range c.Affects {
			if !model.KnownTrendMetrics[m] {
				return nil, fmt.Errorf("changelog.json: %s affects unknown metric %q", c.Version, m)
			}
		}
		if !slices.IsSorted(c.Affects) {
			return nil, fmt.Errorf("changelog.json: %s lists affects sorted", c.Version)
		}
	}
	return out, nil
}
