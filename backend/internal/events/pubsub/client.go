package pubsub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/events"
)

// MetadataTokenURL is where a Cloud Run instance gets its service account's access
// token (header Metadata-Flavor: Google).
const MetadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" //nolint:gosec // a URL, not a credential

// tokenMargin is how long before its expiry a token is fetched again.
const tokenMargin = time.Minute

// maxResponse bounds what is read of an answer (Pub/Sub's, the metadata server's).
const maxResponse = 64 << 10

// Options configure a Client.
type Options struct {
	Project string // GCP_PROJECT
	// Endpoint is Pub/Sub's origin (PUBSUB_ENDPOINT): the regional one, so one user's
	// ordered messages are all published in one region.
	Endpoint string
	// EmulatorHost (PUBSUB_EMULATOR_HOST, host:port) replaces Endpoint with plain http
	// to the emulator and sends no token.
	EmulatorHost string
	// TokenURL is the metadata server's token URL; default MetadataTokenURL.
	TokenURL string
	// HTTPClient sends the publishes and the token requests; default one with a 10 s
	// timeout.
	HTTPClient *http.Client
	// Timeout bounds one Publish, its token fetch and its one retry included (default 10 s).
	Timeout time.Duration
}

// Client publishes to Pub/Sub over REST (POST .../topics/<t>:publish), stdlib only. It
// holds no connection of its own and is safe for concurrent use.
type Client struct {
	base    string // .../v1/projects/<p>
	http    *http.Client
	timeout time.Duration
	tokens  *tokenSource // nil with the emulator
}

// NewClient is a Client for o.
func NewClient(o Options) (*Client, error) {
	if o.Project == "" {
		return nil, errors.New("pubsub: a project is required")
	}
	origin := strings.TrimRight(o.Endpoint, "/")
	if o.EmulatorHost != "" {
		origin = "http://" + o.EmulatorHost
	}
	if u, err := url.Parse(origin); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("pubsub: endpoint %q is not an http(s) origin", origin)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	c := &Client{base: origin + "/v1/projects/" + url.PathEscape(o.Project), http: o.HTTPClient, timeout: o.Timeout}
	if o.EmulatorHost == "" {
		if o.TokenURL == "" {
			o.TokenURL = MetadataTokenURL
		}
		c.tokens = &tokenSource{url: o.TokenURL, http: o.HTTPClient, now: time.Now}
	}
	return c, nil
}

// Message is one Pub/Sub message to publish.
type Message struct {
	Data        []byte
	Attributes  map[string]string
	OrderingKey string // "" for none
}

type publishRequest struct {
	Messages []publishMessage `json:"messages"`
}

type publishMessage struct {
	Data        string            `json:"data"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	OrderingKey string            `json:"orderingKey,omitempty"`
}

// rejectedStatus are the answers to a publish that no retry can change: the request
// itself is refused (400 INVALID_ARGUMENT: an attribute or the message over Pub/Sub's
// limits; 413), the account may not publish there (403), or the topic does not exist
// (404). A publish that gets one is events.Rejected.
var rejectedStatus = map[int]bool{
	http.StatusBadRequest: true, http.StatusForbidden: true, http.StatusNotFound: true, http.StatusRequestEntityTooLarge: true,
}

// Publish sends m to topic and returns the message ID Pub/Sub gave it. A 401 fetches a
// new token and tries once more; nothing else is retried here (the outbox retries a
// failed event, Pub/Sub a nacked push). An answer in rejectedStatus is events.Rejected,
// any other failure transient. Its errors hold the topic and Google's status, never the
// data or the token.
func (c *Client) Publish(ctx context.Context, topic string, m Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	body, err := json.Marshal(publishRequest{Messages: []publishMessage{{
		Data: base64.StdEncoding.EncodeToString(m.Data), Attributes: m.Attributes, OrderingKey: m.OrderingKey,
	}}})
	if err != nil {
		return "", events.Rejected(fmt.Errorf("pubsub: publish to %s: %w", topic, err))
	}
	u := c.base + "/topics/" + url.PathEscape(topic) + ":publish"
	for try := 0; ; try++ {
		token := ""
		if c.tokens != nil {
			if token, err = c.tokens.get(ctx); err != nil {
				return "", fmt.Errorf("pubsub: publish to %s: %w", topic, err)
			}
		}
		id, status, err := c.post(ctx, u, token, body)
		if status == http.StatusUnauthorized && c.tokens != nil && try == 0 {
			c.tokens.invalidate(token)
			continue
		}
		if err != nil {
			err = fmt.Errorf("pubsub: publish to %s: %w", topic, err)
			if rejectedStatus[status] {
				err = events.Rejected(err)
			}
			return "", err
		}
		return id, nil
	}
}

// post sends one publish request; status is the HTTP status (0 without an answer).
func (c *Client) post(ctx context.Context, u, token string, body []byte) (string, int, error) {
	// u is built from the configured origin (config: https://*.googleapis.com only, or the
	// emulator, which is refused on Cloud Run) and the topology's topic names.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body)) //nolint:gosec // u is the configured Pub/Sub origin, see above
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req) //nolint:gosec // u is the configured Pub/Sub origin, see above
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("reading the answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("HTTP %d%s", resp.StatusCode, googleError(raw))
	}
	var out struct {
		MessageIDs []string `json:"messageIds"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.MessageIDs) != 1 {
		return "", resp.StatusCode, errors.New("the answer holds no message id")
	}
	return out.MessageIDs[0], resp.StatusCode, nil
}

// googleError is ": STATUS: message" from a Google API error body, bounded; "" when the
// body is not one.
func googleError(raw []byte) string {
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || (e.Error.Status == "" && e.Error.Message == "") {
		return ""
	}
	return ": " + clip(e.Error.Status, 64) + ": " + clip(e.Error.Message, 200)
}

// tokenSource fetches the service account's access token from the metadata server and
// keeps it until tokenMargin before it expires. One fetch at a time: callers wait for it.
type tokenSource struct {
	url  string
	http *http.Client
	now  func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (s *tokenSource) get(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Add(tokenMargin).Before(s.expiry) {
		return s.token, nil
	}
	tok, ttl, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token, s.expiry = tok, s.now().Add(ttl)
	return tok, nil
}

// invalidate drops token if it is still the cached one (Pub/Sub refused it).
func (s *tokenSource) invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token = ""
	}
}

func (s *tokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil) //nolint:gosec // s.url is MetadataTokenURL (a test's server in tests)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := s.http.Do(req) //nolint:gosec // s.url is MetadataTokenURL (a test's server in tests)
	if err != nil {
		return "", 0, fmt.Errorf("access token from the metadata server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("access token from the metadata server: HTTP %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&t); err != nil {
		return "", 0, errors.New("access token from the metadata server: the answer is not a token")
	}
	if t.AccessToken == "" || t.ExpiresIn <= 0 || !strings.EqualFold(t.TokenType, "Bearer") {
		return "", 0, errors.New("access token from the metadata server: no bearer token with an expiry")
	}
	return t.AccessToken, time.Duration(t.ExpiresIn) * time.Second, nil
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
