package auth

import (
	"encoding/json"
	"time"
)

// The claims below decide operator access, and they are read leniently: any shape other than the
// expected one yields the empty value, never an error. Supabase documents the shapes (aal: "aal1"
// | "aal2"; amr: [{"method": "totp", "timestamp": 1730790000}]), but the same parser serves every
// app user, and a typed field would turn one unexpected shape into a 401 for all of them.

const (
	maxRoleLen = 32
	maxAMR     = 32 // entries read; Supabase adds one per method used in the session
)

// parseAppRole is app_metadata.role when it is a JSON string of at most 32 bytes.
func parseAppRole(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || len(s) > maxRoleLen {
		return ""
	}
	return s
}

// parseAAL is the authenticator assurance level when it is exactly "aal1" or "aal2".
func parseAAL(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || (s != "aal1" && s != "aal2") {
		return ""
	}
	return s
}

// parseTOTPAt is the newest time of an amr entry whose method is "totp"; zero when there is none.
// An entry that is not an object with a string method and a numeric timestamp is skipped (RFC 8176
// strings such as "pwd" included).
func parseTOTPAt(raw json.RawMessage) time.Time {
	var entries []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil {
		return time.Time{}
	}
	if len(entries) > maxAMR {
		entries = entries[:maxAMR]
	}
	var newest time.Time
	for _, e := range entries {
		var m struct {
			Method    string  `json:"method"`
			Timestamp float64 `json:"timestamp"`
		}
		if json.Unmarshal(e, &m) != nil || m.Method != "totp" || !(m.Timestamp > 0 && m.Timestamp < 1e11) {
			continue
		}
		if at := time.Unix(int64(m.Timestamp), 0).UTC(); at.After(newest) {
			newest = at
		}
	}
	return newest
}
