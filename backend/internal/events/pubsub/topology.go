package pubsub

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/events"
)

// The topology (D218), defined once here: cmd/pubsub-topology writes it as
// infra/gcp/pubsub.auto.tfvars.json (Terraform creates it in ryftlab-prod) and as
// deploy/pubsub-emulator/init.sh (compose's emulator), and the client and the push
// handlers take their names from it.
//
//	ryft-ingested, ryft-session-completed   api, ingest → scoring (/push/<key>, ordered by user)
//	ryft-scored, ryft-session-scored,
//	ryft-refresh                             scoring → notify (/push/<key>)
//	ryft-notify-wait                         notify's deferrals → notify (/push/wait, ryft-key = the event's)
//	ryft-tick                                Cloud Scheduler → every service (/internal/tick)
//	ryft-dlq-scoring, ryft-dlq-notify        dead letters, held 14 days for scripts/dlq.sh
//
// One topic per key keeps D99's publish matrix: Pub/Sub grants publishing per topic,
// so ingest cannot publish a session.

// Topic names.
const (
	TopicIngested         = "ryft-ingested"
	TopicSessionCompleted = "ryft-session-completed"
	TopicScored           = "ryft-scored"
	TopicSessionScored    = "ryft-session-scored"
	TopicRefresh          = "ryft-refresh"
	TopicNotifyWait       = "ryft-notify-wait"
	TopicTick             = "ryft-tick"
	TopicDLQScoring       = "ryft-dlq-scoring"
	TopicDLQNotify        = "ryft-dlq-notify"
)

// The services, as Terraform and Cloud Run name them.
const (
	ServiceAPI     = "api"
	ServiceIngest  = "ingest"
	ServiceScoring = "scoring"
	ServiceNotify  = "notify"
)

// Routes.
const (
	PathTick   = "/internal/tick" // every service, the tick
	PathWait   = "/push/wait"     // notify, its deferrals
	pushPrefix = "/push/"
)

// KeyTick is the ryft-key of a tick (Cloud Scheduler sets it; it may also be absent).
const KeyTick = "tick"

// Message attributes on the wire (design 2.1).
const (
	AttrKey       = "ryft-key"        // the event key
	AttrMessageID = "ryft-message-id" // events.Message.ID: stable for the same trigger, not a dedup key (D100)
	AttrReason    = "ryft-reason"     // events.Message.Reason
	AttrReplayed  = "ryft-replayed"   // "true" on a dead letter scripts/dlq.sh put back
	AttrError     = "ryft-error"      // only on a letter a consumer dead-lettered itself: why, at most MaxErrorBytes
)

// MaxErrorBytes bounds the ryft-error attribute.
const MaxErrorBytes = 512

// The delivery policy every push subscription gets. Terraform holds the same constants
// (infra/gcp/pubsub.tf, D218) and the emulator's init script is written with these; a
// test reads pubsub.tf so the two cannot drift.
const (
	RetryMinimumBackoff = 10 * time.Second
	RetryMaximumBackoff = 600 * time.Second
	MaxDeliveryAttempts = 20
)

// HoldAckDeadline is the hold subscriptions' ack deadline: a replay (pull, republish,
// ack) fits in one lease (Terraform hard-codes the same).
const HoldAckDeadline = 60 * time.Second

// Retention and ack deadlines.
const (
	eventRetention = 7 * 24 * time.Hour  // an undelivered event is kept a week
	tickRetention  = 10 * time.Minute    // a failed tick is dropped; the next one comes 15 minutes later
	dlqRetention   = 14 * 24 * time.Hour // a dead letter is kept 14 days
	scoringAck     = 300 * time.Second   // = scoring's Cloud Run request timeout
	notifyAck      = 60 * time.Second
	tickAck        = 600 * time.Second
)

// Topic is one topic and the services that may publish to it (roles/pubsub.publisher;
// Terraform adds the service agents itself).
type Topic struct {
	Name       string
	Publishers []string // sorted; empty for the tick (the Scheduler agent publishes)
	Key        string   // the event key it carries; "" for the wait, tick and dead-letter topics
	Ordered    bool     // published with ordering key = the event's user_id
}

// PushSubscription pushes Topic to Service's Path, with an OIDC token of the push
// service account.
type PushSubscription struct {
	Name            string
	Topic           string
	Service         string
	Path            string
	Ordered         bool
	AckDeadline     time.Duration
	Retention       time.Duration
	DeadLetterTopic string // "" for none (the ticks)
}

// HoldSubscription is a pull subscription that keeps a dead-letter topic's letters for
// the operator.
type HoldSubscription struct {
	Name      string
	Topic     string
	Retention time.Duration
}

// Topology is every topic and subscription.
type Topology struct {
	Topics []Topic
	Push   []PushSubscription
	Hold   []HoldSubscription
}

// consumers are the two event consumers: their keys, their dead-letter topic and how
// long Pub/Sub waits for their answer.
var consumers = []struct {
	name string
	keys []string
	dlq  string
	ack  time.Duration
}{
	{events.ConsumerScoring, events.ScoringKeys, TopicDLQScoring, scoringAck},
	{events.ConsumerNotify, events.NotifyKeys, TopicDLQNotify, notifyAck},
}

// eventTopics are the five event keys' topics, with who publishes each (D99).
var eventTopics = []Topic{
	{Name: TopicIngested, Key: events.KeyIngested, Publishers: []string{ServiceAPI, ServiceIngest}, Ordered: true},
	{Name: TopicSessionCompleted, Key: events.KeySessionCompleted, Publishers: []string{ServiceAPI}, Ordered: true},
	{Name: TopicScored, Key: events.KeyScored, Publishers: []string{ServiceScoring}},
	{Name: TopicSessionScored, Key: events.KeySessionScored, Publishers: []string{ServiceScoring}},
	{Name: TopicRefresh, Key: events.KeyRefresh, Publishers: []string{ServiceScoring}},
}

// TickServices are the services the tick reaches, a subscription each.
var TickServices = []string{ServiceAPI, ServiceIngest, ServiceScoring, ServiceNotify}

// Spec is the topology.
func Spec() Topology {
	t := Topology{Topics: slices.Clone(eventTopics)}
	t.Topics = append(t.Topics,
		Topic{Name: TopicNotifyWait, Publishers: []string{ServiceNotify}},
		Topic{Name: TopicTick, Publishers: []string{}},
	)
	for _, c := range consumers {
		t.Topics = append(t.Topics, Topic{Name: c.dlq, Publishers: []string{c.name}})
		for _, k := range c.keys {
			top, _ := topicOfKey(k)
			t.Push = append(t.Push, PushSubscription{
				Name: c.name + "-" + strings.ReplaceAll(k, ".", "-"), Topic: top.Name, Service: c.name, Path: PushPath(k),
				Ordered: top.Ordered, AckDeadline: c.ack, Retention: eventRetention, DeadLetterTopic: c.dlq,
			})
		}
		t.Hold = append(t.Hold, HoldSubscription{Name: "dlq-" + c.name + "-hold", Topic: c.dlq, Retention: dlqRetention})
	}
	t.Push = append(t.Push, PushSubscription{Name: "notify-wait", Topic: TopicNotifyWait, Service: ServiceNotify, Path: PathWait,
		AckDeadline: notifyAck, Retention: eventRetention, DeadLetterTopic: TopicDLQNotify})
	for _, svc := range TickServices {
		t.Push = append(t.Push, PushSubscription{Name: "tick-" + svc, Topic: TopicTick, Service: svc, Path: PathTick,
			AckDeadline: tickAck, Retention: tickRetention})
	}
	return t
}

// PushPath is the push route of an event key: /push/<key>.
func PushPath(key string) string { return pushPrefix + key }

func topicOfKey(key string) (Topic, bool) {
	i := slices.IndexFunc(eventTopics, func(t Topic) bool { return t.Key == key })
	if i < 0 {
		return Topic{}, false
	}
	return eventTopics[i], true
}

// TopicFor is the topic an event key is published to; false for a key without one.
func TopicFor(key string) (string, bool) {
	t, ok := topicOfKey(key)
	return t.Name, ok
}

// KeyOfPath is the event key a push path carries (/push/ingested → ingested); false for
// /push/wait, whose key is the message's, and for any other path.
func KeyOfPath(path string) (string, bool) {
	k, ok := strings.CutPrefix(path, pushPrefix)
	if !ok {
		return "", false
	}
	_, known := topicOfKey(k)
	return k, known
}

// Ordered reports whether topic is published with an ordering key (scoring's two).
func Ordered(topic string) bool {
	return slices.ContainsFunc(eventTopics, func(t Topic) bool { return t.Name == topic && t.Ordered })
}

// DLQTopic is consumer's dead-letter topic; false for one that is not a consumer.
func DLQTopic(consumer string) (string, bool) {
	for _, c := range consumers {
		if c.name == consumer {
			return c.dlq, true
		}
	}
	return "", false
}

// consumerKeys are the keys consumer handles.
func consumerKeys(consumer string) []string {
	for _, c := range consumers {
		if c.name == consumer {
			return c.keys
		}
	}
	return nil
}

// isTopic reports whether name is a topic of the topology.
func isTopic(name string) bool {
	return slices.ContainsFunc(Spec().Topics, func(t Topic) bool { return t.Name == name })
}

// PushPaths are the push routes service serves for its consumer (not the tick), in the
// topology's order.
func PushPaths(service string) []string {
	var out []string
	for _, s := range Spec().Push {
		if s.Service == service && strings.HasPrefix(s.Path, pushPrefix) {
			out = append(out, s.Path)
		}
	}
	return out
}

// Validate checks the topology is closed: names unique, every subscription's topic and
// dead-letter topic exist, a dead-letter topic is not its own source, every event key of
// a consumer is pushed to that consumer, and publishers are sorted.
func (t Topology) Validate() error {
	topics := map[string]Topic{}
	for _, x := range t.Topics {
		if _, dup := topics[x.Name]; dup {
			return fmt.Errorf("topic %s twice", x.Name)
		}
		if !slices.IsSorted(x.Publishers) || x.Publishers == nil {
			return fmt.Errorf("topic %s: publishers must be a sorted list, not nil", x.Name)
		}
		topics[x.Name] = x
	}
	subs := map[string]bool{}
	for _, s := range t.Push {
		if subs[s.Name] {
			return fmt.Errorf("subscription %s twice", s.Name)
		}
		subs[s.Name] = true
		top, ok := topics[s.Topic]
		switch {
		case !ok:
			return fmt.Errorf("subscription %s: no topic %s", s.Name, s.Topic)
		case s.DeadLetterTopic != "" && (s.DeadLetterTopic == s.Topic || topics[s.DeadLetterTopic].Name == ""):
			return fmt.Errorf("subscription %s: dead-letter topic %q must exist and differ from its topic", s.Name, s.DeadLetterTopic)
		case s.Ordered != top.Ordered:
			return fmt.Errorf("subscription %s: ordered must be its topic's", s.Name)
		case s.AckDeadline < 10*time.Second || s.AckDeadline > 600*time.Second:
			return fmt.Errorf("subscription %s: ack deadline %s outside Pub/Sub's 10-600 s", s.Name, s.AckDeadline)
		case s.Retention < 10*time.Minute || s.Retention > 7*24*time.Hour:
			return fmt.Errorf("subscription %s: retention %s outside 10 min-7 days", s.Name, s.Retention)
		}
	}
	for _, h := range t.Hold {
		if subs[h.Name] {
			return fmt.Errorf("subscription %s twice", h.Name)
		}
		subs[h.Name] = true
		if _, ok := topics[h.Topic]; !ok {
			return fmt.Errorf("hold subscription %s: no topic %s", h.Name, h.Topic)
		}
	}
	for _, c := range consumers {
		for _, k := range c.keys {
			top, ok := topicOfKey(k)
			if !ok {
				return fmt.Errorf("key %s of %s has no topic", k, c.name)
			}
			if !slices.ContainsFunc(t.Push, func(s PushSubscription) bool {
				return s.Topic == top.Name && s.Service == c.name && s.Path == PushPath(k) && s.DeadLetterTopic == c.dlq
			}) {
				return fmt.Errorf("key %s is not pushed to %s at %s", k, c.name, PushPath(k))
			}
		}
	}
	return nil
}
