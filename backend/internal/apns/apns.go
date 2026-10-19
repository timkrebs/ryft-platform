// Package apns sends notifications through Apple Push Notification service
// with token-based authentication: an ES256 JWT from the team's .p8 key over
// HTTP/2 (net/http negotiates it). It is the small part of APNs RYFT uses
// (docs/DECISIONS.md D73): one request per device token, no certificates.
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/timkrebs/ryft/internal/apple"
)

// The two APNs environments; a token is valid in the one the app was built for.
const (
	Production = "production"
	Sandbox    = "sandbox"
)

var hosts = map[string]string{
	Production: "https://api.push.apple.com",
	Sandbox:    "https://api.sandbox.push.apple.com",
}

// Config is the developer-account side of APNs.
type Config struct {
	KeyID, TeamID string // the APNs key's 10-character ID, the team ID
	PrivateKey    string // the AuthKey_<key id>.p8, PEM or base64 PEM
	Topic         string // the app's bundle id
	// URLs replaces the hosts per environment (tests).
	URLs map[string]string
}

// Client sends notifications. Safe for concurrent use.
type Client struct {
	cfg  Config
	key  *ecdsa.PrivateKey
	http *http.Client
	now  func() time.Time

	mu       sync.Mutex
	bearer   string
	bearerAt time.Time
}

// New checks the config and parses the key. A nil client gets one with HTTP/2
// and a 10-second timeout.
func New(c Config, client *http.Client) (*Client, error) {
	if len(c.KeyID) != 10 || c.TeamID == "" || c.Topic == "" {
		return nil, errors.New("apns: APNS_KEY_ID (10 characters), APNS_TEAM_ID and APNS_TOPIC are required")
	}
	key, err := apple.ParseKey(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("apns: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			IdleConnTimeout:   5 * time.Minute,
		}}
	}
	return &Client{cfg: c, key: key, http: client, now: time.Now}, nil
}

// bearerEvery is how long a provider token is reused: Apple refuses tokens
// older than an hour and refreshing more often than every 20 minutes.
const bearerEvery = 40 * time.Minute

func (c *Client) token(refresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !refresh && c.bearer != "" && now.Sub(c.bearerAt) < bearerEvery {
		return c.bearer, nil
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": c.cfg.TeamID, "iat": now.Unix()})
	t.Header["kid"] = c.cfg.KeyID
	signed, err := t.SignedString(c.key)
	if err != nil {
		return "", err
	}
	c.bearer, c.bearerAt = signed, now
	return signed, nil
}

// Push types and priorities (apns-push-type, apns-priority).
const (
	PushAlert      = "alert"
	PushBackground = "background"
)

// Notification is one request to one device.
type Notification struct {
	Token      string // device token, hex
	Env        string // Production or Sandbox
	PushType   string // PushAlert or PushBackground
	Payload    []byte // the JSON body ({"aps": …})
	CollapseID string // replaces an undelivered earlier notification with the same id
	Expiration time.Time
}

// Result is APNs' answer.
type Result struct {
	Status int
	Reason string // e.g. BadDeviceToken, Unregistered (empty on 200)
	ID     string // apns-id
}

// OK says APNs accepted the notification.
func (r Result) OK() bool { return r.Status == http.StatusOK }

// Gone says the token will never work again: drop it (410, or a token that is
// malformed or not for this app).
func (r Result) Gone() bool {
	return r.Status == http.StatusGone ||
		(r.Status == http.StatusBadRequest && (r.Reason == "BadDeviceToken" || r.Reason == "DeviceTokenNotForTopic"))
}

// Send posts n. A transport failure is an error; APNs' refusals are a Result.
// An expired provider token is renewed and the request sent once more.
func (c *Client) Send(ctx context.Context, n Notification) (Result, error) {
	base, ok := c.cfg.URLs[n.Env]
	if !ok {
		if base, ok = hosts[n.Env]; !ok {
			return Result{}, fmt.Errorf("apns: unknown environment %q", n.Env)
		}
	}
	res, err := c.send(ctx, base, n, false)
	if err == nil && res.Status == http.StatusForbidden && res.Reason == "ExpiredProviderToken" {
		res, err = c.send(ctx, base, n, true)
	}
	return res, err
}

func (c *Client) send(ctx context.Context, base string, n Notification, refresh bool) (Result, error) {
	bearer, err := c.token(refresh)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+n.Token, bytes.NewReader(n.Payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", c.cfg.Topic)
	req.Header.Set("apns-push-type", n.PushType)
	priority := "10"
	if n.PushType == PushBackground {
		priority = "5" // Apple requires 5 for background pushes
	}
	req.Header.Set("apns-priority", priority)
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}
	if !n.Expiration.IsZero() {
		req.Header.Set("apns-expiration", strconv.FormatInt(n.Expiration.Unix(), 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("apns: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	res := Result{Status: resp.StatusCode, ID: resp.Header.Get("apns-id")}
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
		res.Reason = body.Reason
	}
	return res, nil
}
