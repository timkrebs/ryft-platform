package model

import (
	"time"

	"github.com/google/uuid"
)

// FlagTrainImport is the training import's feature flag (GET /v1/config), which POST
// /v1/train/import checks itself too.
const FlagTrainImport = "train.import"

// ModelInfo is GET /v1/model.
type ModelInfo struct {
	Current   string             `json:"current"`
	Changelog []ModelChangeEntry `json:"changelog"`
}

// ModelChangeEntry is one changelog entry, its summary in the request's language.
type ModelChangeEntry struct {
	Version   string   `json:"version"`
	Date      string   `json:"date"`
	Summary   string   `json:"summary"`
	SummaryEN string   `json:"summary_en"`
	SummaryDE string   `json:"summary_de"`
	Affects   []string `json:"affects"`
}

// ---- Remote config (GET /v1/config) --------------------------------------------

// FeatureFlag is one row of feature_flags: on for a user when Enabled and the
// user is on AllowUserIDs or in the first RolloutPct buckets (internal/flags).
type FeatureFlag struct {
	Key          string      `json:"key"`
	Enabled      bool        `json:"enabled"`
	RolloutPct   int         `json:"rollout_pct"`
	AllowUserIDs []uuid.UUID `json:"allow_user_ids"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// FlagInput is PUT /v1/admin/flags/{key}: the whole flag (enabled and
// rollout_pct required; a missing allow-list is empty).
type FlagInput struct {
	Enabled      *bool       `json:"enabled"`
	RolloutPct   *int        `json:"rollout_pct"`
	AllowUserIDs []uuid.UUID `json:"allow_user_ids"`
}

// RemoteConfig is GET /v1/config: every flag evaluated for the caller.
type RemoteConfig struct {
	Flags                map[string]bool `json:"flags"`
	EvaluatedAt          time.Time       `json:"evaluated_at"`
	TTLSec               int             `json:"ttl_sec"`
	PrivacyPolicyVersion string          `json:"privacy_policy_version"`
	PrivacyPolicyURL     string          `json:"privacy_policy_url"`
	// Analytics is null while the api has no project key (POSTHOG_PROJECT_KEY unset). It is
	// independent of the flag analytics.enabled: the app combines the two with the user's
	// opt-in (D231).
	Analytics *AnalyticsConfig `json:"analytics"`
	// AppAttest is the server's App Attest mode (APP_ATTEST, D247; AA2), always present: one
	// of AppAttestOff, AppAttestWarn, AppAttestOn. A 501 or 404 from POST
	// /v1/app-attest/challenge stays authoritative over it.
	AppAttest string `json:"app_attest"`
}

// The values of RemoteConfig.AppAttest, a closed enum.
const (
	AppAttestOff  = "off"
	AppAttestWarn = "warn"
	AppAttestOn   = "on"
)

// AnalyticsConfig is where the app sends its product analytics (PostHog EU Cloud, D231):
// the ingestion origin and the project's public API key. Both repositories are public, so
// the key is served here instead of being compiled into the app.
type AnalyticsConfig struct {
	Host       string `json:"host"`
	ProjectKey string `json:"project_key"`
}
