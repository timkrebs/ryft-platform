package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AdminUser is one row of GET /v1/admin/users: who the tester is (no email
// unless asked) and whether their data looks right.
type AdminUser struct {
	ID             uuid.UUID     `json:"id"`
	ShortID        string        `json:"short_id"`
	CreatedAt      time.Time     `json:"created_at"`
	Provider       *string       `json:"provider"`
	Email          *string       `json:"email,omitempty"` // only with ?emails=1
	BetaAccess     bool          `json:"beta_access"`
	ConsentVersion *string       `json:"consent_version"`
	AppVersion     *string       `json:"app_version"` // of the device that synced last
	OSVersion      *string       `json:"os_version"`
	LastSyncAt     *time.Time    `json:"last_sync_at"`
	Sources        []AdminSource `json:"sources"` // the last 30 days
	DaysScored14   int           `json:"days_scored_14d"`
	NightsStaged14 int           `json:"nights_with_stages_14d"`
	DaysHRV14      int           `json:"days_with_hrv_14d"`
	Calibrating    *bool         `json:"calibrating"` // the latest scored day; null without one
	MaxHRSource    string        `json:"max_hr_source"`
	OpenFeedback   int           `json:"open_feedback"`
	// Other accounts that synced from one of this account's devices in the
	// last 30 days (GET /v1/admin/users/{id} names them).
	Duplicates int `json:"dup"`
}

// AdminUserDetail is GET /v1/admin/users/{id}: the account, its devices, its
// data and other accounts on its devices (D89). Emails are masked unless asked.
type AdminUserDetail struct {
	Account    AdminAccount     `json:"account"`
	Devices    []AdminDevice    `json:"devices"`
	Data       AdminUserData    `json:"data"`
	Duplicates []AdminDuplicate `json:"possible_duplicates"`
}

// AdminAccount is who the user is and what the gates know of them.
type AdminAccount struct {
	ID           uuid.UUID `json:"id"`
	ShortID      string    `json:"short_id"`
	CreatedAt    time.Time `json:"created_at"`
	Provider     *string   `json:"provider"`
	Email        *string   `json:"email"` // masked (t***@g***.com) unless ?emails=1
	PrivateRelay bool      `json:"private_relay"`
	// From the Supabase admin API: every way into the account. Null when it was
	// not available (IdentitiesNote says why).
	Identities     []AdminIdentity `json:"identities"`
	IdentitiesNote string          `json:"identities_note,omitempty"`
	LastSignInAt   *time.Time      `json:"last_sign_in_at"`
	BetaAccessAt   *time.Time      `json:"beta_access_at"`
	Consent        *AdminConsent   `json:"consent"`
	MaxHR          int             `json:"max_hr"`
	MaxHRSource    string          `json:"max_hr_source"`
	TZ             string          `json:"tz"`
}

// AdminIdentity is one sign-in method of an account.
type AdminIdentity struct {
	Provider     string     `json:"provider"`
	Email        *string    `json:"email"`
	PrivateRelay bool       `json:"private_relay"` // an Apple "Hide My Email" address
	LastSignInAt *time.Time `json:"last_sign_in_at"`
}

// AdminConsent is the current consent with the app version it came from.
type AdminConsent struct {
	PolicyVersion string    `json:"policy_version"`
	GrantedAt     time.Time `json:"granted_at"`
	AppVersion    *string   `json:"app_version"`
}

// AdminDevice is one app install of the account.
type AdminDevice struct {
	ID                uuid.UUID       `json:"id"`
	Name              string          `json:"name"`
	AppVersion        *string         `json:"app_version"`
	OSVersion         *string         `json:"os_version"`
	FirstSyncAt       time.Time       `json:"first_sync_at"`
	LastSyncAt        *time.Time      `json:"last_sync_at"`
	LastSync          json.RawMessage `json:"last_sync"` // what the last accepted sync carried; null before migration 0023
	PushEnv           *string         `json:"push_env"`
	PushLastSuccessAt *time.Time      `json:"push_last_success_at"`
}

// AdminUserData is whether the account's data looks right.
type AdminUserData struct {
	DaysScored14   int                  `json:"days_scored_14d"`
	NightsStaged14 int                  `json:"nights_with_stages_14d"`
	DaysHRV14      int                  `json:"days_with_hrv_14d"`
	Latest         map[string]time.Time `json:"latest"` // newest record per source kind
	LastSyncAt     *time.Time           `json:"last_sync_at"`
	LastSync       json.RawMessage      `json:"last_sync"`
	OpenFeedback   int                  `json:"open_feedback"`
}

// AdminDuplicate is another account that synced from one of this account's
// devices in the last 30 days: likely the same person signed in another way.
type AdminDuplicate struct {
	ID           uuid.UUID  `json:"id"`
	ShortID      string     `json:"short_id"`
	Provider     *string    `json:"provider"`
	LastSyncAt   *time.Time `json:"last_sync_at"`
	DaysScored   int        `json:"days_scored"`
	NewestRecord *time.Time `json:"newest_record"`
}

// AuditRecord is one admin call as GET /v1/admin/audit shows it.
type AuditRecord struct {
	ID             int64           `json:"id"`
	At             time.Time       `json:"at"`
	KeyFingerprint string          `json:"key_fingerprint"`
	Action         string          `json:"action"` // the route pattern
	TargetUser     *uuid.UUID      `json:"target_user"`
	Status         int             `json:"status"`
	RequestID      string          `json:"request_id"`
	Details        json.RawMessage `json:"details"`
}

// AdminSource is a source in the admin view: name and kinds.
type AdminSource struct {
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
}
