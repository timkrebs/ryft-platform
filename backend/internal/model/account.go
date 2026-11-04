package model

import (
	"time"

	"github.com/google/uuid"
)

// AccountHint is GET /v1/me → account_hint: another account on this device.
type AccountHint struct {
	Kind          string    `json:"kind"` // "other_account_on_device"
	OtherProvider *string   `json:"other_provider"`
	OtherLastSync time.Time `json:"other_last_sync"`
}

// User is the account row as GET /v1/me serves it, and the user the handlers load once
// per request (auth.UserFrom). Email and Provider come from the token, not from the
// client.
type User struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`     // display name; from the sign-in (Apple full name), may be empty
	Email        *string   `json:"email"`    // from the token; null when unknown
	Provider     *string   `json:"provider"` // sign-in provider, e.g. "apple"; null when unknown
	TZ           string    `json:"tz"`
	MaxHR        int       `json:"max_hr"`
	SleepBaseMin int       `json:"sleep_base_min"`
	// Where sleep_base_min comes from (default|learned|manual) and the latest
	// learned estimate, also while manual.
	SleepBaseSource     string     `json:"sleep_base_source"`
	SleepBaseLearnedMin *int       `json:"sleep_base_learned_min"`
	SleepBaseLearnedAt  *time.Time `json:"sleep_base_learned_at"`
	// Push preferences (PATCH /v1/me/notifications).
	Notifications NotificationPrefs `json:"notifications"`
	// Beta: access through an invite code, and the current consent.
	BetaAccess bool     `json:"beta_access"`
	Consent    *Consent `json:"consent"`
	// The birth year (for the max-HR estimate) and where max_hr comes from
	// (default|estimated|manual).
	BirthYear   *int   `json:"birth_year"`
	MaxHRSource string `json:"max_hr_source"`
	// Apps and devices that sent data in the last 30 days, newest first; set by
	// GET and PATCH /v1/me (the per-request user row does not load them).
	Sources []UserSource `json:"sources"`
	// GET /v1/me only: another account on the X-RYFT-Device-ID device (D91).
	AccountHint *AccountHint `json:"account_hint"`
}

// Where User.max_hr comes from; closed in v1.
const (
	HRSourceDefault   = "default"   // 190, nothing known
	HRSourceEstimated = "estimated" // Tanaka, from the birth year
	HRSourceManual    = "manual"    // set with PATCH /v1/me
)

// Consent is the current, unrevoked consent to health-data processing.
type Consent struct {
	PolicyVersion string    `json:"policy_version"`
	GrantedAt     time.Time `json:"granted_at"`
}

// ConsentPost is POST /v1/me/consent.
type ConsentPost struct {
	PolicyVersion string  `json:"policy_version"`
	AppVersion    *string `json:"app_version"`
}

// InvitePost is POST /v1/me/invite.
type InvitePost struct {
	Code string `json:"code"`
}

// UserPatch is PATCH /v1/me. A nil field is left as it is; setting max_hr or
// sleep_base_min makes the user's value the source (manual) until the matching *_manual
// field says otherwise.
type UserPatch struct {
	TZ    *string `json:"tz,omitempty"`
	MaxHR *int    `json:"max_hr,omitempty"` // sets the source to manual
	// false: back to the estimate (or 190 without a birth year); true: keep as manual.
	MaxHRManual  *bool `json:"max_hr_manual,omitempty"`
	BirthYear    *int  `json:"birth_year,omitempty"`
	SleepBaseMin *int  `json:"sleep_base_min,omitempty"` // sets the source to manual
	// false: back to the learned estimate (or the default); true: keep as manual.
	SleepBaseManual *bool `json:"sleep_base_manual,omitempty"`
}
