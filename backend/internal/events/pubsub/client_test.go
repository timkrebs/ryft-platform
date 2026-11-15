package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// published is one publish request as fakePubSub received it.
type published struct {
	path, authorization string
	data                []byte
	attributes          map[string]string
	orderingKey         string
}

// fakePubSub stands in for Pub/Sub's REST publish and the metadata server's token
// endpoint. Tokens are made per run ("test-token-<n>"); a token in refuse is answered
// 401, as an expired or revoked one would be.
type fakePubSub struct {
	*httptest.Server
	mu        sync.Mutex
	got       []published
	fetches   atomic.Int32
	ttl       int // expires_in of the next token
	refuse    map[string]bool
	failTopic string // answered 404 NOT_FOUND
}

func newFakePubSub(t *testing.T) *fakePubSub {
	t.Helper()
	f := &fakePubSub{ttl: 3600, refuse: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		n := f.fetches.Add(1)
		f.mu.Lock()
		ttl := f.ttl
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token-" + strconv.Itoa(int(n)), "expires_in": ttl, "token_type": "Bearer"})
	})
	mux.HandleFunc("POST /v1/projects/{project}/topics/{topic}", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		f.mu.Lock()
		refused, failTopic := f.refuse[strings.TrimPrefix(auth, "Bearer ")], f.failTopic
		f.mu.Unlock()
		if refused {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`))
			return
		}
		if strings.TrimSuffix(r.PathValue("topic"), ":publish") == failTopic {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Resource not found (resource=` + failTopic + `).","status":"NOT_FOUND"}}`))
			return
		}
		var req struct {
			Messages []struct {
				Data        string            `json:"data"`
				Attributes  map[string]string `json:"attributes"`
				OrderingKey string            `json:"orderingKey"`
			} `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &req); err != nil || len(req.Messages) != 1 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data, err := base64.StdEncoding.DecodeString(req.Messages[0].Data)
		if err != nil {
			http.Error(w, "data is not base64", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.got = append(f.got, published{path: r.URL.Path, authorization: auth, data: data,
			attributes: req.Messages[0].Attributes, orderingKey: req.Messages[0].OrderingKey})
		n := len(f.got)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"messageIds": []string{strconv.Itoa(1000 + n)}})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakePubSub) received() []published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]published(nil), f.got...)
}

func (f *fakePubSub) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(Options{Project: "ryft-test", Endpoint: f.URL, TokenURL: f.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBusPublishesEveryKeyToItsTopic(t *testing.T) {
	f := newFakePubSub(t)
	bus := NewBus(f.client(t))
	ctx := context.Background()
	user, session := uuid.New(), uuid.New()
	before := testutil.ToFloat64(metrics.EventsPublished.WithLabelValues("sync", "ok"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(bus.PublishIngested(ctx, model.IngestedEvent{UserID: user, Days: []string{"2026-11-14"}, Reason: "sync"}, ""))
	must(bus.PublishSessionCompleted(ctx, model.SessionCompletedEvent{UserID: user, SessionID: session, Day: "2026-11-13"}, "session:1"))
	must(bus.PublishScored(ctx, model.ScoredEvent{UserID: user, Day: "2026-11-14"}))
	must(bus.PublishSessionScored(ctx, model.SessionScoredEvent{UserID: user, SessionID: session, Day: "2026-11-13"}))
	must(bus.PublishRefresh(ctx, model.RefreshEvent{UserID: user, Day: "2026-11-14"}))

	got := f.received()
	if len(got) != 5 {
		t.Fatalf("%d publishes", len(got))
	}
	for i, want := range []struct {
		topic, key, id, reason, ordering string
	}{
		{TopicIngested, events.KeyIngested, "", "sync", user.String()},
		{TopicSessionCompleted, events.KeySessionCompleted, "session:1", events.ReasonSessionCompleted, user.String()},
		{TopicScored, events.KeyScored, "scored:" + user.String() + ":2026-11-14", "scored", ""},
		{TopicSessionScored, events.KeySessionScored, "session-scored:" + session.String(), "session_scored", ""},
		{TopicRefresh, events.KeyRefresh, "refresh:" + user.String() + ":2026-11-14", "refresh", ""},
	} {
		p := got[i]
		if p.path != "/v1/projects/ryft-test/topics/"+want.topic+":publish" {
			t.Errorf("%s: path %s", want.key, p.path)
		}
		wantAttrs := map[string]string{AttrKey: want.key, AttrReason: want.reason}
		if want.id != "" {
			wantAttrs[AttrMessageID] = want.id
		}
		if len(p.attributes) != len(wantAttrs) {
			t.Errorf("%s: attributes %v, want %v", want.key, p.attributes, wantAttrs)
		}
		for k, v := range wantAttrs {
			if p.attributes[k] != v {
				t.Errorf("%s: %s = %q, want %q", want.key, k, p.attributes[k], v)
			}
		}
		if p.orderingKey != want.ordering {
			t.Errorf("%s: ordering key %q, want %q (only scoring's two topics are ordered)", want.key, p.orderingKey, want.ordering)
		}
		if p.authorization != "Bearer test-token-1" {
			t.Errorf("%s: authorization %q: the first token, cached", want.key, p.authorization)
		}
	}
	// The data is the event's JSON exactly: what DecodeIngested reads on the other side.
	ev, err := events.DecodeIngested(events.KeyIngested, got[0].data)
	if err != nil || ev.UserID != user || ev.Reason != "sync" {
		t.Fatalf("data: %+v %v", ev, err)
	}
	if n := f.fetches.Load(); n != 1 {
		t.Fatalf("%d token fetches for five publishes, want 1", n)
	}
	if n := testutil.ToFloat64(metrics.EventsPublished.WithLabelValues("sync", "ok")) - before; n != 1 {
		t.Fatalf("ryft_events_published_total{sync,ok} rose by %v", n)
	}
}

func TestBusRefusesWhatHasNoTopic(t *testing.T) {
	f := newFakePubSub(t)
	bus := NewBus(f.client(t))
	before := testutil.ToFloat64(metrics.EventsPublished.WithLabelValues("x", "error"))
	err := bus.Send(context.Background(), events.Message{Key: "tick", Body: []byte(`{}`), Reason: "x"})
	if !errors.Is(err, ErrNoTopic) || !events.IsRejected(err) {
		t.Fatalf("got %v: want ErrNoTopic, rejected for good", err)
	}
	// An ordered topic needs a user to order by.
	err = bus.Send(context.Background(), events.Message{Key: events.KeyIngested, Body: []byte(`{"days":["2026-11-14"]}`), Reason: "x"})
	if err == nil || !strings.Contains(err.Error(), "user_id") || !events.IsRejected(err) {
		t.Fatalf("got %v: want the user_id, rejected for good", err)
	}
	if len(f.received()) != 0 {
		t.Fatal("published anyway")
	}
	if n := testutil.ToFloat64(metrics.EventsPublished.WithLabelValues("x", "error")) - before; n != 2 {
		t.Fatalf("errors counted %v, want 2", n)
	}
}

// Pub/Sub's error comes back with its status, never the data.
func TestPublishError(t *testing.T) {
	f := newFakePubSub(t)
	f.mu.Lock()
	f.failTopic = TopicScored
	f.mu.Unlock()
	err := NewBus(f.client(t)).Send(context.Background(), events.Message{Key: events.KeyScored, Body: []byte(`{"recovery":61.7}`), Reason: "scored"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404: NOT_FOUND") || strings.Contains(err.Error(), "61.7") || !events.IsRejected(err) {
		t.Fatalf("got %v", err)
	}
}

// What Pub/Sub refuses for good is events.Rejected (the outbox sets the row aside at
// once); an outage, a quota or a token it keeps refusing is transient (the row waits).
func TestPublishRejectsForGood(t *testing.T) {
	for status, rejected := range map[int]bool{
		http.StatusBadRequest: true, http.StatusForbidden: true, http.StatusNotFound: true, http.StatusRequestEntityTooLarge: true,
		http.StatusUnauthorized: false, http.StatusTooManyRequests: false, http.StatusInternalServerError: false,
		http.StatusBadGateway: false, http.StatusServiceUnavailable: false, http.StatusGatewayTimeout: false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		c, err := NewClient(Options{Project: "ryft-local", EmulatorHost: strings.TrimPrefix(srv.URL, "http://")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Publish(context.Background(), TopicScored, Message{Data: []byte(`{}`)})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) || events.IsRejected(err) != rejected {
			t.Errorf("HTTP %d: %v, rejected %v, want %v", status, err, events.IsRejected(err), rejected)
		}
	}
	// No answer at all is transient.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	c, err := NewClient(Options{Project: "ryft-local", EmulatorHost: strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(context.Background(), TopicScored, Message{}); err == nil || events.IsRejected(err) {
		t.Fatalf("unreachable: %v, want a transient error", err)
	}
}

// A fixed-topic sender keeps each message's key as ryft-key: notify's deferrals go to
// ryft-notify-wait whatever their key.
func TestToFixedTopic(t *testing.T) {
	f := newFakePubSub(t)
	c := f.client(t)
	wait, err := c.To(TopicNotifyWait)
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	m, _ := events.Scored(model.ScoredEvent{UserID: user, Day: "2026-11-14"})
	if err := wait.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := f.received()
	if len(got) != 1 || got[0].path != "/v1/projects/ryft-test/topics/"+TopicNotifyWait+":publish" ||
		got[0].attributes[AttrKey] != events.KeyScored || got[0].attributes[AttrMessageID] != m.ID || got[0].orderingKey != "" {
		t.Fatalf("%+v", got)
	}
	if _, err := c.To("ryft-elsewhere"); err == nil {
		t.Fatal("a topic outside the topology is refused")
	}
}

// The token is cached until shortly before it expires, and a 401 fetches a new one and
// tries once more.
func TestTokenCacheAndRefetch(t *testing.T) {
	f := newFakePubSub(t)
	c := f.client(t)
	now := time.Now()
	c.tokens.now = func() time.Time { return now }
	send := func() error {
		_, err := c.Publish(context.Background(), TopicScored, Message{Data: []byte(`{}`)})
		return err
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if err := send(); err != nil || f.fetches.Load() != 1 {
		t.Fatalf("cached: %v, %d fetches", err, f.fetches.Load())
	}
	now = now.Add(time.Hour - tokenMargin + time.Second) // within the margin of its expiry
	if err := send(); err != nil || f.fetches.Load() != 2 {
		t.Fatalf("near expiry: %v, %d fetches", err, f.fetches.Load())
	}
	// Pub/Sub refuses the cached token: one new fetch, and the publish goes through.
	f.mu.Lock()
	f.refuse["test-token-2"] = true
	f.mu.Unlock()
	if err := send(); err != nil || f.fetches.Load() != 3 {
		t.Fatalf("after a 401: %v, %d fetches", err, f.fetches.Load())
	}
	got := f.received()
	if last := got[len(got)-1]; last.authorization != "Bearer test-token-3" {
		t.Fatalf("published with %q", last.authorization)
	}
	// Refused again: one retry only, then the error.
	f.mu.Lock()
	f.refuse["test-token-3"], f.refuse["test-token-4"] = true, true
	f.mu.Unlock()
	err := send()
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || f.fetches.Load() != 4 {
		t.Fatalf("refused twice: %v, %d fetches", err, f.fetches.Load())
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatalf("the error holds the token: %v", err)
	}
}

func TestTokenFetchFailures(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"not JSON": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"no expiry": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer"}`))
		},
		"not bearer": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"access_token":"x","expires_in":60,"token_type":"Mac"}`))
		},
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"expires_in":60,"token_type":"Bearer"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			c, err := NewClient(Options{Project: "ryft-test", Endpoint: srv.URL, TokenURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Publish(context.Background(), TopicScored, Message{Data: []byte(`{}`)}); err == nil ||
				!strings.Contains(err.Error(), "metadata server") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// With the emulator: plain http to its host, no token, no metadata server.
func TestEmulatorMode(t *testing.T) {
	f := newFakePubSub(t)
	c, err := NewClient(Options{Project: "ryft-local", Endpoint: "https://unused.example", EmulatorHost: strings.TrimPrefix(f.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewBus(c).Send(context.Background(), events.Message{Key: events.KeyRefresh, Body: []byte(`{}`), Reason: "refresh"}); err != nil {
		t.Fatal(err)
	}
	got := f.received()
	if len(got) != 1 || got[0].authorization != "" || got[0].path != "/v1/projects/ryft-local/topics/"+TopicRefresh+":publish" {
		t.Fatalf("%+v", got)
	}
	if f.fetches.Load() != 0 {
		t.Fatal("the emulator needs no token")
	}
}

func TestPublishTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c, err := NewClient(Options{Project: "ryft-local", EmulatorHost: strings.TrimPrefix(srv.URL, "http://"), Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.Publish(context.Background(), TopicScored, Message{}); err == nil {
		t.Fatal("no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestNewClientRefuses(t *testing.T) {
	for name, o := range map[string]Options{
		"no project":   {Endpoint: "https://pubsub.googleapis.com"},
		"no endpoint":  {Project: "p"},
		"not a URL":    {Project: "p", Endpoint: "pubsub.googleapis.com"},
		"another kind": {Project: "p", Endpoint: "ftp://pubsub.googleapis.com"},
	} {
		if _, err := NewClient(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip(strings.Repeat("ä", 300), MaxErrorBytes); len(got) > MaxErrorBytes || !strings.HasPrefix(strings.Repeat("ä", 300), got) || len(got) != 512 {
		t.Fatalf("%d bytes", len(got))
	}
	if got := clip("a"+strings.Repeat("ä", 300), MaxErrorBytes); len(got) != 511 {
		t.Fatalf("cut inside a rune: %d bytes", len(got))
	}
	if clip("short", 512) != "short" {
		t.Fatal("short strings stay")
	}
}
