package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/model"
)

// Half an APNs key ends the process before anything connects, under the message it
// always had (the database is a closed port: a check behind db.Open would be
// outlasted by the start-up retries). The child gets only this environment: compose's
// local Pub/Sub settings (an emulator that is not there, as nothing is published).
func TestMainRefusesHalfAnAPNsKeyBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesHalfAnAPNsKeyBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://notify@127.0.0.1:1/ryft", "APNS_KEY_ID=ABC1234567",
		"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"apns config"`) ||
		!strings.Contains(string(out), `"service":"notify"`) {
		t.Fatalf("want exit 1 with the apns config error, got %v:\n%s", err, out)
	}
}

// transport is a real transport with compose's local settings: it dials nothing, and
// its push routes check no token.
func transport(t *testing.T, log *slog.Logger) *eventbus.Transport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tr, err := eventbus.Open(ctx, config.Config{
		PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
		Push:   config.Push{AllowInsecure: true},
	}, "notify", log)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func deps(t *testing.T, log *slog.Logger) app.Deps {
	return app.Deps{Log: log, Bus: transport(t, log), Outbox: outbox.New(nil, "notify", nil, log)}
}

// Without an APNs key notify still builds: it warns that nothing is sent, and hands app
// its push routes (its four paths, nothing else) and the stale check as its tick step.
func TestBuildWithoutAKeyRunsAndCountsWhatItWouldSend(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	parts, err := build(deps(t, log), pushSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Push) != 1 || len(parts.Ticks) != 1 || parts.Ticks[0].Name != "stale check" {
		t.Fatalf("push %d, ticks %+v: want one push handling and the stale check", len(parts.Push), parts.Ticks)
	}
	if parts.Routes != nil || parts.Checks != nil {
		t.Fatal("notify serves no /v1, and app adds the db check")
	}
	mux := http.NewServeMux()
	parts.Push[0].Register(mux)
	for path, want := range map[string]int{
		"/push/scored": http.StatusBadRequest, "/push/session.scored": http.StatusBadRequest, // routed: not an envelope
		"/push/refresh": http.StatusBadRequest, "/push/wait": http.StatusBadRequest,
		"/push/ingested": http.StatusNotFound, "/push/session.completed": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("not json")))
		if rec.Code != want {
			t.Errorf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	for _, want := range []string{`"msg":"consuming scored events"`, "APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8 not set"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("runbooks grep for %q: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "apns configured") {
		t.Fatal("no key, no 'apns configured'")
	}
}

// A key that does not parse is a build error under the message "apns", as it was.
func TestBuildRefusesAKeyThatDoesNotParse(t *testing.T) {
	push := pushSettings{on: true, cfg: config.APNs{KeyID: "ABC1234567", TeamID: "DEF1234567", PrivateKey: "not a key", Topic: config.DefaultAPNsTopic}}
	_, err := build(deps(t, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))), push)
	var ce app.ConfigError
	if !errors.As(err, &ce) || ce.Msg != "apns" {
		t.Fatalf("want a ConfigError under \"apns\", got %v", err)
	}
}

// Without the wait box every deferral would be a nack; without the transport there are
// no push routes: build refuses both.
func TestBuildNeedsTheWaitBoxAndTheTransport(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	for want, d := range map[string]app.Deps{
		"wait box":  {Log: log, Bus: transport(t, log)},
		"transport": {Log: log, Outbox: outbox.New(nil, "notify", nil, log)},
	} {
		if _, err := build(d, pushSettings{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("build without the %s = %v", want, err)
		}
	}
}

// The Spec decides how app wires the service: notify's outbox is its wait box (into
// ryft-notify-wait, swept by the tick), and its tick keeps the default 45 s.
func TestSpecFlags(t *testing.T) {
	s := spec()
	if s.Name != "notify" || s.Outbox != app.WaitOutbox || s.TickBudget != 0 || s.Edge {
		t.Fatalf("Name %q, Outbox %v, TickBudget %v, Edge %v: want notify, the wait box, the default, false", s.Name, s.Outbox, s.TickBudget, s.Edge)
	}
	if s.Load == nil || s.Build == nil {
		t.Fatal("notify needs its Load and its Build")
	}
}

// Load reads the APNs key: none is fine, a half is a ConfigError under "apns config".
func TestSpecLoadReadsTheAPNsKey(t *testing.T) {
	for _, k := range []string{"APNS_KEY_ID", "APNS_TEAM_ID", "APNS_KEY_P8"} {
		t.Setenv(k, "")
	}
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if err := spec().Load(log); err != nil {
		t.Fatalf("no key is a valid setting: %v", err)
	}
	t.Setenv("APNS_KEY_ID", "ABC1234567")
	var ce app.ConfigError
	if err := spec().Load(log); !errors.As(err, &ce) || ce.Msg != "apns config" {
		t.Fatalf("Load = %v, want a ConfigError under \"apns config\"", err)
	}
}

// With a key that parses the service sends: APNs is set. (A mutant that builds the
// client and drops it would leave notify counting "unconfigured" in production.)
func TestNewServiceWithAKeySends(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	var logs bytes.Buffer
	d := app.Deps{Log: slog.New(slog.NewJSONHandler(&logs, nil))}

	svc, err := newService(d, pushSettings{on: true, cfg: config.APNs{KeyID: "ABC1234567", TeamID: "DEF1234567", PrivateKey: p8, Topic: config.DefaultAPNsTopic}})
	if err != nil {
		t.Fatal(err)
	}
	if svc.APNs == nil || !strings.Contains(logs.String(), `"msg":"apns configured"`) {
		t.Fatalf("APNs %v, log %s: a configured key sends", svc.APNs, logs.String())
	}
	if svc, err = newService(d, pushSettings{}); err != nil || svc.APNs != nil {
		t.Fatalf("no key: APNs %v, err %v: nothing is sent", svc.APNs, err)
	}
}

func TestFieldsOfTheEvent(t *testing.T) {
	user := uuid.New()
	for _, tc := range []struct {
		name string
		m    events.NotifyMessage
	}{
		{"scored", events.NotifyMessage{Scored: &model.ScoredEvent{UserID: user, RequestID: "r1"}}},
		{"refresh", events.NotifyMessage{Refresh: &model.RefreshEvent{UserID: user, RequestID: "r1"}}},
		{"session scored", events.NotifyMessage{SessionScored: &model.SessionScoredEvent{UserID: user, RequestID: "r1"}}},
	} {
		if f := fieldsOf(tc.m); f.UserID != user.String() || f.RequestID != "r1" {
			t.Errorf("%s: %+v", tc.name, f)
		}
	}
	if f := fieldsOf(events.NotifyMessage{}); f.UserID != "" || f.RequestID != "" {
		t.Errorf("an empty message: %+v", f)
	}
}

// box is a wait box that records what it was asked to hold.
type box struct {
	mu   sync.Mutex
	got  []events.Message
	at   []time.Time
	fail error
}

func (b *box) SendAt(_ context.Context, m events.Message, at time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.got, b.at = append(b.got, m), append(b.at, at)
	return nil
}

// envelope is a push of data under path with these attributes.
func envelope(t *testing.T, data []byte, attrs map[string]string) string {
	t.Helper()
	a, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"message":{"data":%q,"attributes":%s,"messageId":"901"},"subscription":"projects/ryft-local/subscriptions/notify-x","deliveryAttempt":1}`,
		base64.StdEncoding.EncodeToString(data), a)
}

// A deferral lands in the wait box with the event's own key, ryft-message-id, body and
// reason, due after the handler's delay; the push is answered 204 (acked: the box has
// it). When the box cannot take it the push is a nack. Once due, the sweep publishes it
// to ryft-notify-wait and /push/wait hands it back to the handler under that key.
func TestADeferLandsInTheWaitBox(t *testing.T) {
	scored, err := json.Marshal(model.ScoredEvent{UserID: uuid.New(), Day: "2026-11-15"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		fail error
		code int
	}{
		{"stored: acked", nil, http.StatusNoContent},
		{"the box fails: nacked", errors.New("insert failed"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			b := &box{fail: tc.fail}
			var handled []events.NotifyMessage
			var mu sync.Mutex
			routes, err := pushRoutes(transport(t, log), func(_ context.Context, m events.NotifyMessage) error {
				mu.Lock()
				defer mu.Unlock()
				handled = append(handled, m)
				if len(handled) == 1 {
					return events.Defer(2 * time.Hour) // quiet hours
				}
				return nil
			}, b)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			routes.Register(mux)

			before := time.Now()
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/push/scored",
				strings.NewReader(envelope(t, scored, map[string]string{"ryft-key": "scored", "ryft-message-id": "scored:u1:2026-11-15"}))))
			after := time.Now()
			if rec.Code != tc.code {
				t.Fatalf("the deferred push = %d, want %d", rec.Code, tc.code)
			}
			if tc.fail != nil {
				return
			}
			if len(b.got) != 1 {
				t.Fatalf("the box holds %d messages", len(b.got))
			}
			m := b.got[0]
			if m.Key != events.KeyScored || m.ID != "scored:u1:2026-11-15" || !bytes.Equal(m.Body, scored) || m.Reason != "scored" {
				t.Errorf("held %+v: want the original key, ID, body and reason", m)
			}
			if at := b.at[0]; at.Before(before.Add(2*time.Hour)) || at.After(after.Add(2*time.Hour)) {
				t.Errorf("due at %v, want two hours after the push", at)
			}

			// The sweep relays the row to ryft-notify-wait with ryft-key = its key: back it comes.
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/push/wait",
				strings.NewReader(envelope(t, m.Body, map[string]string{"ryft-key": m.Key, "ryft-message-id": m.ID}))))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("/push/wait = %d", rec.Code)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(handled) != 2 || handled[1].Scored == nil || handled[1].Scored.Day != "2026-11-15" {
				t.Errorf("the handler got %+v: the second is the deferred scored event", handled)
			}
		})
	}
}
