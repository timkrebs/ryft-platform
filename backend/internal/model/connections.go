package model

import (
	"time"

	"github.com/google/uuid"
)

// Direct connections (D257; docs/API.md → Connections): a link from an account to a provider's own service,
// which delivers the user's data to RYFT instead of Apple Health.

// Connection statuses (Connection.status). Closed: the app decodes them as an enum. A flow that was started
// but not confirmed is no connection and has none.
const (
	ConnectionConnected   = "connected"
	ConnectionNeedsReauth = "needs_reauth"
	ConnectionRevoked     = "revoked"
)

// Why a connection has its status (connections.status_reason; never on the wire).
const (
	ReasonRefreshFailed   = "refresh_failed"
	ReasonPermissions     = "permissions"
	ReasonProviderRevoked = "provider_revoked"
	ReasonRelinked        = "relinked"
)

// ConnectionHistoryNone is Connection.history: a connection delivers from the moment it is connected, nothing
// from before. The set is open for later tiers: an app reads an unknown value as none.
const ConnectionHistoryNone = "none"

// MaxProviderPermissions bounds Connection.permissions.
const MaxProviderPermissions = 32

// Connection is one account's link to one provider (GET /v1/me/connections and the answer of confirm).
type Connection struct {
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	ConnectedAt time.Time  `json:"connected_at"`
	LastDataAt  *time.Time `json:"last_data_at"`
	Provides    []string   `json:"provides"` // the UserSource.kinds vocabulary, sorted
	History     string     `json:"history"`
	// ProviderUserID is the provider's id of the user's account there (1 to 128 characters), kept to tell provider
	// accounts apart and part of the account's data (Art. 15). Null on a revoked connection.
	ProviderUserID *string `json:"provider_user_id"`
	// Permissions are what the user granted at the provider, in its own words (open set, at most 32). Empty on a
	// revoked connection.
	Permissions []string `json:"permissions"`
}

// AvailableConnection is a provider this account may connect now.
type AvailableConnection struct {
	Provider string   `json:"provider"`
	Provides []string `json:"provides"`
	History  string   `json:"history"`
	// Accepting is false while the provider's user cap is full; a reconnect is always accepted.
	Accepting bool `json:"accepting"`
}

// ConnectionsResponse is GET /v1/me/connections.
type ConnectionsResponse struct {
	Connections []Connection          `json:"connections"`
	Available   []AvailableConnection `json:"available"`
}

// ConnectStart is the 200 of POST /v1/me/connections/{provider}/start.
type ConnectStart struct {
	AuthorizeURL string    `json:"authorize_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ConnectConfirmPost is the body of POST /v1/me/connections/{provider}/confirm.
type ConnectConfirmPost struct {
	Ticket string `json:"ticket"`
}

// ConnectionDeleted is the 200 of DELETE /v1/me/connections/{provider}.
type ConnectionDeleted struct {
	Status string `json:"status"` // always "disconnected"
	// Deregistered: true the provider ended the grant (or no longer knew it); false it could not be reached and
	// the platform retries; null there was nothing to end.
	Deregistered *bool `json:"deregistered"`
	DataDeleted  bool  `json:"data_deleted"` // always true
}

// The callback's redirect: ryft://connect/{provider}?status=<s>[&ticket=<t>]. Closed.
const (
	CallbackOK      = "ok"
	CallbackDenied  = "denied"
	CallbackExpired = "expired"
	CallbackError   = "error"
)

// InboxReceivedEvent is published (routing key inbox.received) by ingest's hook for every new or changed
// provider record it stored, and consumed by ingest (D257). It holds ids only: no health value.
type InboxReceivedEvent struct {
	InboxID   int64     `json:"inbox_id"`
	UserID    uuid.UUID `json:"user_id"`
	Provider  string    `json:"provider"`
	Kind      string    `json:"kind"`
	Revision  int       `json:"revision"`
	RequestID string    `json:"request_id,omitempty"`
}
