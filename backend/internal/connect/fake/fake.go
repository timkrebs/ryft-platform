package fake

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

// Name is the fake's provider id.
const Name = "fake"

// SignatureHeader carries hex(HMAC-SHA256(secret, body)) of a push.
const SignatureHeader = "X-Ryft-Fake-Signature"

// Permissions the fake grants, and requires.
var permissions = []string{"read_hrv", "read_sleep"}

// Provider is the fake adapter.
type Provider struct {
	secret []byte

	// DeregisterErr, when set, is what Deregister returns (a provider that cannot be reached); RefreshErr is
	// Refresh's, ExchangeErr Exchange's. Tests set them before use.
	DeregisterErr, RefreshErr, ExchangeErr error

	mu    sync.Mutex
	calls []Call
	n     int
}

// Call is one recorded network-side call, for tests. Tokens are not recorded.
type Call struct {
	Op             string // "exchange", "refresh", "deregister"
	ProviderUserID string
}

// New is the fake with the secret its pushes are signed with (CONNECT_FAKE_PUSH_SECRET).
func New(pushSecret string) *Provider { return &Provider{secret: []byte(pushSecret)} }

// Name is the fake's provider id.
func (p *Provider) Name() string { return Name }

// DisplayName is the name the app shows.
func (p *Provider) DisplayName() string { return "Fake" }

// Provides is what the fake delivers.
func (p *Provider) Provides() []string { return []string{model.KindHRV, model.KindSleep} }

// History is the fake's backfill: none.
func (p *Provider) History() string { return model.ConnectionHistoryNone }

// MaxPushBytes is the hook's bound for the fake's pushes.
func (p *Provider) MaxPushBytes() int64 { return connect.MaxPushBytes }

// Required is the permissions a grant must hold.
func (p *Provider) Required() []string { return slices.Clone(permissions) }

func (p *Provider) record(op, puid string) {
	p.mu.Lock()
	p.calls = append(p.calls, Call{op, puid})
	p.mu.Unlock()
}

// Calls are the calls made so far, oldest first.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// Called says whether op was called for the provider user id.
func (p *Provider) Called(op, puid string) bool {
	for _, c := range p.Calls() {
		if c.Op == op && c.ProviderUserID == puid {
			return true
		}
	}
	return false
}

// UserID is the provider user id the fake gives for a state.
func UserID(state string) string {
	h := sha256.Sum256([]byte(state))
	return hex.EncodeToString(h[:])[:16]
}

// Sign is the signature header value of a body under secret.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// AuthorizeURL is the redirect URI itself with the state and a code: the fake authorises at once. The code is
// "<code challenge>.<provider user id>", so Exchange can check the PKCE pair and still know the account, and a
// code is worth nothing without its verifier.
func (p *Provider) AuthorizeURL(state, codeChallenge, redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("code", codeChallenge+"."+UserID(state))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// The fake's tokens carry the provider user id, so a refresh and a deregistration know whose they are.
func access(puid string, n int) string { return fmt.Sprintf("fake-access.%s.%d", puid, n) }
func refreshToken(puid string) string  { return "fake-refresh." + puid }

func (p *Provider) grant(puid string) connect.Grant {
	p.mu.Lock()
	p.n++
	n := p.n
	p.mu.Unlock()
	now := time.Now()
	return connect.Grant{ProviderUserID: puid, Access: access(puid, n), Refresh: refreshToken(puid),
		AccessExpires: now.Add(time.Hour), RefreshDue: now.Add(50 * time.Minute), Permissions: slices.Clone(permissions)}
}

// Exchange checks the PKCE pair and gives a grant.
func (p *Provider) Exchange(_ context.Context, code, verifier, _ string) (connect.Grant, error) {
	if p.ExchangeErr != nil {
		return connect.Grant{}, p.ExchangeErr
	}
	challenge, puid, ok := strings.Cut(code, ".")
	if !ok || puid == "" || connect.Challenge(verifier) != challenge {
		return connect.Grant{}, connect.ErrInvalidGrant
	}
	p.record("exchange", puid)
	return p.grant(puid), nil
}

// Refresh gives a new grant for the refresh token the fake issued.
func (p *Provider) Refresh(_ context.Context, refresh string) (connect.Grant, error) {
	if p.RefreshErr != nil {
		return connect.Grant{}, p.RefreshErr
	}
	puid, ok := strings.CutPrefix(refresh, "fake-refresh.")
	if !ok || puid == "" {
		return connect.Grant{}, connect.ErrInvalidGrant
	}
	p.record("refresh", puid)
	return p.grant(puid), nil
}

// Deregister records the call.
func (p *Provider) Deregister(_ context.Context, g connect.Grant) error {
	if p.DeregisterErr != nil {
		return p.DeregisterErr
	}
	p.record("deregister", g.ProviderUserID)
	return nil
}

// VerifyPush checks the HMAC of the raw body in constant time.
func (p *Provider) VerifyPush(h http.Header, body []byte, _ time.Time) error {
	got, err := hex.DecodeString(h.Get(SignatureHeader))
	if err != nil || len(got) != sha256.Size {
		return connect.ErrUnverified
	}
	m := hmac.New(sha256.New, p.secret)
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return connect.ErrUnverified
	}
	return nil
}

// envelope is a push body as far as SplitPush reads it: no health value.
type envelope struct {
	Items []json.RawMessage `json:"items"`
}

type itemHead struct {
	Kind     string `json:"kind"`
	RecordID string `json:"record_id"`
	User     string `json:"user"`
	Version  int64  `json:"version"`
}

// SplitPush cuts {"items":[...]} into items, reading each one's head only.
func (p *Provider) SplitPush(body []byte) ([]connect.PushItem, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || len(env.Items) == 0 || len(env.Items) > connect.MaxItems {
		return nil, connect.ErrBadPush
	}
	out := make([]connect.PushItem, 0, len(env.Items))
	for _, raw := range env.Items {
		var h itemHead
		if err := json.Unmarshal(raw, &h); err != nil || h.Version < 0 {
			return nil, connect.ErrBadPush
		}
		out = append(out, connect.PushItem{Kind: h.Kind, RecordID: h.RecordID, ProviderUserID: h.User, Version: h.Version, Payload: raw})
	}
	return out, nil
}

type segment struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Stage string    `json:"stage"`
}

type payload struct {
	Night *struct {
		State    string    `json:"state"`
		Segments []segment `json:"segments"`
	} `json:"night"`
	HRV *struct {
		Start   time.Time `json:"start"`
		End     time.Time `json:"end"`
		RMSSDMs float64   `json:"rmssd_ms"`
	} `json:"hrv"`
	Permissions []string `json:"permissions"`
}

// stages maps the fake's stages onto the six HealthKit ones: its "light" is core.
var stages = map[string]string{"light": model.SleepCore, "deep": model.SleepDeep, "rem": model.SleepREM, "awake": model.SleepAwake}

// ParseRecord maps one stored item to records. It is pure.
func (p *Provider) ParseRecord(kind string, raw json.RawMessage) (connect.Records, error) {
	var pl payload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return connect.Records{}, connect.ErrBadRecord
	}
	var out connect.Records
	switch kind {
	case connect.KindSleep:
		if pl.Night == nil || len(pl.Night.Segments) == 0 || len(pl.Night.Segments) > connect.MaxRows {
			return out, connect.ErrBadRecord
		}
		switch pl.Night.State {
		case model.RecordProvisional, model.RecordFinal:
			out.NightState = pl.Night.State
		default:
			return out, connect.ErrBadRecord
		}
		for _, s := range pl.Night.Segments {
			stage, ok := stages[s.Stage]
			if !ok || !s.End.After(s.Start) {
				return out, connect.ErrBadRecord
			}
			out.Sleep = append(out.Sleep, model.SleepSegment{Start: s.Start, End: s.End, Stage: stage})
		}
	case connect.KindHRV:
		h := pl.HRV
		if h == nil || h.End.Before(h.Start) || h.RMSSDMs <= 0 || h.RMSSDMs > 500 {
			return out, connect.ErrBadRecord
		}
		out.Samples = []model.Sample{{Type: model.SampleHRVRMSSD, Start: h.Start, End: h.End, Value: h.RMSSDMs, Unit: "ms"}}
	case connect.KindPermissions:
		if len(pl.Permissions) > model.MaxProviderPermissions {
			return out, connect.ErrBadRecord
		}
		out.Permissions = append([]string{}, pl.Permissions...)
	case connect.KindDeregistration:
	default:
		return out, connect.ErrBadRecord
	}
	return out, nil
}

var _ connect.Provider = (*Provider)(nil)
