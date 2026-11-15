package events

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRedelivered(t *testing.T) {
	ctx := context.Background()
	if Redelivered(ctx) {
		t.Fatal("a first delivery is not redelivered")
	}
	if !Redelivered(WithRedelivery(ctx)) {
		t.Fatal("marked, not seen")
	}
	type other struct{}
	if !Redelivered(context.WithValue(WithRedelivery(ctx), other{}, 1)) {
		t.Fatal("the mark is lost in a derived context")
	}
}

func TestGuardedTurnsAPanicIntoAnError(t *testing.T) {
	var nilMap map[string]*int
	err := Guarded(quiet, KeyRefresh, func() error { return errors.New(strings.Repeat("x", 1) + fmtNil(*nilMap["x"])) })
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("a panic must become an error: %v", err)
	}
	if err := Guarded(quiet, KeyRefresh, func() error { return nil }); err != nil {
		t.Fatalf("no panic, no error: %v", err)
	}
}

func fmtNil(int) string { return "" }

type recorder struct {
	mu   sync.Mutex
	sent []Message
}

func (r *recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

// Every event decodes on its consumer's side to what was published, with a stable id
// (the ryft-message-id attribute, for a consumer that wants to recognise a repeat).
func TestEventsRoundTrip(t *testing.T) {
	r := &recorder{}
	p := Publisher{Sender: r}
	ctx := context.Background()
	user, session := uuid.New(), uuid.New()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.PublishIngested(ctx, model.IngestedEvent{UserID: user, Days: []string{"2026-10-21"}, Reason: "sync", RequestID: "r1"}, ""))
	must(p.PublishSessionCompleted(ctx, model.SessionCompletedEvent{UserID: user, SessionID: session, Day: "2026-10-20"}, "session:x"))
	must(p.PublishScored(ctx, model.ScoredEvent{UserID: user, Day: "2026-10-21"}))
	must(p.PublishSessionScored(ctx, model.SessionScoredEvent{UserID: user, SessionID: session, Day: "2026-10-20"}))
	must(p.PublishRefresh(ctx, model.RefreshEvent{UserID: user, Day: "2026-10-21"}))

	keys := []string{}
	for _, m := range r.sent {
		keys = append(keys, m.Key)
	}
	if strings.Join(keys, ",") != "ingested,session.completed,scored,session.scored,refresh" {
		t.Fatalf("keys %v", keys)
	}
	ing, err := DecodeIngested(r.sent[0].Key, r.sent[0].Body)
	if err != nil || ing.UserID != user || ing.Days[0] != "2026-10-21" || ing.Reason != "sync" || r.sent[0].ID != "" {
		t.Fatalf("ingested: %+v %v id %q", ing, err, r.sent[0].ID)
	}
	sess, err := DecodeIngested(r.sent[1].Key, r.sent[1].Body)
	if err != nil || sess.Reason != ReasonSessionCompleted || *sess.SessionID != session || sess.Days[0] != "2026-10-20" || r.sent[1].ID != "session:x" {
		t.Fatalf("session completed: %+v %v", sess, err)
	}
	for i, want := range []string{"scored", "session_scored", "refresh"} {
		m := r.sent[2+i]
		n, err := DecodeNotify(m.Key, m.Body)
		if err != nil || n.Reason() != want || m.Reason != want {
			t.Fatalf("%s: %+v %v", want, n, err)
		}
	}
	if r.sent[2].ID != "scored:"+user.String()+":2026-10-21" || r.sent[3].ID != "session-scored:"+session.String() {
		t.Fatalf("ids: %q %q", r.sent[2].ID, r.sent[3].ID)
	}
	if _, err := DecodeIngested(KeyScored, r.sent[2].Body); err == nil {
		t.Fatal("scoring must refuse notify's events")
	}
	if _, err := DecodeNotify(KeyScored, []byte(`{"user_id":"`+user.String()+`"}`)); err == nil {
		t.Fatal("a scored event without a day is bad")
	}
}

func TestOutcomes(t *testing.T) {
	if after, ok := Deferred(Defer(time.Millisecond)); !ok || after != time.Second {
		t.Fatalf("a deferral is at least a second: %v %v", after, ok)
	}
	if after, _ := Deferred(Defer(48 * time.Hour)); after != 24*time.Hour {
		t.Fatalf("and at most a day: %v", after)
	}
	if !IsPermanent(Permanent(errors.New("gone"))) || IsPermanent(errors.New("transient")) {
		t.Fatal("permanent errors are recognised")
	}
	cause := errors.New("does not decode")
	bad := Bad(cause)
	if !IsBad(bad) || !IsBad(fmt.Errorf("wrapped: %w", bad)) || IsBad(cause) || IsBad(Permanent(cause)) {
		t.Fatal("bad events are recognised, and only they")
	}
	if !errors.Is(bad, cause) || bad.Error() != cause.Error() {
		t.Fatalf("Bad keeps its cause and its text: %v", bad)
	}
	if IsPermanent(bad) {
		t.Fatal("a bad event is not a permanent failure: it is dead-lettered, not dropped")
	}
}

// The typed handlers decode for the transport: what decodes reaches the function with
// its metric reason set, what does not is Bad and never reaches it.
func TestTypedHandlers(t *testing.T) {
	user, session := uuid.New(), uuid.New()
	ing, _ := Ingested(model.IngestedEvent{UserID: user, Days: []string{"2026-10-21"}, Reason: "sync"}, "")
	sess, _ := SessionCompleted(model.SessionCompletedEvent{UserID: user, SessionID: session, Day: "2026-10-20"}, "s")
	scored, _ := Scored(model.ScoredEvent{UserID: user, Day: "2026-10-21"})
	refresh, _ := Refresh(model.RefreshEvent{UserID: user, Day: "2026-10-21"})

	var gotIngested []model.IngestedEvent
	scoring := ScoringHandler(func(_ context.Context, ev model.IngestedEvent) error {
		gotIngested = append(gotIngested, ev)
		return nil
	})
	var gotNotify []NotifyMessage
	notify := NotifyHandler(func(_ context.Context, m NotifyMessage) error {
		gotNotify = append(gotNotify, m)
		return errors.New("handler failed")
	})
	for _, c := range []struct {
		name   string
		h      Handler
		key    string
		data   []byte
		reason string
		bad    bool
		failed bool
	}{
		{"ingested", scoring, KeyIngested, ing.Body, "sync", false, false},
		{"session completed", scoring, KeySessionCompleted, sess.Body, ReasonSessionCompleted, false, false},
		{"scoring: not JSON", scoring, KeyIngested, []byte("{"), ReasonUnknown, true, false},
		{"scoring: notify's key", scoring, KeyScored, scored.Body, ReasonUnknown, true, false},
		{"scored", notify, KeyScored, scored.Body, "scored", false, true},
		{"refresh", notify, KeyRefresh, refresh.Body, "refresh", false, true},
		{"notify: no day", notify, KeyScored, []byte(`{"user_id":"` + user.String() + `"}`), ReasonUnknown, true, false},
		{"notify: scoring's key", notify, KeyIngested, ing.Body, ReasonUnknown, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, reason := WithReason(context.Background())
			err := c.h(ctx, c.key, c.data)
			if IsBad(err) != c.bad {
				t.Fatalf("bad %v, want %v: %v", IsBad(err), c.bad, err)
			}
			if failed := err != nil && !IsBad(err); failed != c.failed {
				t.Fatalf("handler error %v, want %v: %v", failed, c.failed, err)
			}
			if got := reason(); got != c.reason {
				t.Fatalf("reason %q, want %q", got, c.reason)
			}
		})
	}
	if len(gotIngested) != 2 || gotIngested[1].SessionID == nil || *gotIngested[1].SessionID != session {
		t.Fatalf("scoring got %+v", gotIngested)
	}
	if len(gotNotify) != 2 || gotNotify[0].Scored == nil || gotNotify[1].Refresh == nil {
		t.Fatalf("notify got %+v", gotNotify)
	}
}

func TestReasonSlot(t *testing.T) {
	SetReason(context.Background(), "sync") // no slot: nothing to set, no panic
	ctx, reason := WithReason(context.Background())
	if reason() != ReasonUnknown {
		t.Fatalf("before: %q", reason())
	}
	SetReason(ctx, "")
	if reason() != ReasonUnknown {
		t.Fatalf("an empty reason is not a label: %q", reason())
	}
	type other struct{}
	SetReason(context.WithValue(ctx, other{}, 1), "recompute")
	if reason() != "recompute" {
		t.Fatalf("a derived context sets the same slot: %q", reason())
	}
}

// An event that does not decode is logged and dead-lettered with its error, so
// the error names the field and the type, never the value.
func TestDecodeErrorsQuoteNoValues(t *testing.T) {
	user := uuid.NewString()
	for body, field := range map[string]string{
		`{"user_id":61.7,"day":"2026-10-21"}`:                    "ScoredEvent.user_id",
		`{"user_id":"` + user + `","day":61.7}`:                  "ScoredEvent.day",
		`{"user_id":"` + user + `","day":"x","request_id":61.7}`: "ScoredEvent.request_id",
		`[61.7]`: "Go value", // not an object
	} {
		_, err := DecodeNotify(KeyScored, []byte(body))
		if err == nil {
			t.Fatalf("%s: must not decode", body)
		}
		if msg := err.Error(); strings.Contains(msg, "61.7") || !strings.Contains(msg, field) || strings.Contains(msg, "  ") {
			t.Errorf("%s: %q: want the field %s and no value", body, msg, field)
		}
	}
}

// The scored event names the user and the day, and no health value: it waits in
// Pub/Sub, its dead-letter hold and notify's waiting rows for days, and notify reads the
// score when it sends. An event of an older image, with the values, still decodes.
func TestScoredEventHoldsNoValues(t *testing.T) {
	user := uuid.New()
	m, err := Scored(model.ScoredEvent{UserID: user, Day: "2026-10-21", RequestID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"user_id":"` + user.String() + `","day":"2026-10-21","request_id":"r1"}`; string(m.Body) != want {
		t.Fatalf("body %s, want %s", m.Body, want)
	}
	old := `{"user_id":"` + user.String() + `","day":"2026-10-21","recovery":71,"readiness":64,"state":"green",` +
		`"sleep_performance":88,"sleep_min":452,"request_id":"r1"}`
	n, err := DecodeNotify(KeyScored, []byte(old))
	if err != nil || n.Scored == nil || *n.Scored != (model.ScoredEvent{UserID: user, Day: "2026-10-21", RequestID: "r1"}) {
		t.Fatalf("an older image's event: %+v %v", n.Scored, err)
	}
}

// The reason label comes from an ingested event's body: a reason no publisher writes is
// counted as unknown, so a forged event cannot add series. The event itself keeps it.
func TestMetricReasonIsBounded(t *testing.T) {
	for _, r := range Reasons {
		if MetricReason(r) != r {
			t.Errorf("%s: a publisher's reason is its own label", r)
		}
	}
	for _, r := range []string{"rescore", "Sync", "sync ", "x-" + uuid.NewString(), strings.Repeat("a", 4096), ReasonUnknown} {
		if MetricReason(r) != ReasonUnknown {
			t.Errorf("%.40q: want unknown", r)
		}
	}
	// What the encoders write is in the list.
	user := uuid.New()
	for _, m := range []Message{
		must(SessionCompleted(model.SessionCompletedEvent{UserID: user, SessionID: uuid.New(), Day: "2026-10-21"}, "")),
		must(Scored(model.ScoredEvent{UserID: user, Day: "2026-10-21"})),
		must(SessionScored(model.SessionScoredEvent{UserID: user, SessionID: uuid.New(), Day: "2026-10-21"})),
		must(Refresh(model.RefreshEvent{UserID: user, Day: "2026-10-21"})),
	} {
		if MetricReason(m.Reason) != m.Reason {
			t.Errorf("%s: reason %q is not in Reasons", m.Key, m.Reason)
		}
	}
	forged := must(Ingested(model.IngestedEvent{UserID: user, Days: []string{"2026-10-21"}, Reason: "x-" + uuid.NewString()}, ""))
	var got model.IngestedEvent
	ctx, reason := WithReason(context.Background())
	if err := ScoringHandler(func(_ context.Context, ev model.IngestedEvent) error { got = ev; return nil })(ctx, KeyIngested, forged.Body); err != nil {
		t.Fatal(err)
	}
	if reason() != ReasonUnknown || !strings.HasPrefix(got.Reason, "x-") {
		t.Fatalf("label %q, the event's reason %q: want unknown, and the event unchanged", reason(), got.Reason)
	}
}

func must(m Message, err error) Message {
	if err != nil {
		panic(err)
	}
	return m
}

// Rejected marks a send no retry can fix, and keeps its cause.
func TestRejected(t *testing.T) {
	cause := errors.New("pubsub: publish to ryft-scored: HTTP 404: NOT_FOUND")
	r := Rejected(cause)
	if !IsRejected(r) || !IsRejected(fmt.Errorf("outbox: %w", r)) || IsRejected(cause) || IsRejected(Permanent(cause)) {
		t.Fatal("a rejection is recognised, and only it")
	}
	if !errors.Is(r, cause) || r.Error() != cause.Error() {
		t.Fatalf("Rejected keeps its cause and its text: %v", r)
	}
}
