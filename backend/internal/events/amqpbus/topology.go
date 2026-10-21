package amqpbus

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/events"
)

// The broker's topology (D99), defined once here: the Topology Operator's CRs in
// deploy/rabbitmq/topology and the definitions compose and the tests load are
// generated from it (cmd/rabbitmq-topology; make k8s-validate checks they match), and
// the client takes its exchange, queue and tier names from it.
//
//	ryft.events  (topic)   publishers → scoring  [ingested, session.completed]
//	                                  → notify   [scored, session.scored, refresh]
//	ryft.retry   (topic)   a consumer's failed or deferred message → <queue>.retry.<tier>
//	                       or <queue>.wait.<tier>, which holds it for the tier's TTL
//	ryft.requeue (direct)  the retry queues dead-letter here when the TTL ends → back to
//	                       the queue it came from (written only by the broker)
//	ryft.dlx     (topic)   what cannot be handled → dlq.<queue>, 14 days
//
// Every queue is a quorum queue (three replicas). Delivery limits, TTLs and
// dead-lettering are policies, not queue arguments: the Topology Operator cannot
// change a queue's arguments after it exists.
const (
	Vhost           = "ryft"
	ExchangeEvents  = "ryft.events"
	ExchangeRetry   = "ryft.retry"
	ExchangeRequeue = "ryft.requeue"
	ExchangeDLX     = "ryft.dlx"
	ClusterName     = "rabbitmq" // the RabbitmqCluster (deploy/rabbitmq/cluster)
)

// Tier is how long a message waits before it is handled again.
type Tier struct {
	Name  string
	Delay time.Duration
}

// RetryTiers follow a failure: the first retry after 5 s, the second after 30 s,
// every further one after 5 minutes, until MaxAttempts.
var RetryTiers = []Tier{{"5s", 5 * time.Second}, {"30s", 30 * time.Second}, {"5m", 5 * time.Minute}}

// WaitTiers hold notify's deferrals (quiet hours): the longest tier that fits, again
// and again until the handler stops deferring.
var WaitTiers = []Tier{{"1m", time.Minute}, {"15m", 15 * time.Minute}, {"1h", time.Hour}}

// MaxAttempts is how often a message is handled before it is dead-lettered: at 0,
// +5 s, +30 s, then every 5 minutes, about 16 minutes in all.
const MaxAttempts = 6

// DeliveryLimit is the broker's own limit for a queue's message (redeliveries after a
// consumer died with it unacknowledged, or rejected it); then the broker
// dead-letters it. Retries through the tiers are acknowledged copies and do not
// count.
const DeliveryLimit = 10

// DLQTTL is how long a dead letter is kept.
const DLQTTL = 14 * 24 * time.Hour

// maxLength bounds every queue; a full queue refuses publishes (reject-publish), which
// the publisher sees as a failed confirm and retries from the outbox.
const maxLength = 100000

// Consumer queues and the keys bound to them.
var consumerKeys = map[string][]string{
	events.ConsumerScoring: events.ScoringKeys,
	events.ConsumerNotify:  events.NotifyKeys,
}

// tiersOf are the delay tiers a consumer's queue has: notify also waits.
func tiersOf(consumer string) []Tier {
	if consumer == events.ConsumerNotify {
		return append(append([]Tier{}, RetryTiers...), WaitTiers...)
	}
	return RetryTiers
}

// RetryQueue is the queue that holds consumer's messages for tier t; it is bound to
// ExchangeRetry with its own name as routing key.
func RetryQueue(consumer string, t Tier) string {
	if slices.Contains(WaitTiers, t) {
		return consumer + ".wait." + t.Name
	}
	return consumer + ".retry." + t.Name
}

// DLQ is consumer's dead-letter queue.
func DLQ(consumer string) string { return "dlq." + consumer }

// Grant is one service's rights in the vhost: what it may write, read, and with
// which routing keys on the topic exchanges. No service may configure anything:
// the topology is the operator's.
type Grant struct {
	User   string
	Write  []string          // exchanges
	Read   []string          // queues
	Topics map[string]string // exchange → routing-key regex for writes
}

// Grants are the four services' (D99): each may publish only its own events and
// read only its own queue.
var Grants = []Grant{
	{User: "ingest", Write: []string{ExchangeEvents}, Topics: map[string]string{ExchangeEvents: oneOf(events.KeyIngested)}},
	{User: "api", Write: []string{ExchangeEvents},
		Topics: map[string]string{ExchangeEvents: oneOf(events.KeyIngested, events.KeySessionCompleted)}},
	{User: "scoring", Write: []string{ExchangeEvents, ExchangeRetry, ExchangeDLX}, Read: []string{events.ConsumerScoring},
		Topics: map[string]string{
			ExchangeEvents: oneOf(events.KeyScored, events.KeySessionScored, events.KeyRefresh),
			ExchangeRetry:  "^" + regexp.QuoteMeta(events.ConsumerScoring+"."),
			ExchangeDLX:    oneOf(events.ConsumerScoring),
		}},
	{User: "notify", Write: []string{ExchangeRetry, ExchangeDLX}, Read: []string{events.ConsumerNotify},
		Topics: map[string]string{
			ExchangeRetry: "^" + regexp.QuoteMeta(events.ConsumerNotify+"."),
			ExchangeDLX:   oneOf(events.ConsumerNotify),
		}},
}

// oneOf is a regex matching exactly one of names.
func oneOf(names ...string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = regexp.QuoteMeta(n)
	}
	if len(q) == 1 {
		return "^" + q[0] + "$"
	}
	return "^(" + strings.Join(q, "|") + ")$"
}

// ResourceRegex is the permission regex for a list of exchange or queue names; none
// is "^$" (nothing).
func ResourceRegex(names []string) string {
	if len(names) == 0 {
		return "^$"
	}
	return oneOf(names...)
}

// Exchange, Queue, Binding and Policy describe the topology.
type Exchange struct{ Name, Type string }
type Queue struct{ Name string }
type Binding struct{ Source, Destination, Key string }
type Policy struct {
	Name, Pattern string
	Priority      int
	Definition    map[string]any
}

// Topology is everything the broker needs, the permissions included.
type Topology struct {
	Exchanges []Exchange
	Queues    []Queue
	Bindings  []Binding
	Policies  []Policy
	Grants    []Grant
}

// Spec is the topology. scale shortens every TTL (1 in production; the integration
// tests use a small fraction so a retry takes milliseconds).
func Spec(scale float64) Topology {
	ms := func(d time.Duration) int64 { return max(int64(float64(d.Milliseconds())*scale), 1) }
	t := Topology{
		Exchanges: []Exchange{{ExchangeEvents, "topic"}, {ExchangeRetry, "topic"}, {ExchangeRequeue, "direct"}, {ExchangeDLX, "topic"}},
		Grants:    Grants,
	}
	for _, c := range []string{events.ConsumerScoring, events.ConsumerNotify} {
		t.Queues = append(t.Queues, Queue{c})
		for _, k := range consumerKeys[c] {
			t.Bindings = append(t.Bindings, Binding{ExchangeEvents, c, k})
		}
		t.Bindings = append(t.Bindings, Binding{ExchangeRequeue, c, c})
		t.Policies = append(t.Policies, Policy{Name: "work-" + c, Pattern: "^" + regexp.QuoteMeta(c) + "$", Priority: 10,
			Definition: map[string]any{
				"delivery-limit":          DeliveryLimit,
				"dead-letter-exchange":    ExchangeDLX,
				"dead-letter-routing-key": c,
				"dead-letter-strategy":    "at-least-once",
				"overflow":                "reject-publish",
				"max-length":              maxLength,
			}})
		for _, tier := range tiersOf(c) {
			q := RetryQueue(c, tier)
			t.Queues = append(t.Queues, Queue{q})
			t.Bindings = append(t.Bindings, Binding{ExchangeRetry, q, q})
			t.Policies = append(t.Policies, Policy{Name: q, Pattern: "^" + regexp.QuoteMeta(q) + "$", Priority: 10,
				Definition: map[string]any{
					"message-ttl":             ms(tier.Delay),
					"dead-letter-exchange":    ExchangeRequeue,
					"dead-letter-routing-key": c,
					"dead-letter-strategy":    "at-least-once",
					"overflow":                "reject-publish",
					"max-length":              maxLength,
				}})
		}
		t.Queues = append(t.Queues, Queue{DLQ(c)})
		t.Bindings = append(t.Bindings, Binding{ExchangeDLX, DLQ(c), c})
	}
	t.Policies = append(t.Policies, Policy{Name: "dlq", Pattern: `^dlq\.`, Priority: 10,
		Definition: map[string]any{"message-ttl": ms(DLQTTL), "max-length": maxLength}})
	return t
}

// tierFor is the tier of a retry after attempt failures (1-based).
func tierFor(attempt int) Tier {
	return RetryTiers[min(attempt, len(RetryTiers))-1]
}

// waitTierFor is the longest of consumer's tiers that does not exceed d, or the
// shortest if all do.
func waitTierFor(consumer string, d time.Duration) Tier {
	tiers := tiersOf(consumer)
	best := tiers[0]
	for _, t := range tiers {
		if t.Delay <= d && t.Delay > best.Delay {
			best = t
		}
	}
	if best.Delay > d {
		return tiers[0]
	}
	return best
}

// Validate checks the topology is closed: every binding's ends exist, every queue a
// policy names exists, every queue has exactly one policy.
func (t Topology) Validate() error {
	ex := map[string]bool{}
	for _, e := range t.Exchanges {
		ex[e.Name] = true
	}
	q := map[string]int{}
	for _, x := range t.Queues {
		q[x.Name] = 0
	}
	for _, b := range t.Bindings {
		if !ex[b.Source] {
			return fmt.Errorf("binding from unknown exchange %s", b.Source)
		}
		if _, ok := q[b.Destination]; !ok {
			return fmt.Errorf("binding to unknown queue %s", b.Destination)
		}
	}
	for _, p := range t.Policies {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return fmt.Errorf("policy %s: %w", p.Name, err)
		}
		for name := range q {
			if re.MatchString(name) {
				q[name]++
			}
		}
	}
	for name, n := range q {
		if n != 1 {
			return fmt.Errorf("queue %s matches %d policies, want 1 (only one applies)", name, n)
		}
	}
	return nil
}
