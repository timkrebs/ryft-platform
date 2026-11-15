package pubsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// The emulator tests (A1-6; design 2.3: publish → push → nack → dead letter against the
// emulator). They run against the Pub/Sub emulator at RYFT_TEST_PUBSUB_EMULATOR_HOST
// (host:port; CI's test job runs it with host networking, so its pushes reach the servers
// these tests bind on 127.0.0.1) and skip without it. Each test makes its own topics and
// subscriptions in project ryft-test (init.sh: in a project of its own), named with a
// random suffix and deleted at its end, runs in parallel with the others and is done
// within emuBudget. Where the emulator may differ from Pub/Sub without the transport
// relying on it (a field it does not keep, no deliveryAttempt on a push) the test logs it
// with the prefix "emulator:" (go test -v); where the transport relies on it, it fails.

const (
	emuHostEnv = "RYFT_TEST_PUBSUB_EMULATOR_HOST"
	emuProject = "ryft-test"
	emuBudget  = 60 * time.Second // one test, from its start to its last assertion
	emuAck     = 10               // ackDeadlineSeconds of the tests' subscriptions: Pub/Sub's minimum
	// The retry policy of the tests' push subscriptions: a nack is pushed again within
	// a second or two (Terraform's 10-600 s would not fit a test).
	emuRetryMin = "1s"
	emuRetryMax = "2s"
	// emuQuiet is how long a settled message must stay away: longer than the ack deadline
	// plus the retry policy, so a push the emulator did not take as acked comes again
	// within it.
	emuQuiet       = 15 * time.Second
	emuArrive      = 20 * time.Second // how long a push or a letter may take to arrive
	emuPoll        = 100 * time.Millisecond
	emuDLQAttempts = 5 // maxDeliveryAttempts of the nack test: Pub/Sub's minimum
	// deadLetterSource prefixes the attributes Pub/Sub puts on its own dead letters.
	deadLetterSource = "CloudPubSubDeadLetterSource"
)

// init.sh (generated from the topology) is accepted by the emulator, twice (a rerun keeps
// what exists), and the emulator reports every topic and subscription as the topology
// has it: the push endpoint, the ordering, the ack deadline and the dead-letter policy
// must match. The retention and the retry policy the emulator may accept without keeping
// (configurable retention is a documented gap); a difference there is logged by field.
// If the emulator refuses a request, the failure names the field (diagnose).
func TestEmulatorInitScript(t *testing.T) {
	e, ctx := newEmu(t)
	e.project = "ryft-init-" + e.id // init.sh's fixed names, in a project of their own
	if _, err := exec.LookPath("curl"); err != nil {
		t.Fatal("init.sh needs curl on PATH")
	}
	script := repoFile(t, EmulatorInitFile)
	top := Spec()
	t.Cleanup(func() {
		for _, s := range top.Push {
			e.remove("subscriptions/" + s.Name)
		}
		for _, h := range top.Hold {
			e.remove("subscriptions/" + h.Name)
		}
		for _, x := range top.Topics {
			e.remove("topics/" + x.Name)
		}
	})

	objects := len(top.Topics) + len(top.Push) + len(top.Hold)
	for run, want := range []string{"created ", "exists  "} {
		stdout, stderr, err := runInit(ctx, script, e.host, e.project)
		if err != nil {
			t.Fatalf("init.sh, run %d: %v\nstderr: %s\nstdout:\n%s%s", run+1, err, stderr, stdout, e.diagnose(top))
		}
		if n := countLines(stdout, want); n != objects {
			t.Errorf("init.sh, run %d: %d lines %q, want %d (one per topic and subscription):\n%s", run+1, n, want, objects, stdout)
		}
	}

	for _, x := range top.Topics {
		var got struct {
			Name string `json:"name"`
		}
		e.get(ctx, "topics/"+x.Name, &got)
		if got.Name != e.topicPath(x.Name) {
			t.Errorf("topic %s: the emulator names it %q", x.Name, got.Name)
		}
	}
	var notKept []string
	for _, s := range top.Push {
		var got emuSub
		e.get(ctx, "subscriptions/"+s.Name, &got)
		where := "subscription " + s.Name
		if got.Topic != e.topicPath(s.Topic) {
			t.Errorf("%s: topic %q, want %q", where, got.Topic, e.topicPath(s.Topic))
		}
		if ep := endpointOf(got); ep != emulatorEndpoint(s) {
			t.Errorf("%s: pushConfig.pushEndpoint %q, want %q", where, ep, emulatorEndpoint(s))
		}
		if got.Ordered != s.Ordered {
			t.Errorf("%s: enableMessageOrdering %v, want %v (an absent field reads false: dropped)", where, got.Ordered, s.Ordered)
		}
		if got.AckDeadline != int(seconds(s.AckDeadline)) {
			t.Errorf("%s: ackDeadlineSeconds %d, want %d", where, got.AckDeadline, seconds(s.AckDeadline))
		}
		dl := got.DeadLetterPolicy
		switch {
		case s.DeadLetterTopic == "":
			if dl != nil && dl.DeadLetterTopic != "" {
				t.Errorf("%s: deadLetterPolicy %+v, want none", where, *dl)
			}
		case dl == nil || dl.DeadLetterTopic == "":
			t.Errorf("%s: no deadLetterPolicy, want %s after %d attempts: the emulator dropped it", where, s.DeadLetterTopic, MaxDeliveryAttempts)
		default:
			if dl.DeadLetterTopic != e.topicPath(s.DeadLetterTopic) {
				t.Errorf("%s: deadLetterPolicy.deadLetterTopic %q, want %q", where, dl.DeadLetterTopic, e.topicPath(s.DeadLetterTopic))
			}
			if dl.MaxDeliveryAttempts != MaxDeliveryAttempts {
				t.Errorf("%s: deadLetterPolicy.maxDeliveryAttempts %d, want %d", where, dl.MaxDeliveryAttempts, MaxDeliveryAttempts)
			}
		}
		notKept = appendNotKept(notKept, where, "messageRetentionDuration", got.Retention, s.Retention)
		if got.RetryPolicy == nil {
			notKept = append(notKept, where+": retryPolicy dropped")
		} else {
			notKept = appendNotKept(notKept, where, "retryPolicy.minimumBackoff", got.RetryPolicy.MinimumBackoff, RetryMinimumBackoff)
			notKept = appendNotKept(notKept, where, "retryPolicy.maximumBackoff", got.RetryPolicy.MaximumBackoff, RetryMaximumBackoff)
		}
	}
	for _, h := range top.Hold {
		var got emuSub
		e.get(ctx, "subscriptions/"+h.Name, &got)
		where := "hold subscription " + h.Name
		if got.Topic != e.topicPath(h.Topic) {
			t.Errorf("%s: topic %q, want %q", where, got.Topic, e.topicPath(h.Topic))
		}
		if ep := endpointOf(got); ep != "" {
			t.Errorf("%s: pushes to %q, want a pull subscription", where, ep)
		}
		if got.AckDeadline != int(seconds(HoldAckDeadline)) {
			t.Errorf("%s: ackDeadlineSeconds %d, want %d", where, got.AckDeadline, seconds(HoldAckDeadline))
		}
		if dl := got.DeadLetterPolicy; dl != nil && dl.DeadLetterTopic != "" {
			t.Errorf("%s: deadLetterPolicy %+v, want none", where, *dl)
		}
		notKept = appendNotKept(notKept, where, "messageRetentionDuration", got.Retention, h.Retention)
	}
	if len(notKept) > 0 {
		t.Logf("emulator: accepted, but reports otherwise (%d):\n  %s", len(notKept), strings.Join(notKept, "\n  "))
	} else {
		t.Log("emulator: reports every field init.sh sends as sent")
	}
}

// publish → push → ack: the Bus publishes an ingested event to ryft-ingested with the
// user as ordering key; the emulator pushes it to scoring's /push/ingested with the
// ryft-* attributes; the handler gets the path's key and the data as published, as a
// first delivery; the 204 acks it, so it is not pushed again.
func TestEmulatorPublishPushAck(t *testing.T) {
	e, ctx := newEmu(t)
	topic, dlq := e.name(TopicIngested), e.name(TopicDLQScoring)
	c, sent := e.client(map[string]string{TopicIngested: topic, TopicDLQScoring: dlq})
	var mu sync.Mutex
	var handled []model.IngestedEvent
	mux := http.NewServeMux()
	e.push(mux, PushOptions{Consumer: events.ConsumerScoring, DLQ: c}, events.ScoringHandler(func(_ context.Context, ev model.IngestedEvent) error {
		mu.Lock()
		defer mu.Unlock()
		handled = append(handled, ev)
		return nil
	}))
	srv, pushes := emuServer(t, mux)
	e.topic(ctx, topic)
	e.topic(ctx, dlq)
	sub := e.name("scoring-ingested")
	s := e.pushSub(topic, srv.URL+PushPath(events.KeyIngested), true)
	s.DeadLetterPolicy = &emuDeadLetter{DeadLetterTopic: e.topicPath(dlq), MaxDeliveryAttempts: MaxDeliveryAttempts}
	e.subscribe(ctx, sub, s)

	user := uuid.New()
	ev := model.IngestedEvent{UserID: user, Days: []string{"2026-11-13", "2026-11-14"}, Reason: "sync", RequestID: "emulator-" + e.id}
	m, err := events.Ingested(ev, "ingest:"+e.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewBus(c).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if !poll(ctx, emuArrive, func() bool { return len(pushes.of(m.ID)) > 0 }) {
		t.Fatalf("no push of the event within %s: %s", emuArrive, pushes)
	}

	d := pushes.of(m.ID)[0]
	t.Logf("emulator: push envelope %s", d.Raw)
	if d.EnvErr != nil {
		t.Errorf("the push body is not Pub/Sub's envelope: %v", d.EnvErr)
	}
	if d.Path != PushPath(events.KeyIngested) || d.Status != http.StatusNoContent {
		t.Fatalf("push to %s answered %d, want %s answered 204", d.Path, d.Status, PushPath(events.KeyIngested))
	}
	msg := d.Env.Message
	if want := map[string]string{AttrKey: m.Key, AttrMessageID: m.ID, AttrReason: m.Reason}; !maps.Equal(msg.Attributes, want) {
		t.Errorf("attributes %v, want %v", msg.Attributes, want)
	}
	if keys := sent.orderingKeys(); !slices.Equal(keys, []string{user.String()}) {
		t.Errorf("published with ordering keys %q, want only the user %s", keys, user)
	}
	switch msg.OrderingKey {
	case user.String():
	case "":
		t.Log("emulator: a push of an ordered subscription carries no orderingKey (the publish did)")
	default:
		t.Errorf("orderingKey %q, want the user %s", msg.OrderingKey, user)
	}
	if !bytes.Equal(msg.Data, m.Body) {
		t.Errorf("data %q, want %q", msg.Data, m.Body)
	}
	if !pubsubMessageID.MatchString(msg.MessageID) || msg.PublishTime == "" {
		t.Errorf("messageId %q, publishTime %q: want both, the id decimal (the push handlers refuse any other)", msg.MessageID, msg.PublishTime)
	}
	if !strings.HasSuffix(d.Env.Subscription, "/subscriptions/"+sub) {
		t.Errorf("subscription %q, want …/subscriptions/%s", d.Env.Subscription, sub)
	}
	switch a := d.Env.DeliveryAttempt; {
	case a == nil:
		t.Log("emulator: a push of a subscription with a dead-letter policy carries no deliveryAttempt")
	case *a != 1:
		t.Errorf("the first push has deliveryAttempt %d, want 1", *a)
	}
	if !d.Handled || d.Key != events.KeyIngested || !bytes.Equal(d.Data, m.Body) || d.Redelivered {
		t.Errorf("the handler got key %q, data %q, redelivered %v; want %q, %q, false", d.Key, d.Data, d.Redelivered, events.KeyIngested, m.Body)
	}
	mu.Lock()
	got := slices.Clone(handled)
	mu.Unlock()
	if len(got) != 1 || got[0].UserID != user || !slices.Equal(got[0].Days, ev.Days) || got[0].Reason != ev.Reason || got[0].RequestID != ev.RequestID {
		t.Errorf("decoded %+v, want once %+v", got, ev)
	}

	if !holds(t, ctx, emuQuiet, func() bool { return len(pushes.of(m.ID)) == 1 }) {
		t.Fatalf("pushed again after a 204: %s", pushes)
	}
	if got := sent.topics(); !slices.Equal(got, []string{TopicIngested}) {
		t.Errorf("published to %v, want only [%s]", got, TopicIngested)
	}
}

// nack → dead letter: every push of an event whose handler always fails is answered 500;
// the emulator pushes it again and, after maxDeliveryAttempts (5), forwards it to the
// subscription's dead-letter topic with Pub/Sub's CloudPubSubDeadLetterSource*
// attributes and the event's own, where the hold subscription has it. deliveryAttempt
// rises from 1 across the pushes, the handler told of a redelivery from the second on;
// or the emulator sends it on none, and then every push is a first delivery for the
// handler.
func TestEmulatorNackDeadLetter(t *testing.T) {
	e, ctx := newEmu(t)
	topic, dlq := e.name(TopicIngested), e.name("dlq")
	c, sent := e.client(map[string]string{TopicIngested: topic})
	mux := http.NewServeMux()
	e.push(mux, PushOptions{Consumer: events.ConsumerScoring, DLQ: c}, events.ScoringHandler(func(context.Context, model.IngestedEvent) error {
		return errors.New("emulator test: this handler always fails")
	}))
	srv, pushes := emuServer(t, mux)
	e.topic(ctx, topic)
	e.topic(ctx, dlq)
	hold := e.hold(ctx, dlq)
	sub := e.name("scoring-ingested")
	s := e.pushSub(topic, srv.URL+PushPath(events.KeyIngested), true) // ordered, as scoring's are
	s.DeadLetterPolicy = &emuDeadLetter{DeadLetterTopic: e.topicPath(dlq), MaxDeliveryAttempts: emuDLQAttempts}
	e.subscribe(ctx, sub, s)

	m, err := events.Ingested(model.IngestedEvent{UserID: uuid.New(), Days: []string{"2026-11-14"}, Reason: "sync"}, "ingest:"+e.id)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := NewBus(c).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	const within = 50 * time.Second
	var letters []emuReceived
	if !poll(ctx, within, func() bool {
		letters = append(letters, e.pull(ctx, hold)...)
		return len(letters) > 0
	}) {
		t.Fatalf("no dead letter on %s within %s: %s", hold, within, pushes)
	}
	t.Logf("emulator: dead-lettered %s after the publish; %s", time.Since(start).Round(100*time.Millisecond), pushes)

	if len(letters) != 1 {
		t.Errorf("%d letters, want 1", len(letters))
	}
	l := letters[0].Message
	if !bytes.Equal(l.Data, m.Body) {
		t.Errorf("the letter's data %q, want %q", l.Data, m.Body)
	}
	for k, want := range map[string]string{AttrKey: m.Key, AttrMessageID: m.ID, AttrReason: m.Reason} {
		if l.Attributes[k] != want {
			t.Errorf("the letter's %s %q, want %q (Pub/Sub keeps the event's attributes)", k, l.Attributes[k], want)
		}
	}
	if v, ok := l.Attributes[AttrError]; ok {
		t.Errorf("the letter carries %s %q: the handler dead-lettered it, not Pub/Sub", AttrError, v)
	}
	src := map[string]string{}
	for k, v := range l.Attributes {
		if strings.HasPrefix(k, deadLetterSource) {
			src[k] = v
		}
	}
	t.Logf("emulator: dead-letter attributes %v", src)
	if v := src[deadLetterSource+"Subscription"]; v != sub && v != e.subPath(sub) {
		t.Errorf("%sSubscription %q, want %s (the letter's attributes: %v)", deadLetterSource, v, sub, l.Attributes)
	}
	if v, ok := src[deadLetterSource+"SubscriptionProject"]; ok && v != e.project {
		t.Errorf("%sSubscriptionProject %q, want %s", deadLetterSource, v, e.project)
	}
	if v := src[deadLetterSource+"DeliveryCount"]; v == "" {
		t.Errorf("no %sDeliveryCount (the letter's attributes: %v)", deadLetterSource, l.Attributes)
	} else if n, err := strconv.Atoi(v); err != nil || n < 1 {
		t.Errorf("%sDeliveryCount %q, want the number of attempts", deadLetterSource, v)
	}

	ps := pushes.of(m.ID)
	if len(ps) < 2 {
		t.Errorf("dead-lettered after %d push(es): a nack is pushed again first", len(ps))
	}
	for i, p := range ps {
		if p.Status != http.StatusInternalServerError || !p.Handled {
			t.Errorf("push %d answered %d, handled %v; want 500 from the handler", i+1, p.Status, p.Handled)
		}
	}
	checkAttempts(t, ps)
	if got := sent.topics(); !slices.Equal(got, []string{TopicIngested}) {
		t.Errorf("published to %v, want only [%s]: a failure is not the handler's to dead-letter", got, TopicIngested)
	}
}

// A bad event (data that is not JSON) is not retried: the handler publishes it to its
// consumer's own dead-letter topic (ryft-dlq-scoring) with ryft-error and the event's
// attributes and acks it, so the letter is on the hold subscription once and the event is
// not pushed again. The source subscription dead-letters to the same topic, as in
// production, so a second path would show as a second letter.
func TestEmulatorBadEvent(t *testing.T) {
	e, ctx := newEmu(t)
	topic, dlq := e.name(TopicIngested), e.name(TopicDLQScoring)
	c, sent := e.client(map[string]string{TopicDLQScoring: dlq})
	var called atomic.Int32
	mux := http.NewServeMux()
	e.push(mux, PushOptions{Consumer: events.ConsumerScoring, DLQ: c}, events.ScoringHandler(func(context.Context, model.IngestedEvent) error {
		called.Add(1)
		return nil
	}))
	srv, pushes := emuServer(t, mux)
	e.topic(ctx, topic)
	e.topic(ctx, dlq)
	hold := e.hold(ctx, dlq)
	sub := e.name("scoring-ingested")
	s := e.pushSub(topic, srv.URL+PushPath(events.KeyIngested), true)
	s.DeadLetterPolicy = &emuDeadLetter{DeadLetterTopic: e.topicPath(dlq), MaxDeliveryAttempts: MaxDeliveryAttempts}
	e.subscribe(ctx, sub, s)

	id, data := "ingest:"+e.id, []byte("not json {"+e.id)
	attrs := map[string]string{AttrKey: events.KeyIngested, AttrMessageID: id, AttrReason: "sync"}
	counted := rise(metrics.EventsDLQ.WithLabelValues(events.KeyIngested, "ok"))
	if _, err := c.Publish(ctx, topic, Message{Data: data, Attributes: attrs, OrderingKey: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	var letters []emuReceived
	// The letter is published inside the push, the push recorded after it: wait for both.
	if !poll(ctx, emuArrive, func() bool {
		letters = append(letters, e.pull(ctx, hold)...)
		return len(letters) > 0 && len(pushes.of(id)) > 0
	}) {
		t.Fatalf("within %s: %d letters on %s, %s", emuArrive, len(letters), hold, pushes)
	}

	ps := pushes.of(id)
	if len(ps) != 1 || ps[0].Status != http.StatusNoContent || !ps[0].Handled || ps[0].Key != events.KeyIngested || !bytes.Equal(ps[0].Data, data) {
		t.Errorf("want one push of the data under %s, answered 204: %s", events.KeyIngested, pushes)
	}
	if n := called.Load(); n != 0 {
		t.Errorf("the typed handler ran %d times on data that does not decode", n)
	}
	l := letters[0].Message
	if !bytes.Equal(l.Data, data) {
		t.Errorf("the letter's data %q, want %q", l.Data, data)
	}
	if why := l.Attributes[AttrError]; why == "" || len(why) > MaxErrorBytes {
		t.Errorf("the letter's %s %q: want the decode error, at most %d bytes", AttrError, why, MaxErrorBytes)
	}
	for k, want := range attrs {
		if l.Attributes[k] != want {
			t.Errorf("the letter's %s %q, want %q", k, l.Attributes[k], want)
		}
	}
	for k := range l.Attributes {
		if strings.HasPrefix(k, deadLetterSource) {
			t.Errorf("the letter has %s: Pub/Sub dead-lettered it, not the handler", k)
		}
	}
	n := 0
	for _, to := range sent.topics() {
		if to == TopicDLQScoring {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d publishes to %s, want 1 (all: %v)", n, TopicDLQScoring, sent.topics())
	}
	if got := counted(); got != 1 {
		t.Errorf("ryft_events_dlq_total{subject=ingested,result=ok} rose by %v, want 1", got)
	}

	if !holds(t, ctx, emuQuiet, func() bool {
		letters = append(letters, e.pull(ctx, hold)...)
		return len(letters) == 1 && len(pushes.of(id)) == 1
	}) {
		t.Fatalf("after the 204: %d letters (want 1); %s", len(letters), pushes)
	}
}

// defer (notify): a handler that returns events.Defer(d) on /push/scored is answered 204
// once the Deferrer stored the message for now+d with its original key, ID, body and
// reason; the 204 acks it.
func TestEmulatorDefer(t *testing.T) {
	e, ctx := newEmu(t)
	topic, dlq := e.name(TopicScored), e.name(TopicDLQNotify)
	c, sent := e.client(map[string]string{TopicScored: topic, TopicDLQNotify: dlq})
	now := time.Date(2026, 11, 14, 22, 30, 0, 0, time.UTC)
	const after = 9*time.Hour + 30*time.Minute // until quiet hours end
	def := &emuDeferrer{}
	mux := http.NewServeMux()
	e.push(mux, PushOptions{Consumer: events.ConsumerNotify, DLQ: c, Deferrer: def, Now: func() time.Time { return now }},
		events.NotifyHandler(func(context.Context, events.NotifyMessage) error { return events.Defer(after) }))
	srv, pushes := emuServer(t, mux)
	e.topic(ctx, topic)
	e.topic(ctx, dlq)
	s := e.pushSub(topic, srv.URL+PushPath(events.KeyScored), false)
	s.DeadLetterPolicy = &emuDeadLetter{DeadLetterTopic: e.topicPath(dlq), MaxDeliveryAttempts: MaxDeliveryAttempts}
	e.subscribe(ctx, e.name("notify-scored"), s)

	m, err := events.Scored(model.ScoredEvent{UserID: uuid.New(), Day: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewBus(c).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	// The deferral is stored inside the push, the push recorded after it: wait for both.
	if !poll(ctx, emuArrive, func() bool { return len(pushes.of(m.ID)) > 0 && len(def.all()) > 0 }) {
		t.Fatalf("within %s: %d deferrals, %s", emuArrive, len(def.all()), pushes)
	}

	if d := pushes.of(m.ID)[0]; d.Status != http.StatusNoContent || d.Key != events.KeyScored {
		t.Errorf("push answered %d under key %q, want 204 under %q", d.Status, d.Key, events.KeyScored)
	}
	got := def.all()
	if len(got) != 1 {
		t.Fatalf("%d deferrals, want 1", len(got))
	}
	g := got[0]
	if g.m.Key != m.Key || g.m.ID != m.ID || !bytes.Equal(g.m.Body, m.Body) || g.m.Reason != m.Reason {
		t.Errorf("deferred key %q, ID %q, body %q, reason %q; want %q, %q, %q, %q", g.m.Key, g.m.ID, g.m.Body, g.m.Reason, m.Key, m.ID, m.Body, m.Reason)
	}
	if !g.at.Equal(now.Add(after)) {
		t.Errorf("deferred until %s, want %s", g.at, now.Add(after))
	}

	if !holds(t, ctx, emuQuiet, func() bool { return len(pushes.of(m.ID)) == 1 && len(def.all()) == 1 }) {
		t.Fatalf("after the 204: %d deferrals (want 1); %s", len(def.all()), pushes)
	}
	if got := sent.topics(); !slices.Equal(got, []string{TopicScored}) {
		t.Errorf("published to %v, want only [%s]", got, TopicScored)
	}
}

// tick: {} with ryft-key=tick on a tick topic is pushed to /internal/tick; the steps run
// once, in order, ryft_ticks_total{result=ok} rises by one, and the 204 acks the tick.
func TestEmulatorTick(t *testing.T) {
	e, ctx := newEmu(t)
	topic := e.name(TopicTick)
	c, _ := e.client(nil)
	svc := "emulator-" + e.id // a ryft_ticks_total label of this test's own
	var mu sync.Mutex
	var ran []string
	step := func(name string) Tick {
		return Tick{Name: name, Run: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, name)
			return nil
		}}
	}
	order := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(ran, ", ")
	}
	h, err := NewTick(TickOptions{Service: svc, Verifier: auth.InsecurePush(quiet), Budget: 10 * time.Second, Log: quiet},
		step("db ping"), step("outbox sweep"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(http.MethodPost+" "+PathTick, h)
	srv, pushes := emuServer(t, mux)
	e.topic(ctx, topic)
	e.subscribe(ctx, e.name("tick-scoring"), e.pushSub(topic, srv.URL+PathTick, false))

	ok := rise(metrics.Ticks.WithLabelValues(svc, "ok"))
	failed := rise(metrics.Ticks.WithLabelValues(svc, "error"))
	overlap := rise(metrics.Ticks.WithLabelValues(svc, "overlap"))
	if _, err := c.Publish(ctx, topic, Message{Data: []byte("{}"), Attributes: map[string]string{AttrKey: KeyTick}}); err != nil {
		t.Fatal(err)
	}
	if !poll(ctx, emuArrive, func() bool { return len(pushes.all()) > 0 }) {
		t.Fatalf("no tick pushed within %s", emuArrive)
	}

	d := pushes.all()[0]
	if d.Path != PathTick || d.Status != http.StatusNoContent || string(d.Env.Message.Data) != "{}" || d.Env.Message.Attributes[AttrKey] != KeyTick {
		t.Errorf("push to %s answered %d with data %q, attributes %v; want %s answered 204, {}, %s=%s",
			d.Path, d.Status, d.Env.Message.Data, d.Env.Message.Attributes, PathTick, AttrKey, KeyTick)
	}
	if got := order(); got != "db ping, outbox sweep" {
		t.Errorf("steps ran %q, want \"db ping, outbox sweep\"", got)
	}
	if got := ok(); got != 1 {
		t.Errorf("ryft_ticks_total{result=ok} rose by %v, want 1", got)
	}

	if !holds(t, ctx, emuQuiet, func() bool { return len(pushes.all()) == 1 }) {
		t.Fatalf("pushed again after a 204: %s", pushes)
	}
	if got := order(); got != "db ping, outbox sweep" {
		t.Errorf("by the end the steps ran %q, want each once", got)
	}
	if o, f, x := ok(), failed(), overlap(); o != 1 || f != 0 || x != 0 {
		t.Errorf("ryft_ticks_total rose by ok %v, error %v, overlap %v; want 1, 0, 0", o, f, x)
	}
}

// checkAttempts pins deliveryAttempt across the pushes of one message: on every push,
// rising from 1, the handler told of a redelivery from the second on; or (what the
// emulator may do) on none, each push then a first delivery for the handler.
func checkAttempts(t *testing.T, ps []delivery) {
	t.Helper()
	var seq []int
	for _, p := range ps {
		if p.Env.DeliveryAttempt != nil {
			seq = append(seq, *p.Env.DeliveryAttempt)
		}
	}
	switch {
	case len(seq) == 0:
		t.Logf("emulator: no deliveryAttempt on any of %d pushes of a subscription with a dead-letter policy; the handler counts each as attempt 1", len(ps))
		for i, p := range ps {
			if p.Redelivered {
				t.Errorf("push %d has no deliveryAttempt but reached the handler as a redelivery", i+1)
			}
		}
	case len(seq) < len(ps):
		t.Errorf("deliveryAttempt on %d of %d pushes (%v): want it on every push or on none", len(seq), len(ps), seq)
	default:
		t.Logf("emulator: deliveryAttempt across the pushes: %v", seq)
		for i, a := range seq {
			if (i == 0 && a != 1) || (i > 0 && a <= seq[i-1]) {
				t.Errorf("deliveryAttempt %v: want 1, then rising with every push", seq)
				break
			}
		}
		for i, p := range ps {
			if p.Redelivered != (seq[i] > 1) {
				t.Errorf("push %d (deliveryAttempt %d) reached the handler with redelivered %v", i+1, seq[i], p.Redelivered)
			}
		}
	}
}

// emuUp waits, once per test binary and up to a minute, for the emulator to answer: CI
// starts it in the background shortly before the tests.
var emuUp = sync.OnceValue(func() error {
	host := os.Getenv(emuHostEnv)
	c := &http.Client{Timeout: 2 * time.Second}
	var last error
	if poll(context.Background(), time.Minute, func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+host+"/", nil)
		if err != nil {
			last = err
			return false
		}
		resp, err := c.Do(req)
		if err != nil {
			last = err
			return false
		}
		_ = resp.Body.Close()
		return true
	}) {
		return nil
	}
	return fmt.Errorf("the emulator at %s does not answer within a minute: %w", host, last)
})

// emu is one test's emulator: its project, its name suffix and a REST client for the
// emulator's admin calls (the transport's Client is what is under test; this is not).
type emu struct {
	t       *testing.T
	host    string
	project string
	id      string // the suffix of every name this test makes
	http    *http.Client
}

// newEmu skips the test without an emulator; else it makes the test parallel, waits for
// the emulator and returns it with the test's budget.
func newEmu(t *testing.T) (*emu, context.Context) {
	t.Helper()
	host := os.Getenv(emuHostEnv)
	if host == "" {
		t.Skip(emuHostEnv + " is unset: the Pub/Sub emulator tests run in CI's test job, or against an emulator at that host:port")
	}
	t.Parallel()
	if err := emuUp(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), emuBudget)
	t.Cleanup(cancel)
	id := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	return &emu{t: t, host: host, project: emuProject, id: id, http: &http.Client{Timeout: 10 * time.Second}}, ctx
}

// name is base with this test's suffix.
func (e *emu) name(base string) string { return base + "-" + e.id }

func (e *emu) topicPath(name string) string { return "projects/" + e.project + "/topics/" + name }

func (e *emu) subPath(name string) string { return "projects/" + e.project + "/subscriptions/" + name }

// call sends one REST request to the emulator (path below /v1/projects/<project>/) and
// returns its status and body.
func (e *emu) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+e.host+"/v1/projects/"+e.project+"/"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}

// create PUTs a topic or a subscription ("topics/<name>", "subscriptions/<name>"),
// which must be new, and deletes it when the test ends.
func (e *emu) create(ctx context.Context, path string, body any) {
	e.t.Helper()
	code, raw, err := e.call(ctx, http.MethodPut, path, body)
	if err != nil || code != http.StatusOK {
		e.t.Fatalf("PUT %s: HTTP %d%s %v", path, code, googleError(raw), err)
	}
	e.t.Cleanup(func() { e.remove(path) })
}

// remove deletes a topic or a subscription, best effort: it runs in cleanups, after
// the test's context has ended.
func (e *emu) remove(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = e.call(ctx, http.MethodDelete, path, nil)
}

// get GETs path into out; anything but 200 fails the test.
func (e *emu) get(ctx context.Context, path string, out any) {
	e.t.Helper()
	code, raw, err := e.call(ctx, http.MethodGet, path, nil)
	if err != nil || code != http.StatusOK {
		e.t.Fatalf("GET %s: HTTP %d%s %v", path, code, googleError(raw), err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		e.t.Fatalf("GET %s: %v in %s", path, err, raw)
	}
}

func (e *emu) topic(ctx context.Context, name string) {
	e.t.Helper()
	e.create(ctx, "topics/"+name, struct{}{})
}

func (e *emu) subscribe(ctx context.Context, name string, s emuSub) {
	e.t.Helper()
	e.create(ctx, "subscriptions/"+name, s)
}

// pushSub is a push subscription of topic to endpoint as these tests make them: Pub/Sub's
// minimum ack deadline and a retry policy of a second or two.
func (e *emu) pushSub(topic, endpoint string, ordered bool) emuSub {
	return emuSub{Topic: e.topicPath(topic), PushConfig: &emuPushConfig{PushEndpoint: endpoint}, Ordered: ordered,
		AckDeadline: emuAck, RetryPolicy: &emuRetry{MinimumBackoff: emuRetryMin, MaximumBackoff: emuRetryMax}}
}

// hold makes the pull subscription of a dead-letter topic. It must exist before the
// first publish: a letter to a topic without a subscription is lost.
func (e *emu) hold(ctx context.Context, topic string) string {
	e.t.Helper()
	name := topic + "-hold"
	e.subscribe(ctx, name, emuSub{Topic: e.topicPath(topic), AckDeadline: emuAck})
	return name
}

// pull takes what sub holds right now and acks it at once, so a letter is never seen
// twice because its lease ran out.
func (e *emu) pull(ctx context.Context, sub string) []emuReceived {
	e.t.Helper()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, raw, err := e.call(pctx, http.MethodPost, "subscriptions/"+sub+":pull", map[string]any{"maxMessages": 10, "returnImmediately": true})
	switch {
	case err != nil && ctx.Err() == nil && pctx.Err() != nil:
		return nil // a long poll cut by the 5 s: nothing yet
	case err != nil || code != http.StatusOK:
		e.t.Fatalf("pull %s: HTTP %d%s %v", sub, code, googleError(raw), err)
	}
	var out struct {
		Received []emuReceived `json:"receivedMessages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("pull %s: %v in %s", sub, err, raw)
	}
	if len(out.Received) == 0 {
		return nil
	}
	ids := make([]string, len(out.Received))
	for i, r := range out.Received {
		ids[i] = r.AckID
	}
	code, raw, err = e.call(ctx, http.MethodPost, "subscriptions/"+sub+":acknowledge", map[string]any{"ackIds": ids})
	if err != nil || code != http.StatusOK {
		e.t.Fatalf("acknowledge on %s: HTTP %d%s %v", sub, code, googleError(raw), err)
	}
	return out.Received
}

// client is the transport's Client on the emulator in this test's project, its
// publishes to the topology's topics in rename sent to this test's topics instead.
func (e *emu) client(rename map[string]string) (*Client, *renamer) {
	e.t.Helper()
	rn := &renamer{prefix: "/v1/projects/" + e.project + "/topics/", to: rename}
	c, err := NewClient(Options{Project: e.project, EmulatorHost: e.host, HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: rn}})
	if err != nil {
		e.t.Fatal(err)
	}
	return c, rn
}

// push serves the consumer's push routes on mux: fn behind tap, the insecure verifier
// (the emulator sends no token) and a quiet log unless o sets them.
func (e *emu) push(mux *http.ServeMux, o PushOptions, fn events.Handler) {
	e.t.Helper()
	if o.Verifier == nil {
		o.Verifier = auth.InsecurePush(quiet)
	}
	if o.Log == nil {
		o.Log = quiet
	}
	p, err := NewPush(o, tap(fn))
	if err != nil {
		e.t.Fatal(err)
	}
	p.Register(mux)
}

// diagnose says what the emulator refuses when init.sh stops: it sends every field
// init.sh sends, each alone and then all of an ordered push subscription's together, to
// a new probe subscription, and lists the answers.
func (e *emu) diagnose(top Topology) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var b strings.Builder
	b.WriteString("\nThe emulator's answer to each field init.sh sends, on a new subscription:")
	topic, dlq := "probe-"+e.id, "probe-dlq-"+e.id
	for _, x := range []string{topic, dlq} {
		code, raw, err := e.call(ctx, http.MethodPut, "topics/"+x, struct{}{})
		if err != nil || (code != http.StatusOK && code != http.StatusConflict) {
			fmt.Fprintf(&b, "\n  PUT topics/%s {}: HTTP %d%s %v", x, code, googleError(raw), err)
			return b.String()
		}
		defer e.remove("topics/" + x)
	}
	type probe struct {
		field string
		value any
	}
	acks, rets := map[int64]bool{seconds(HoldAckDeadline): true}, map[int64]bool{}
	for _, s := range top.Push {
		acks[seconds(s.AckDeadline)] = true
		rets[seconds(s.Retention)] = true
	}
	for _, h := range top.Hold {
		rets[seconds(h.Retention)] = true
	}
	retry := emuRetry{MinimumBackoff: fmt.Sprintf("%ds", seconds(RetryMinimumBackoff)), MaximumBackoff: fmt.Sprintf("%ds", seconds(RetryMaximumBackoff))}
	dead := emuDeadLetter{DeadLetterTopic: e.topicPath(dlq), MaxDeliveryAttempts: MaxDeliveryAttempts}
	endpoint := emuPushConfig{PushEndpoint: "http://" + ServiceScoring + ":8080" + PushPath(events.KeyIngested)}
	probes := []probe{{"pushConfig", endpoint}, {"enableMessageOrdering", true}}
	for _, a := range slices.Sorted(maps.Keys(acks)) {
		probes = append(probes, probe{"ackDeadlineSeconds", a})
	}
	for _, r := range slices.Sorted(maps.Keys(rets)) {
		probes = append(probes, probe{"messageRetentionDuration", fmt.Sprintf("%ds", r)})
	}
	probes = append(probes, probe{"retryPolicy", retry}, probe{"deadLetterPolicy", dead})
	for i, p := range probes {
		v, _ := json.Marshal(p.value)
		sub := fmt.Sprintf("probe-%d-%s", i, e.id)
		code, raw, err := e.call(ctx, http.MethodPut, "subscriptions/"+sub, map[string]any{"topic": e.topicPath(topic), p.field: p.value})
		fmt.Fprintf(&b, "\n  %s %s: HTTP %d%s", p.field, v, code, googleError(raw))
		if err != nil {
			fmt.Fprintf(&b, " (%v)", err)
		}
		e.remove("subscriptions/" + sub)
	}
	i := slices.IndexFunc(top.Push, func(s PushSubscription) bool { return s.Ordered && s.DeadLetterTopic != "" })
	if i < 0 {
		return b.String()
	}
	s := top.Push[i]
	all := emuSub{Topic: e.topicPath(topic), PushConfig: &emuPushConfig{PushEndpoint: emulatorEndpoint(s)}, Ordered: true,
		AckDeadline: int(seconds(s.AckDeadline)), Retention: fmt.Sprintf("%ds", seconds(s.Retention)), RetryPolicy: &retry, DeadLetterPolicy: &dead}
	sub := "probe-all-" + e.id
	code, raw, err := e.call(ctx, http.MethodPut, "subscriptions/"+sub, all)
	fmt.Fprintf(&b, "\n  all of %s's fields together: HTTP %d%s", s.Name, code, googleError(raw))
	if err != nil {
		fmt.Fprintf(&b, " (%v)", err)
	}
	e.remove("subscriptions/" + sub)
	return b.String()
}

// emuSub is a subscription in the REST API's JSON: what the tests and init.sh send and
// what the emulator reports back (an absent field reads as its zero value).
type emuSub struct {
	Topic            string         `json:"topic"`
	PushConfig       *emuPushConfig `json:"pushConfig,omitempty"`
	Ordered          bool           `json:"enableMessageOrdering,omitempty"`
	AckDeadline      int            `json:"ackDeadlineSeconds,omitempty"`
	Retention        string         `json:"messageRetentionDuration,omitempty"`
	RetryPolicy      *emuRetry      `json:"retryPolicy,omitempty"`
	DeadLetterPolicy *emuDeadLetter `json:"deadLetterPolicy,omitempty"`
}

type emuPushConfig struct {
	PushEndpoint string `json:"pushEndpoint"`
}

type emuRetry struct {
	MinimumBackoff string `json:"minimumBackoff"`
	MaximumBackoff string `json:"maximumBackoff"`
}

type emuDeadLetter struct {
	DeadLetterTopic     string `json:"deadLetterTopic"`
	MaxDeliveryAttempts int    `json:"maxDeliveryAttempts,omitempty"`
}

// emulatorEndpoint is where init.sh points a push subscription: compose's service at
// http://<service>:8080<path>.
func emulatorEndpoint(s PushSubscription) string {
	return "http://" + s.Service + ":8080" + s.Path
}

func endpointOf(s emuSub) string {
	if s.PushConfig == nil {
		return ""
	}
	return s.PushConfig.PushEndpoint
}

// appendNotKept appends where's field to list when the emulator reports it otherwise
// than sent (absent: dropped).
func appendNotKept(list []string, where, field, got string, want time.Duration) []string {
	if got == "" {
		return append(list, fmt.Sprintf("%s: %s dropped (sent %ds)", where, field, seconds(want)))
	}
	if d, err := time.ParseDuration(got); err != nil || d != want {
		return append(list, fmt.Sprintf("%s: %s %q (sent %ds)", where, field, got, seconds(want)))
	}
	return list
}

// emuMessage is a message as Pub/Sub sends it in a push envelope and a pull answer. The
// field names are Google's documented ones, not the transport's types, so a wrong tag
// there cannot hide here.
type emuMessage struct {
	Data        []byte            `json:"data"`
	Attributes  map[string]string `json:"attributes"`
	MessageID   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
	OrderingKey string            `json:"orderingKey"`
}

type emuEnvelope struct {
	Message         emuMessage `json:"message"`
	Subscription    string     `json:"subscription"`
	DeliveryAttempt *int       `json:"deliveryAttempt"`
}

type emuReceived struct {
	AckID   string     `json:"ackId"`
	Message emuMessage `json:"message"`
}

// delivery is one push the emulator made: the envelope as it came, the answer, and what
// the event handler was given (nothing when the push was settled before it ran).
type delivery struct {
	At     time.Time
	Path   string
	Raw    []byte
	Env    emuEnvelope
	EnvErr error // the body is not a push envelope as Google documents it
	Status int

	Handled     bool
	Key         string
	Data        []byte
	Redelivered bool
}

// pushLog records the pushes a test's server answered.
type pushLog struct {
	start time.Time
	mu    sync.Mutex
	got   []delivery
}

type tapKey struct{}

// record is h with every push it answers recorded, after h answered it.
func (l *pushLog) record(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxPushBody+1))
		d := &delivery{At: time.Now(), Path: r.URL.Path, Raw: raw}
		if err == nil {
			err = json.Unmarshal(raw, &d.Env)
		}
		d.EnvErr = err
		r.Body = io.NopCloser(bytes.NewReader(raw))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), tapKey{}, d)))
		d.Status = sw.status
		l.mu.Lock()
		l.got = append(l.got, *d)
		l.mu.Unlock()
	})
}

func (l *pushLog) all() []delivery {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

// of are the pushes of the message whose ryft-message-id is id.
func (l *pushLog) of(id string) []delivery {
	var out []delivery
	for _, d := range l.all() {
		if d.Env.Message.Attributes[AttrMessageID] == id {
			out = append(out, d)
		}
	}
	return out
}

// String is what a failure shows: every push, its answer and its deliveryAttempt.
func (l *pushLog) String() string {
	ds := l.all()
	if len(ds) == 0 {
		return "no push arrived"
	}
	parts := make([]string, len(ds))
	for i, d := range ds {
		a := "none"
		if d.Env.DeliveryAttempt != nil {
			a = strconv.Itoa(*d.Env.DeliveryAttempt)
		}
		parts[i] = fmt.Sprintf("%s → %d (deliveryAttempt %s, +%s)", d.Path, d.Status, a, d.At.Sub(l.start).Round(10*time.Millisecond))
	}
	return fmt.Sprintf("%d pushes: %s", len(ds), strings.Join(parts, "; "))
}

// tap is fn, recording on its push what it was given.
func tap(fn events.Handler) events.Handler {
	return func(ctx context.Context, key string, data []byte) error {
		if d, ok := ctx.Value(tapKey{}).(*delivery); ok {
			d.Handled, d.Key, d.Data, d.Redelivered = true, key, bytes.Clone(data), events.Redelivered(ctx)
		}
		return fn(ctx, key, data)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

// emuServer serves h on 127.0.0.1 (the emulator reaches it over the host network),
// recording every push, until the test ends. Make it before the subscriptions: cleanups
// run last first, so they are deleted before it closes.
func emuServer(t *testing.T, h http.Handler) (*httptest.Server, *pushLog) {
	t.Helper()
	l := &pushLog{start: time.Now()}
	srv := httptest.NewServer(l.record(h))
	t.Cleanup(srv.Close)
	return srv, l
}

// renamer is the Client's transport: a publish to one of the topology's topics in to
// goes to this test's topic of that name instead, and it keeps the topic of every
// publish by the name the transport used (the Bus publishes by key to fixed names, a bad
// event goes to its consumer's fixed DLQ topic; the test's names stay its own), and the
// ordering key of every message published.
type renamer struct {
	prefix string            // /v1/projects/<project>/topics/
	to     map[string]string // the topology's name → this test's
	mu     sync.Mutex
	sent   []string
	keys   []string
}

func (r *renamer) RoundTrip(req *http.Request) (*http.Response, error) {
	if rest, ok := strings.CutPrefix(req.URL.Path, r.prefix); ok {
		if topic, ok := strings.CutSuffix(rest, ":publish"); ok {
			var body struct {
				Messages []struct {
					OrderingKey string `json:"orderingKey"`
				} `json:"messages"`
			}
			if req.Body != nil {
				raw, err := io.ReadAll(req.Body)
				_ = req.Body.Close()
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					return nil, fmt.Errorf("the publish body is not JSON: %w", err)
				}
				req = req.Clone(req.Context())
				req.Body = io.NopCloser(bytes.NewReader(raw))
				req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
			}
			r.mu.Lock()
			r.sent = append(r.sent, topic)
			for _, m := range body.Messages {
				r.keys = append(r.keys, m.OrderingKey)
			}
			r.mu.Unlock()
			if mine, ok := r.to[topic]; ok {
				req = req.Clone(req.Context())
				req.URL.Path, req.URL.RawPath = r.prefix+mine+":publish", ""
			}
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (r *renamer) topics() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

// orderingKeys are the published messages' ordering keys, "" for none, in order.
func (r *renamer) orderingKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.keys)
}

// emuDeferrer stands in for notify's outbox, safe for the server's goroutines.
type emuDeferrer struct {
	mu  sync.Mutex
	got []emuDeferral
}

type emuDeferral struct {
	m  events.Message
	at time.Time
}

func (d *emuDeferrer) SendAt(_ context.Context, m events.Message, at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m.Body = bytes.Clone(m.Body)
	d.got = append(d.got, emuDeferral{m, at})
	return nil
}

func (d *emuDeferrer) all() []emuDeferral {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.got)
}

// poll checks done every emuPoll until it holds (true), or within has passed or ctx
// ended (false): how these tests wait, never a blind sleep.
func poll(ctx context.Context, within time.Duration, done func() bool) bool {
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(emuPoll)
	defer tick.Stop()
	for {
		if done() {
			return true
		}
		select {
		case <-ctx.Done():
			return done()
		case <-deadline.C:
			return done()
		case <-tick.C:
		}
	}
}

// holds checks ok every emuPoll for all of within: false as soon as it does not hold.
// The test's budget must outlast within.
func holds(t *testing.T, ctx context.Context, within time.Duration, ok func() bool) bool {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(emuPoll)
	defer tick.Stop()
	for {
		if !ok() {
			return false
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the test's budget (%s) ended inside a %s wait", emuBudget, within)
		case <-deadline.C:
			return ok()
		case <-tick.C:
		}
	}
}

// repoFile is rel (a path from the repository root), found from the test's directory
// upwards, so the test does not depend on how deep this package sits.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("%s is in no directory above %s", rel, wd)
		}
		dir = parent
	}
}

// runInit runs init.sh against the emulator at host in project.
func runInit(ctx context.Context, script, host, project string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "sh", script)
	cmd.Env = append(os.Environ(), "PUBSUB_EMULATOR_HOST="+host, "GCP_PROJECT="+project)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// countLines counts the lines of out that begin with prefix.
func countLines(out, prefix string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}
