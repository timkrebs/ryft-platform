package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Invite statuses, a closed enum; the first that applies wins (cmd/api inviteStatus).
const (
	InviteRevoked = "revoked" // revoked_at is set
	InviteUsed    = "used"    // uses >= max_uses
	InviteExpired = "expired" // expires_at is not in the future
	InviteOpen    = "open"
)

// BetaInvite is an invite code as the admin routes show it.
type BetaInvite struct {
	Code      string     `json:"code"` // RYFT-7K3M-QX9P
	Note      string     `json:"note"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	// Email is the address the code is bound to; nil for a code anyone may redeem. The handler masks it
	// unless ?emails=1 (the create answer shows it as stored).
	Email     *string     `json:"email"`
	CreatedBy Actor       `json:"created_by"` // {"kind":"key","id":null} for codes from before 2026-11-05
	RevokedAt *time.Time  `json:"revoked_at"`
	Status    string      `json:"status"` // InviteOpen, InviteUsed, InviteExpired or InviteRevoked
	Link      string      `json:"link"`   // PUBLIC_WEB_URL + /invite/ + the code
	UsesBy    []InviteUse `json:"uses_by"`
}

// InviteUse is an account that redeemed a code. Only accounts that still exist are listed.
type InviteUse struct {
	UserID       uuid.UUID `json:"user_id"`
	ShortID      string    `json:"short_id"` // the first 8 hex digits of UserID
	Email        *string   `json:"email"`    // the account's email, masked unless ?emails=1; nil when it has none
	PrivateRelay bool      `json:"private_relay"`
	At           time.Time `json:"at"`
}

// BetaInviteInput is POST /v1/admin/invites.
type BetaInviteInput struct {
	Note      string     `json:"note"`
	MaxUses   *int       `json:"max_uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	Email     *string    `json:"email"` // binds the code to this address (max_uses 1); nil: anyone with the code
}

// FeedbackBrief is what an operator's Supabase token gets of a Feedback in GET /v1/admin/feedback: the
// message and where it stands, a short reference to the account, and none of the device fields, the
// request ids or the diagnostics. The admin key gets the whole Feedback. The projection is made on the server.
type FeedbackBrief struct {
	ID          int64      `json:"id"`
	UserShortID string     `json:"user_short_id"` // the account's first 8 hex digits
	Category    string     `json:"category"`
	Message     string     `json:"message"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

// Brief is the token caller's view of f.
func (f Feedback) Brief() FeedbackBrief {
	return FeedbackBrief{ID: f.ID, UserShortID: f.UserID.String()[:8], Category: f.Category, Message: f.Message,
		CreatedAt: f.CreatedAt, ResolvedAt: f.ResolvedAt}
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
