package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BetaInvite is an invite code as the admin routes show it.
type BetaInvite struct {
	Code      string     `json:"code"` // RYFT-7K3M-QX9P
	Note      string     `json:"note"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// BetaInviteInput is POST /v1/admin/invites.
type BetaInviteInput struct {
	Note      string     `json:"note"`
	MaxUses   *int       `json:"max_uses"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Feedback categories; closed in v1.
const (
	FeedbackBug   = "bug"
	FeedbackIdea  = "idea"
	FeedbackData  = "data"
	FeedbackOther = "other"
)

// FeedbackPost is POST /v1/feedback.
type FeedbackPost struct {
	Message     string          `json:"message"`
	Category    *string         `json:"category"`
	AppVersion  *string         `json:"app_version"`
	Build       *string         `json:"build"`
	DeviceModel *string         `json:"device_model"`
	OSVersion   *string         `json:"os_version"`
	RequestIDs  []string        `json:"request_ids"`
	Diagnostics json.RawMessage `json:"diagnostics"` // any JSON object
}

// Feedback is a stored report as GET /v1/admin/feedback shows it.
type Feedback struct {
	ID          int64           `json:"id"`
	UserID      uuid.UUID       `json:"user_id"`
	Category    string          `json:"category"`
	Message     string          `json:"message"`
	AppVersion  *string         `json:"app_version"`
	Build       *string         `json:"build"`
	DeviceModel *string         `json:"device_model"`
	OSVersion   *string         `json:"os_version"`
	RequestIDs  []string        `json:"request_ids"`
	Diagnostics json.RawMessage `json:"diagnostics"` // null when none
	CreatedAt   time.Time       `json:"created_at"`
	ResolvedAt  *time.Time      `json:"resolved_at"`
}
