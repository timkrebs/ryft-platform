package pubsub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// rise reads a counter now and returns how far it has risen since.
func rise(c prometheus.Collector) func() float64 {
	before := testutil.ToFloat64(c)
	return func() float64 { return testutil.ToFloat64(c) - before }
}

type fakeVerifier struct{ err error }

func (f fakeVerifier) VerifyPush(*http.Request) error { return f.err }

type letter struct {
	topic string
	m     Message
}

// fakeDLQ records the dead letters a push handler publishes, or fails them.
type fakeDLQ struct {
	mu  sync.Mutex
	got []letter
	err error
}

func (f *fakeDLQ) Publish(_ context.Context, topic string, m Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.got = append(f.got, letter{topic, m})
	return "dlq-1", nil
}

type deferral struct {
	m  events.Message
	at time.Time
}

// fakeDeferrer stands in for notify's outbox (SendAt).
type fakeDeferrer struct {
	got []deferral
	err error
}

func (f *fakeDeferrer) SendAt(_ context.Context, m events.Message, at time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, deferral{m, at})
	return nil
}

// pushBody is a push request body in Pub/Sub's wrapped format, with both spellings of
// messageId and publishTime, as Pub/Sub sends them. A nil attempt leaves deliveryAttempt
// out (what a subscription without a dead-letter policy sends).
func pushBody(sub string, data []byte, attrs map[string]string, attempt *int) string {
	msg := map[string]any{
		"data":      base64.StdEncoding.EncodeToString(data),
		"messageId": "17012345678901234", "message_id": "17012345678901234",
		"publishTime": "2026-11-14T10:00:00.123Z", "publish_time": "2026-11-14T10:00:00.123Z",
	}
	if attrs != nil {
		msg["attributes"] = attrs
	}
	env := map[string]any{"message": msg, "subscription": "projects/ryft-test/subscriptions/" + sub}
	if attempt != nil {
		env["deliveryAttempt"] = *attempt
	}
	b, err := json.Marshal(env)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func attempt(i int) *int { return &i }

func serve(h http.Handler, method, body string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/push", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func must(m events.Message, err error) events.Message {
	if err != nil {
		panic(err)
	}
	return m
}

// attrsOf are the attributes the Bus publishes m with, changed by kv (key, value; a
// value of "-" removes the key).
func attrsOf(m events.Message, kv ...string) map[string]string {
	a := map[string]string{AttrKey: m.Key, AttrMessageID: m.ID, AttrReason: m.Reason}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "-" {
			delete(a, kv[i])
			continue
		}
		a[kv[i]] = kv[i+1]
	}
	return a
}

func scoringReturns(err error) events.Handler {
	return events.ScoringHandler(func(context.Context, model.IngestedEvent) error { return err })
}

func notifyReturns(err error) events.Handler {
	return events.NotifyHandler(func(context.Context, events.NotifyMessage) error { return err })
}

func consumerOf(path string) string {
	if slices.Contains(PushPaths(ServiceScoring), path) {
		return events.ConsumerScoring
	}
	return events.ConsumerNotify
}

// TestPushStatusMapping is the push handler's contract with Pub/Sub: which delivery is
// acked (204) or nacked (500), what is dead-lettered and deferred, and what is counted.
func TestPushStatusMapping(t *testing.T) {
	user := uuid.New()
	ingested := must(events.Ingested(model.IngestedEvent{UserID: user, Days: []string{"2026-11-13"}, Reason: "sync"}, "ingest:"+uuid.NewString()))
	completed := must(events.SessionCompleted(model.SessionCompletedEvent{UserID: user, SessionID: uuid.New(), Day: "2026-11-13"}, "session:"+uuid.NewString()))
	scored := must(events.Scored(model.ScoredEvent{UserID: user, Day: "2026-11-13"}))
	refresh := must(events.Refresh(model.RefreshEvent{UserID: user, Day: "2026-11-13"}))
	notJSON := []byte(`{"user_id":`)
	boom := errors.New("boom")
	now := time.Date(2026, 11, 14, 22, 30, 0, 0, time.UTC)

	cases := []struct {
		name     string
		path     string
		data     []byte
		attrs    map[string]string
		attempt  *int
		fn       events.Handler // nil: the consumer's typed handler, succeeding
		sink     string         // "": no Deferrer; "ok"; "fail"
		dlqFails bool

		want        int
		reason      string // ryft_events_handled_total{reason,result} rises by one
		result      string
		key         string // the key the handler ran with; "": it did not run
		redelivered bool
		letter      map[string]string // the dead letter's attributes but ryft-error; nil: none
		errHas      string            // in the dead letter's ryft-error
		dlq         string            // ryft_events_dlq_total{subject,dlq} rises by one; "": none
		deferred    time.Duration
		panics      bool
	}{
		{name: "ok", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			want: 204, reason: "sync", result: "ok", key: "ingested"},
		{name: "ok without attributes", path: "/push/ingested", data: ingested.Body,
			want: 204, reason: "sync", result: "ok", key: "ingested"},
		{name: "ok without ryft-key", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested, AttrKey, "-"), attempt: attempt(1),
			want: 204, reason: "sync", result: "ok", key: "ingested"},
		{name: "ok session.completed", path: "/push/session.completed", data: completed.Body, attrs: attrsOf(completed), attempt: attempt(1),
			want: 204, reason: events.ReasonSessionCompleted, result: "ok", key: "session.completed"},
		{name: "ok on notify", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			want: 204, reason: "scored", result: "ok", key: "scored"},
		{name: "permanent", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			fn:   scoringReturns(events.Permanent(errors.New("user deleted"))),
			want: 204, reason: "sync", result: "dropped", key: "ingested"},
		{name: "bad JSON", path: "/push/ingested", data: notJSON, attrs: attrsOf(ingested), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad", key: "ingested",
			letter: map[string]string{AttrKey: "ingested", AttrMessageID: ingested.ID, AttrReason: "sync"}, dlq: "ingested ok"},
		{name: "bad JSON, dead-letter publish fails", path: "/push/ingested", data: notJSON, attrs: attrsOf(ingested), attempt: attempt(1),
			dlqFails: true,
			want:     500, reason: events.ReasonUnknown, result: "bad", key: "ingested", dlq: "ingested error"},
		{name: "a handler's Bad", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			fn:   scoringReturns(events.Bad(errors.New("a day in the year 3000"))),
			want: 204, reason: events.ReasonUnknown, result: "bad", key: "ingested",
			letter: map[string]string{AttrKey: "ingested", AttrMessageID: ingested.ID, AttrReason: "sync"}, errHas: "3000", dlq: "ingested ok"},
		{name: "wrong key", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested, AttrKey, "scored"), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad",
			letter: map[string]string{AttrKey: "ingested", AttrMessageID: ingested.ID, AttrReason: "sync"}, errHas: AttrKey, dlq: "ingested ok"},
		{name: "empty key", path: "/push/session.completed", data: completed.Body, attrs: attrsOf(completed, AttrKey, ""), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad",
			letter: map[string]string{AttrKey: "session.completed", AttrMessageID: completed.ID, AttrReason: events.ReasonSessionCompleted},
			errHas: AttrKey, dlq: "session.completed ok"},
		{name: "wait with notify's key", path: PathWait, data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			want: 204, reason: "scored", result: "ok", key: "scored"},
		{name: "wait with refresh", path: PathWait, data: refresh.Body, attrs: attrsOf(refresh), attempt: attempt(3),
			want: 204, reason: "refresh", result: "ok", key: "refresh", redelivered: true},
		{name: "wait without a key", path: PathWait, data: scored.Body, attrs: attrsOf(scored, AttrKey, "-"), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad",
			letter: map[string]string{AttrMessageID: scored.ID, AttrReason: "scored"}, errHas: AttrKey, dlq: "unknown ok"},
		{name: "wait with scoring's key", path: PathWait, data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad",
			letter: map[string]string{AttrMessageID: ingested.ID, AttrReason: "sync"}, errHas: "notify", dlq: "unknown ok"},
		{name: "wait with a bad event", path: PathWait, data: []byte(`{}`), attrs: attrsOf(refresh), attempt: attempt(1),
			want: 204, reason: events.ReasonUnknown, result: "bad", key: "refresh",
			letter: map[string]string{AttrKey: "refresh", AttrMessageID: refresh.ID, AttrReason: "refresh"}, errHas: "day", dlq: "refresh ok"},
		{name: "defer with a sink", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			fn: notifyReturns(events.Defer(7 * time.Hour)), sink: "ok",
			want: 204, reason: "scored", result: "deferred", key: "scored", deferred: 7 * time.Hour},
		{name: "defer from wait", path: PathWait, data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			fn: notifyReturns(events.Defer(time.Hour)), sink: "ok",
			want: 204, reason: "scored", result: "deferred", key: "scored", deferred: time.Hour},
		{name: "defer without a sink", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			fn:   notifyReturns(events.Defer(7 * time.Hour)),
			want: 500, reason: "scored", result: "retry", key: "scored"},
		{name: "defer, the sink fails", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored), attempt: attempt(1),
			fn: notifyReturns(events.Defer(7 * time.Hour)), sink: "fail",
			want: 500, reason: "scored", result: "retry", key: "scored"},
		{name: "defer on scoring", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			fn:   scoringReturns(events.Defer(time.Minute)),
			want: 500, reason: "sync", result: "retry", key: "ingested"},
		{name: "handler error", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			fn:   scoringReturns(boom),
			want: 500, reason: "sync", result: "retry", key: "ingested"},
		{name: "handler error, attempt 19", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(MaxDeliveryAttempts - 1),
			fn:   scoringReturns(boom),
			want: 500, reason: "sync", result: "retry", key: "ingested", redelivered: true},
		{name: "handler error, last attempt", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(MaxDeliveryAttempts),
			fn:   scoringReturns(boom),
			want: 500, reason: "sync", result: "failed", key: "ingested", redelivered: true},
		{name: "panic", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(1),
			fn:   events.ScoringHandler(func(context.Context, model.IngestedEvent) error { panic("nil map") }),
			want: 500, reason: "sync", result: "retry", key: "ingested", panics: true},
		{name: "redelivery by deliveryAttempt", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(2),
			want: 204, reason: "sync", result: "ok", key: "ingested", redelivered: true},
		{name: "redelivery by ryft-replayed", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored, AttrReplayed, "true"), attempt: attempt(1),
			want: 204, reason: "scored", result: "ok", key: "scored", redelivered: true},
		{name: "ryft-replayed not true", path: "/push/scored", data: scored.Body, attrs: attrsOf(scored, AttrReplayed, "yes"), attempt: attempt(1),
			want: 204, reason: "scored", result: "ok", key: "scored"},
		{name: "no deliveryAttempt", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested),
			fn:   scoringReturns(boom),
			want: 500, reason: "sync", result: "retry", key: "ingested"},
		{name: "deliveryAttempt 0", path: "/push/ingested", data: ingested.Body, attrs: attrsOf(ingested), attempt: attempt(0),
			fn:   scoringReturns(boom),
			want: 500, reason: "sync", result: "retry", key: "ingested"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			consumer := consumerOf(c.path)
			dlq := &fakeDLQ{}
			if c.dlqFails {
				dlq.err = errors.New("pubsub: 503 UNAVAILABLE")
			}
			var deferrer *fakeDeferrer
			o := PushOptions{Consumer: consumer, Verifier: fakeVerifier{}, DLQ: dlq, Log: quiet, Now: func() time.Time { return now }}
			if c.sink != "" {
				deferrer = &fakeDeferrer{}
				if c.sink == "fail" {
					deferrer.err = errors.New("outbox: insert failed")
				}
				o.Deferrer = deferrer
			}
			fn := c.fn
			if fn == nil && consumer == events.ConsumerScoring {
				fn = scoringReturns(nil)
			} else if fn == nil {
				fn = notifyReturns(nil)
			}
			var ranWith string
			var redelivered bool
			p, err := NewPush(o, func(ctx context.Context, key string, data []byte) error {
				ranWith, redelivered = key, events.Redelivered(ctx)
				return fn(ctx, key, data)
			})
			if err != nil {
				t.Fatal(err)
			}

			handled := rise(metrics.EventsHandled.WithLabelValues(c.reason, c.result))
			redeliveries := rise(metrics.Redeliveries.WithLabelValues(consumer))
			panics := rise(metrics.ConsumerPanics.WithLabelValues("ingested"))
			var dlqCount func() float64
			if c.dlq != "" {
				subject, result, _ := strings.Cut(c.dlq, " ")
				dlqCount = rise(metrics.EventsDLQ.WithLabelValues(subject, result))
			}

			w := serve(p.Handler(c.path), http.MethodPost, pushBody(consumer+"-x", c.data, c.attrs, c.attempt), nil)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d", w.Code, c.want)
			}
			if w.Body.Len() != 0 {
				t.Errorf("a body %q: a push answer has none", w.Body.String())
			}
			if got := handled(); got != 1 {
				t.Errorf("ryft_events_handled_total{%s,%s} rose by %v, want 1", c.reason, c.result, got)
			}
			if ranWith != c.key {
				t.Errorf("the handler ran with key %q, want %q", ranWith, c.key)
			}
			if c.key != "" && redelivered != c.redelivered {
				t.Errorf("the handler saw redelivered %v, want %v", redelivered, c.redelivered)
			}
			if want := map[bool]float64{true: 1}[c.redelivered]; redeliveries() != want {
				t.Errorf("ryft_nats_redeliveries_total{%s} rose by %v, want %v", consumer, redeliveries(), want)
			}
			if want := map[bool]float64{true: 1}[c.panics]; panics() != want {
				t.Errorf("ryft_event_handler_panics_total{ingested} rose by %v, want %v", panics(), want)
			}
			if dlqCount != nil && dlqCount() != 1 {
				t.Errorf("ryft_events_dlq_total{%s} rose by %v, want 1", c.dlq, dlqCount())
			}

			switch {
			case c.letter == nil && len(dlq.got) != 0:
				t.Errorf("dead-lettered %+v", dlq.got)
			case c.letter != nil && len(dlq.got) != 1:
				t.Errorf("%d dead letters, want 1", len(dlq.got))
			case c.letter != nil:
				l := dlq.got[0]
				if want, _ := DLQTopic(consumer); l.topic != want {
					t.Errorf("dead-lettered to %s, want %s", l.topic, want)
				}
				if !bytes.Equal(l.m.Data, c.data) || l.m.OrderingKey != "" {
					t.Errorf("dead letter data %q ordering %q: the pushed data, unordered", l.m.Data, l.m.OrderingKey)
				}
				cause := l.m.Attributes[AttrError]
				if cause == "" || len(cause) > MaxErrorBytes || !strings.Contains(cause, c.errHas) {
					t.Errorf("ryft-error %q: want 1..%d bytes with %q", cause, MaxErrorBytes, c.errHas)
				}
				rest := maps.Clone(l.m.Attributes)
				delete(rest, AttrError)
				if !maps.Equal(rest, c.letter) {
					t.Errorf("dead letter attributes %v, want %v and ryft-error", rest, c.letter)
				}
			}

			switch {
			case c.deferred == 0 && deferrer != nil && len(deferrer.got) != 0:
				t.Errorf("deferred %+v", deferrer.got)
			case c.deferred != 0 && (deferrer == nil || len(deferrer.got) != 1):
				t.Error("not deferred")
			case c.deferred != 0:
				d := deferrer.got[0]
				want := events.Message{Key: c.key, ID: c.attrs[AttrMessageID], Body: c.data, Reason: c.reason}
				if d.m.Key != want.Key || d.m.ID != want.ID || d.m.Reason != want.Reason || !bytes.Equal(d.m.Body, want.Body) {
					t.Errorf("deferred %+v, want %+v", d.m, want)
				}
				if !d.at.Equal(now.Add(c.deferred)) {
					t.Errorf("deferred until %s, want %s", d.at, now.Add(c.deferred))
				}
			}
		})
	}
}

// One envelope as Pub/Sub's wrapped push sends it (field order and both spellings as
// recorded), on scoring's ordered subscription on its third attempt.
func TestPushRecordedEnvelope(t *testing.T) {
	user := uuid.New()
	m := must(events.Ingested(model.IngestedEvent{UserID: user, Days: []string{"2026-11-12", "2026-11-13"}, Reason: "sync"}, "ingest:"+uuid.NewString()))
	body := fmt.Sprintf(`{
  "message": {
    "attributes": {"ryft-key": "ingested", "ryft-message-id": %q, "ryft-reason": "sync"},
    "data": %q,
    "messageId": "13561470523127459",
    "message_id": "13561470523127459",
    "orderingKey": %q,
    "publishTime": "2026-11-14T06:15:02.381Z",
    "publish_time": "2026-11-14T06:15:02.381Z"
  },
  "subscription": "projects/ryft-test/subscriptions/scoring-ingested",
  "deliveryAttempt": 3
}`, m.ID, base64.StdEncoding.EncodeToString(m.Body), user.String())

	var got model.IngestedEvent
	var redelivered bool
	p, err := NewPush(PushOptions{Consumer: events.ConsumerScoring, Verifier: fakeVerifier{}, DLQ: &fakeDLQ{}, Log: quiet},
		events.ScoringHandler(func(ctx context.Context, ev model.IngestedEvent) error {
			got, redelivered = ev, events.Redelivered(ctx)
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(p.Handler("/push/ingested"), http.MethodPost, body, nil); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if got.UserID != user || strings.Join(got.Days, ",") != "2026-11-12,2026-11-13" || got.Reason != "sync" || !redelivered {
		t.Errorf("handled %+v redelivered %v", got, redelivered)
	}
}

// What is answered before the handler runs: the method, the token, the envelope.
func TestPushRefuses(t *testing.T) {
	good := pushBody("scoring-ingested", []byte(`{}`), nil, attempt(1))
	cases := []struct {
		name       string
		method     string
		body       string
		verifier   error
		want       int
		retryAfter string
		authReason string // the verifier's reason, counted by the verifier, not here
	}{
		{name: "GET", method: http.MethodGet, body: good, want: 405},
		{name: "no token", body: good, verifier: auth.Unauthorized("missing_token", errors.New("no bearer")), want: 401},
		{name: "a bad token", body: good, verifier: auth.Unauthorized("audience", errors.New("wrong audience")), want: 401},
		{name: "a plain verifier error", body: good, verifier: errors.New("odd"), want: 401},
		{name: "keys unavailable", body: good, verifier: auth.Unavailable("jwks_unavailable", errors.New("no keys")), want: 503, retryAfter: "5"},
		{name: "not JSON", body: `<html>`, want: 400},
		{name: "not an object", body: `[]`, want: 400},
		{name: "data not base64", body: `{"message":{"data":"!!","messageId":"1"},"subscription":"s"}`, want: 400},
		{name: "no messageId", body: `{"message":{"data":"e30="},"subscription":"s"}`, want: 400},
		{name: "messageId not decimal", body: `{"message":{"data":"e30=","messageId":"17x"},"subscription":"s"}`, want: 400},
		{name: "messageId with a newline", body: `{"message":{"data":"e30=","messageId":"17\nlevel=ERROR"},"subscription":"s"}`, want: 400},
		{name: "messageId of 33 digits", body: `{"message":{"data":"e30=","messageId":"` + strings.Repeat("1", 33) + `"},"subscription":"s"}`, want: 400},
		{name: "deliveryAttempt not a number", body: `{"message":{"data":"e30=","messageId":"1"},"deliveryAttempt":"2"}`, want: 400},
		{name: "too big", body: `{"message":{"data":"` + strings.Repeat("A", MaxPushBody) + `","messageId":"1"}}`, want: 413},
		{name: "empty", body: ``, want: 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ran := false
			dlq := &fakeDLQ{}
			p, err := NewPush(PushOptions{Consumer: events.ConsumerScoring, Verifier: fakeVerifier{c.verifier}, DLQ: dlq, Log: quiet},
				func(context.Context, string, []byte) error { ran = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			method := c.method
			if method == "" {
				method = http.MethodPost
			}
			w := serve(p.Handler("/push/ingested"), method, c.body, nil)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d", w.Code, c.want)
			}
			if got := w.Header().Get("Retry-After"); got != c.retryAfter {
				t.Errorf("Retry-After %q, want %q", got, c.retryAfter)
			}
			if c.want == 405 && w.Header().Get("Allow") != http.MethodPost {
				t.Errorf("Allow %q", w.Header().Get("Allow"))
			}
			if ran || len(dlq.got) != 0 {
				t.Error("the handler ran or a dead letter went out")
			}
		})
	}
}

// The token is checked before the body is read: an unauthenticated caller cannot make
// the service read a megabyte.
func TestPushChecksTheTokenFirst(t *testing.T) {
	p, err := NewPush(PushOptions{Consumer: events.ConsumerNotify, Verifier: fakeVerifier{auth.Unauthorized("missing_token", nil)}, DLQ: &fakeDLQ{}, Log: quiet},
		func(context.Context, string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(p.Handler("/push/scored"), http.MethodPost, strings.Repeat("x", 2*MaxPushBody), nil); w.Code != 401 {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

// A long cause is clipped to MaxErrorBytes in ryft-error, and so are copied attributes.
func TestPushClipsTheDeadLetter(t *testing.T) {
	dlq := &fakeDLQ{}
	p, err := NewPush(PushOptions{Consumer: events.ConsumerNotify, Verifier: fakeVerifier{}, DLQ: dlq, Log: quiet},
		func(context.Context, string, []byte) error { return events.Bad(errors.New(strings.Repeat("ü", 600))) })
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]string{AttrKey: "refresh", AttrMessageID: strings.Repeat("m", 2000)}
	if w := serve(p.Handler("/push/refresh"), http.MethodPost, pushBody("notify-refresh", []byte(`{}`), attrs, attempt(1)), nil); w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
	if len(dlq.got) != 1 {
		t.Fatal("no dead letter")
	}
	a := dlq.got[0].m.Attributes
	if e := a[AttrError]; len(e) > MaxErrorBytes || len(e) < MaxErrorBytes-1 || !strings.HasPrefix(e, "üü") || strings.ContainsRune(e, '�') {
		t.Errorf("ryft-error is %d bytes, want the first ≤%d on a rune boundary", len(e), MaxErrorBytes)
	}
	if len(a[AttrMessageID]) != MaxErrorBytes {
		t.Errorf("ryft-message-id is %d bytes, want %d", len(a[AttrMessageID]), MaxErrorBytes)
	}
}

// The envelope is whoever pushed it: a subscription is cut in the log line, and a
// ryft-message-id is cut before a deferral stores it (an attribute over Pub/Sub's 1024
// bytes could never be published again).
func TestPushCutsWhatTheEnvelopeHolds(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	def := &fakeDeferrer{}
	p, err := NewPush(PushOptions{Consumer: events.ConsumerNotify, Verifier: fakeVerifier{}, DLQ: &fakeDLQ{}, Deferrer: def, Log: log},
		func(context.Context, string, []byte) error { return events.Defer(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	refresh := must(events.Refresh(model.RefreshEvent{UserID: uuid.New(), Day: "2026-11-15"}))
	attrs := attrsOf(refresh, AttrMessageID, strings.Repeat("m", 2000))
	if w := serve(p.Handler("/push/refresh"), http.MethodPost, pushBody(strings.Repeat("s", 5000), refresh.Body, attrs, attempt(1)), nil); w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
	if len(def.got) != 1 || def.got[0].m.ID != strings.Repeat("m", maxMessageID) {
		t.Fatalf("deferred %d, ID of %d bytes: want one, its ID cut to %d", len(def.got), len(def.got[0].m.ID), maxMessageID)
	}
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatal(err)
		}
		if sub, ok := line["subscription"].(string); ok {
			lines++
			if len(sub) > maxLogValue {
				t.Errorf("%q logs a subscription of %d bytes, want at most %d", line["msg"], len(sub), maxLogValue)
			}
		}
	}
	if lines == 0 {
		t.Fatal("no line with the subscription")
	}
}

// The push handler logs the message id with the last error, and never the token or the
// event's data.
func TestPushLogsNeitherTokenNorBody(t *testing.T) {
	token := "eyJ" + strings.Repeat("x", 12) + "." + uuid.NewString() // built at runtime
	marker := "marker-" + uuid.NewString()
	data := []byte(`{"user_id":"` + uuid.NewString() + `","note":"` + marker + `"}`)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	outcomes := map[string]events.Handler{
		"error": func(context.Context, string, []byte) error { return errors.New("store: timeout") },
		"bad":   func(context.Context, string, []byte) error { return events.Bad(errors.New("unknown day")) },
		"panic": func(context.Context, string, []byte) error { panic("index out of range") },
	}
	for name, fn := range outcomes {
		p, err := NewPush(PushOptions{Consumer: events.ConsumerScoring, Verifier: fakeVerifier{}, DLQ: &fakeDLQ{}, Log: log}, fn)
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{"Authorization": {"Bearer " + token}}
		serve(p.Handler("/push/ingested"), http.MethodPost, pushBody("scoring-ingested", data, map[string]string{AttrKey: "ingested"}, attempt(2)), h)
		if !strings.Contains(buf.String(), `"message_id":"17012345678901234"`) {
			t.Errorf("%s: the log lacks the message id", name)
		}
	}
	p, err := NewPush(PushOptions{Consumer: events.ConsumerScoring, Verifier: fakeVerifier{auth.Unauthorized("invalid", errors.New("signature"))}, DLQ: &fakeDLQ{}, Log: log},
		func(context.Context, string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	serve(p.Handler("/push/ingested"), http.MethodPost, pushBody("scoring-ingested", data, nil, nil), http.Header{"Authorization": {"Bearer " + token}})

	out := buf.String()
	for _, secret := range []string{token, marker, base64.StdEncoding.EncodeToString(data)} {
		if strings.Contains(out, secret) {
			t.Errorf("the log holds %.20q...", secret)
		}
	}
	for _, want := range []string{"store: timeout", "dead-lettered", "handler panicked", `"reason":"invalid"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
}

func TestPushRegister(t *testing.T) {
	for consumer, want := range map[string][]string{
		events.ConsumerScoring: {"/push/ingested", "/push/session.completed"},
		events.ConsumerNotify:  {"/push/scored", "/push/session.scored", "/push/refresh", "/push/wait"},
	} {
		p, err := NewPush(PushOptions{Consumer: consumer, Verifier: fakeVerifier{}, DLQ: &fakeDLQ{}, Log: quiet},
			func(context.Context, string, []byte) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.Paths(), want) {
			t.Errorf("%s: paths %v, want %v", consumer, p.Paths(), want)
		}
		mux := http.NewServeMux()
		p.Register(mux)
		for _, path := range want {
			key := "scored"
			if k, ok := KeyOfPath(path); ok {
				key = k
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(pushBody("x", []byte(`{}`), map[string]string{AttrKey: key}, attempt(1))))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Errorf("%s POST %s: %d", consumer, path, w.Code)
			}
			r = httptest.NewRequest(http.MethodGet, path, nil)
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s GET %s: %d", consumer, path, w.Code)
			}
		}
		other := "/push/ingested"
		if consumer == events.ConsumerScoring {
			other = "/push/scored"
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, other, strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s serves %s: %d", consumer, other, w.Code)
		}
	}
}

func TestNewPushRefuses(t *testing.T) {
	ok := func(context.Context, string, []byte) error { return nil }
	good := PushOptions{Consumer: events.ConsumerScoring, Verifier: fakeVerifier{}, DLQ: &fakeDLQ{}, Log: quiet}
	for name, o := range map[string]PushOptions{
		"no consumer":    {Verifier: good.Verifier, DLQ: good.DLQ, Log: quiet},
		"api":            {Consumer: "api", Verifier: good.Verifier, DLQ: good.DLQ, Log: quiet},
		"no verifier":    {Consumer: events.ConsumerScoring, DLQ: good.DLQ, Log: quiet},
		"no dead letter": {Consumer: events.ConsumerScoring, Verifier: good.Verifier, Log: quiet},
		"no logger":      {Consumer: events.ConsumerScoring, Verifier: good.Verifier, DLQ: good.DLQ},
	} {
		if _, err := NewPush(o, ok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewPush(good, nil); err == nil {
		t.Error("no handler: accepted")
	}
	if _, err := NewPush(good, ok); err != nil {
		t.Error(err)
	}
}
