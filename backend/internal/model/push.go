package model

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
)

// ---- Push (M7 core) ---------------------------------------------------------------

// Notification categories (the app's UNNotificationCategory identifiers).
const (
	NotifyMorningScore  = "MORNING_SCORE"
	NotifySessionScored = "SESSION_SCORED"
	NotifyPR            = "PR"
	NotifySyncStale     = "SYNC_STALE"
)

// NotifyCategories is every category a user can turn off, in display order.
var NotifyCategories = []string{NotifyMorningScore, NotifySessionScored, NotifyPR, NotifySyncStale}

// PushRegistration is POST /v1/devices/{id}/push.
type PushRegistration struct {
	Token  string `json:"token"`  // the APNs device token, hex
	Env    string `json:"env"`    // sandbox | production (the app's aps-environment)
	Locale string `json:"locale"` // the app's language, e.g. de or de-DE; en or de is kept
}

// PushToken is a registered app install.
type PushToken struct {
	DeviceID uuid.UUID
	Token    string
	Env      string
	Locale   string
}

// QuietHours are local times "HH:MM"; from > to wraps midnight (22:00–07:00).
type QuietHours struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// NotificationPrefs is User.notifications and the answer to PATCH
// /v1/me/notifications: every category with whether it is on.
type NotificationPrefs struct {
	Categories map[string]bool `json:"categories"`
	QuietHours *QuietHours     `json:"quiet_hours"`
}

// NotificationPatch is PATCH /v1/me/notifications: categories merge into the
// stored ones; quiet_hours absent keeps them, null clears them.
type NotificationPatch struct {
	Categories map[string]bool `json:"categories"`
	QuietHours OptionalQuiet   `json:"quiet_hours"`
}

// OptionalQuiet tells an absent quiet_hours (Set false) from null (Set, nil).
type OptionalQuiet struct {
	Set   bool
	Value *QuietHours
}

// UnmarshalJSON sets Set even for null, which is how PATCH tells "clear the quiet hours"
// from "leave them" (an absent key never reaches it). Unknown keys inside the object
// are refused, like everywhere else.
func (o *OptionalQuiet) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var q QuietHours
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		return err
	}
	o.Value = &q
	return nil
}
