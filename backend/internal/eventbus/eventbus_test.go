package eventbus

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/events/pubsub"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// The wiring's pieces fit: the Transport is the Bus an outbox relays into, and notify's
// outbox is the push handler's deferral sink.
var (
	_ events.Bus      = (*Transport)(nil)
	_ pubsub.Deferrer = (*outbox.Outbox)(nil)
)

type publish struct {
	topic string
	auth  string
	attrs map[string]string
	data  []byte
}

// emulator stands in for the Pub/Sub emulator's publish endpoint.
type emulator struct {
	mu  sync.Mutex
	got []publish
}

func (e *emulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	topic, ok := strings.CutPrefix(r.URL.Path, "/v1/projects/"+config.EmulatorProject+"/topics/")
	topic, publishes := strings.CutSuffix(topic, ":publish")
	var body struct {
		Messages []struct {
			Data       []byte            `json:"data"`
			Attributes map[string]string `json:"attributes"`
		} `json:"messages"`
	}
	if !ok || !publishes || r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 1 {
		http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	e.got = append(e.got, publish{topic, r.Header.Get("Authorization"), body.Messages[0].Attributes, body.Messages[0].Data})
	e.mu.Unlock()
	_, _ = w.Write([]byte(`{"messageIds":["1"]}`))
}

func (e *emulator) published() []publish {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]publish(nil), e.got...)
}

func local(t *testing.T) (*Transport, *emulator, *bytes.Buffer) {
	t.Helper()
	emu := &emulator{}
	srv := httptest.NewServer(emu)
	t.Cleanup(srv.Close)
	cfg := config.Config{
		PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: strings.TrimPrefix(srv.URL, "http://")},
		Push:   config.Push{AllowInsecure: true},
	}
	var logs bytes.Buffer
	tr, err := Open(t.Context(), cfg, "notify", slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return tr, emu, &logs
}

func envelope(key string, data []byte) string {
	b, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"data": base64.StdEncoding.EncodeToString(data), "messageId": "42",
			"attributes": map[string]string{pubsub.AttrKey: key, pubsub.AttrMessageID: "scored:x"},
		},
		"deliveryAttempt": 1, "subscription": "projects/ryft-local/subscriptions/notify-wait",
	})
	return string(b)
}

// waitRows stands in for notify's outbox.
type waitRows struct{ got []events.Message }

func (o *waitRows) SendAt(_ context.Context, m events.Message, _ time.Time) error {
	o.got = append(o.got, m)
	return nil
}

// Locally (the emulator, ALLOW_INSECURE_PUSH) the transport publishes by key without a
// token, says that pushes are unchecked, and its push, wait and tick routes work.
func TestOpenLocal(t *testing.T) {
	tr, emu, logs := local(t)
	for _, want := range []string{`"transport":"pubsub"`, `"push_auth":"insecure"`, "ALLOW_INSECURE_PUSH", "(emulator)"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the start-up log lacks %s:\n%s", want, logs)
		}
	}

	ev := model.ScoredEvent{UserID: uuid.New(), Day: "2026-11-14"}
	if err := tr.PublishScored(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	m, err := events.Scored(ev)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := tr.WaitSender()
	if err != nil {
		t.Fatal(err)
	}
	if err := wait.Send(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	got := emu.published()
	if len(got) != 2 || got[0].topic != pubsub.TopicScored || got[1].topic != pubsub.TopicNotifyWait {
		t.Fatalf("published %+v", got)
	}
	for _, p := range got {
		if p.auth != "" || p.attrs[pubsub.AttrKey] != events.KeyScored || p.attrs[pubsub.AttrMessageID] != m.ID || !bytes.Equal(p.data, m.Body) {
			t.Errorf("published %+v, want ryft-key scored, ryft-message-id %s, the body and no token", p, m.ID)
		}
	}

	// notify's push routes: /push/wait hands the waited event back under its key, a bad
	// one is dead-lettered with the transport's client, a Defer goes to the outbox.
	var handled []string
	box := &waitRows{}
	push, err := tr.Push(events.ConsumerNotify, events.NotifyHandler(func(_ context.Context, n events.NotifyMessage) error {
		handled = append(handled, n.Reason())
		if n.Refresh != nil {
			return events.Defer(time.Hour)
		}
		return nil
	}), box)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	push.Register(mux)
	refresh, err := events.Refresh(model.RefreshEvent{UserID: ev.UserID, Day: ev.Day})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, key string
		data      []byte
	}{
		{pubsub.PathWait, events.KeyScored, m.Body},
		{pubsub.PathWait, events.KeyRefresh, refresh.Body},
		{pubsub.PathWait, events.KeyIngested, m.Body},
		{"/push/scored", events.KeyScored, []byte(`{"user_id":`)},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(envelope(c.key, c.data))))
		if w.Code != http.StatusNoContent {
			t.Errorf("POST %s (%s): %d", c.path, c.key, w.Code)
		}
	}
	if strings.Join(handled, " ") != "scored refresh" || len(box.got) != 1 || box.got[0].Key != events.KeyRefresh {
		t.Errorf("handled %v, deferred %+v", handled, box.got)
	}
	got = emu.published()[2:]
	if len(got) != 2 || got[0].topic != pubsub.TopicDLQNotify || got[1].topic != pubsub.TopicDLQNotify ||
		got[0].attrs[pubsub.AttrKey] != "" || got[1].attrs[pubsub.AttrKey] != events.KeyScored {
		t.Errorf("dead letters %+v", got)
	}

	// The tick, counted under the transport's service.
	ran := false
	tick, err := tr.Tick(time.Second, nil, Tick{Name: "ping", Run: func(context.Context) error { ran = true; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle(http.MethodPost+" "+PathTick, tick)
	before := testutil.ToFloat64(metrics.Ticks.WithLabelValues("notify", "ok"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, PathTick, strings.NewReader(`{"message":{"data":"","messageId":"7"}}`)))
	if w.Code != http.StatusNoContent || !ran || testutil.ToFloat64(metrics.Ticks.WithLabelValues("notify", "ok")) != before+1 {
		t.Errorf("tick: %d, ran %v", w.Code, ran)
	}
	if tr.Verifier() == nil {
		t.Error("no verifier")
	}
}

func TestOpenRefusesABadClient(t *testing.T) {
	cfg := config.Config{Push: config.Push{AllowInsecure: true}} // no project, no emulator
	if _, err := Open(t.Context(), cfg, "api", slog.New(slog.DiscardHandler)); err == nil {
		t.Error("opened without a project")
	}
}
