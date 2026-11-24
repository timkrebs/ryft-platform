package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Limits of a push (design 7.2). The hook reads at most MaxPushBytes, cuts at most MaxItems items from it, and
// stores an item only up to MaxItemBytes of payload and MaxRecordIDLen of record id.
const (
	MaxPushBytes   = 4 << 20
	MaxItemBytes   = 256 << 10
	MaxItems       = 500
	MaxRecordIDLen = 128
	// MaxSpan is the longest stretch of time one record's rows may cover (a night and the evening before it): a
	// record that spans more is refused as a bad record, and it bounds the replacement's scan.
	MaxSpan = 36 * time.Hour
	// MaxRows bounds the sleep segments and samples of one record.
	MaxRows = 600
)

// The kinds of an inbox item. Closed; others are counted bad_item.
const (
	KindSleep          = "sleep"
	KindHRV            = "hrv"
	KindDeregistration = "deregistration"
	KindPermissions    = "permissions"
)

// KnownKind says whether kind is one of the four.
func KnownKind(kind string) bool {
	switch kind {
	case KindSleep, KindHRV, KindDeregistration, KindPermissions:
		return true
	}
	return false
}

// The errors an adapter may return. Anything else is transient.
var (
	// ErrUnverified: the push does not carry a valid proof of its origin (answered 401).
	ErrUnverified = errors.New("connect: push not verified")
	// ErrBadPush: the verified body cannot be split into items (answered 400).
	ErrBadPush = errors.New("connect: bad push body")
	// ErrBadRecord: a stored item does not map to records (the inbox row fails, kept 7 days).
	ErrBadRecord = errors.New("connect: bad record")
	// ErrInvalidGrant: the provider refuses the refresh token or the code: the grant is gone (needs_reauth).
	ErrInvalidGrant = errors.New("connect: invalid grant")
	// ErrDenied: the user declined at the provider.
	ErrDenied = errors.New("connect: access denied")
	// ErrGone: the provider no longer knows the grant (a deregistration is done).
	ErrGone = errors.New("connect: grant gone")
)

// Grant is what an exchange or a refresh returns. The api seals Access and Refresh; nothing here logs them.
type Grant struct {
	ProviderUserID  string
	Access, Refresh string
	AccessExpires   time.Time
	RefreshDue      time.Time // zero: never refresh
	Permissions     []string
}

// OAuth is the half of a provider that holds and uses tokens: the api's.
type OAuth interface {
	// AuthorizeURL is the provider's authorize URL for the state. codeChallenge is the S256 challenge of the
	// verifier (Challenge). An adapter builds the query with SetPKCE, so code_challenge_method is always S256, and
	// its test runs the URL through CheckPKCE.
	AuthorizeURL(state, codeChallenge, redirectURI string) (string, error)
	Exchange(ctx context.Context, code, verifier, redirectURI string) (Grant, error)
	Refresh(ctx context.Context, refreshToken string) (Grant, error)
	// Deregister ends the grant at the provider; ErrGone when it no longer knows it.
	Deregister(ctx context.Context, g Grant) error
	// Required are the permissions a usable grant must hold.
	Required() []string
}

// PushItem is one record of a verified push body, cut out by SplitPush.
type PushItem struct {
	Kind           string // closed: KindSleep, KindHRV, KindDeregistration, KindPermissions
	RecordID       string // unique within (provider, kind) across users: an adapter whose ids are per user prefixes them
	ProviderUserID string
	Version        int64           // the provider's ordering token for the record; 0 when it has none
	Payload        json.RawMessage // this item only, at most MaxItemBytes
}

// Records is what one stored item means for RYFT.
type Records struct {
	Sleep       []model.SleepSegment // stages mapped onto the six HealthKit ones: the provider's "light" is core
	Samples     []model.Sample       // hrv_rmssd (ms), later others
	NightState  string               // model.RecordProvisional, model.RecordFinal, or "" (no night)
	Permissions []string             // KindPermissions: what the user holds now
}

// PushSide is the half of a provider that receives: ingest's. It holds no token and cannot reach one.
type PushSide interface {
	// MaxPushBytes is the most of a body the hook reads, at most MaxPushBytes.
	MaxPushBytes() int64
	// VerifyPush is the provider's own origin check over the raw headers and body. The body has not been parsed.
	// nil, ErrUnverified (401), or another error (503: it cannot be checked now).
	VerifyPush(h http.Header, body []byte, now time.Time) error
	// SplitPush cuts a verified body into items. It reads no health value. ErrBadPush answers 400.
	SplitPush(body []byte) ([]PushItem, error)
	// ParseRecord maps one stored item to RYFT's records. Pure: no network, no token, no clock. ErrBadRecord fails
	// the inbox row.
	ParseRecord(kind string, payload json.RawMessage) (Records, error)
	// Required are the permissions a usable grant must hold: a permissions notice that lacks one makes the
	// connection needs_reauth.
	Required() []string
}

// Provider is one direct source.
type Provider interface {
	Name() string // [a-z][a-z0-9]{1,31}: "garmin", "fake"
	DisplayName() string
	Provides() []string // model.Kind*, sorted
	History() string    // "none"
	OAuth
	PushSide
}
