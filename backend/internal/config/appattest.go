package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/appattest"
)

// MaxAppAttestExemptUsers bounds APP_ATTEST_EXEMPT_USERS: the smoke user and a few test accounts (D245).
const MaxAppAttestExemptUsers = 5

// AppAttest configures App Attest in api and ingest (D245, D247): the mode, the accounts it never
// asks, the token ring and the App ID the attestations are for.
type AppAttest struct {
	Mode        Mode        // APP_ATTEST: off (default), warn or on
	ExemptUsers []uuid.UUID // APP_ATTEST_EXEMPT_USERS: at most 5 accounts, every route
	// Ring is APP_ATTEST_TOKEN_KEYS parsed (the secret app-attest-token-key); nil when unset, and then
	// the three /v1/app-attest routes answer 501.
	Ring *appattest.Ring
	// TeamID and ClientID are APPLE_TEAM_ID and APPLE_CLIENT_ID: the App ID is TeamID.ClientID
	// (RequireAppID checks them on api).
	TeamID, ClientID string
}

// AppID is the App ID an attestation's RP ID hash is checked against: team id, ".", bundle id.
func (a AppAttest) AppID() string { return a.TeamID + "." + a.ClientID }

// Exempt says whether the account is in APP_ATTEST_EXEMPT_USERS.
func (a AppAttest) Exempt(user uuid.UUID) bool { return slices.Contains(a.ExemptUsers, user) }

// LoadAppAttest reads APP_ATTEST (off|warn|on, default off), APP_ATTEST_EXEMPT_USERS (empty, or 1 to 5
// lower-case UUIDs, comma-separated, each once), APP_ATTEST_TOKEN_KEYS (optional; appattest.ParseRing's
// form) and APPLE_TEAM_ID and APPLE_CLIENT_ID. It refuses a malformed ring in any mode, and warn or on
// without a ring: a service that checks tokens must be able to. No error holds a key, a line of the ring
// or an account id.
func LoadAppAttest() (AppAttest, error) {
	a := AppAttest{TeamID: strings.TrimSpace(os.Getenv("APPLE_TEAM_ID")), ClientID: strings.TrimSpace(os.Getenv("APPLE_CLIENT_ID"))}
	var err error
	if a.Mode, err = parseMode("APP_ATTEST", string(ModeOff)); err != nil {
		return AppAttest{}, err
	}
	if a.ExemptUsers, err = parseExemptUsers(os.Getenv("APP_ATTEST_EXEMPT_USERS")); err != nil {
		return AppAttest{}, fmt.Errorf("APP_ATTEST_EXEMPT_USERS: %w", err)
	}
	if raw := os.Getenv("APP_ATTEST_TOKEN_KEYS"); raw != "" {
		// ParseRing's errors name the line, never its content.
		if a.Ring, err = appattest.ParseRing(raw); err != nil {
			return AppAttest{}, fmt.Errorf("APP_ATTEST_TOKEN_KEYS: %w", err)
		}
	}
	if a.Mode != ModeOff && a.Ring == nil {
		return AppAttest{}, fmt.Errorf("APP_ATTEST=%s needs APP_ATTEST_TOKEN_KEYS (the token key ring, the secret "+
			"app-attest-token-key pinned by version): without it no token can be checked (D247)", a.Mode)
	}
	return a, nil
}

// RequireAppID refuses a ring without the App ID (api only: it verifies attestations, ingest checks
// tokens alone). Without a ring the routes answer 501 and the App ID is not needed.
func (a AppAttest) RequireAppID() error {
	if a.Ring != nil && (a.TeamID == "" || a.ClientID == "") {
		return errors.New("APP_ATTEST_TOKEN_KEYS is set, so the api verifies attestations: it needs APPLE_TEAM_ID and " +
			"APPLE_CLIENT_ID for the App ID (D247)")
	}
	return nil
}

// parseExemptUsers reads a comma-separated list of at most MaxAppAttestExemptUsers canonical lower-case
// UUIDs, each once; blank is none. Errors name the entry's position, never its value.
func parseExemptUsers(list string) ([]uuid.UUID, error) {
	if list == "" {
		return nil, nil
	}
	entries := strings.Split(list, ",")
	if len(entries) > MaxAppAttestExemptUsers {
		return nil, fmt.Errorf("at most %d accounts, got %d", MaxAppAttestExemptUsers, len(entries))
	}
	out := make([]uuid.UUID, 0, len(entries))
	for i, e := range entries {
		id, err := uuid.Parse(e)
		if err != nil || id.String() != e {
			return nil, fmt.Errorf("entry %d is not a lower-case UUID (8-4-4-4-12 hex digits, no spaces)", i+1)
		}
		if slices.Contains(out, id) {
			return nil, fmt.Errorf("entry %d repeats an earlier one", i+1)
		}
		out = append(out, id)
	}
	return out, nil
}
