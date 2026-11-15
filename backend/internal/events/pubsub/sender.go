package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/events"
)

// ErrNoTopic is a send whose key has no topic in the topology. It is an error, not a
// drop, and an events.Rejected one: the outbox keeps the row and sets it aside at once.
var ErrNoTopic = errors.New("pubsub: no topic for this key")

// Bus is events.Bus on Pub/Sub: Send publishes a message to its key's topic, and the
// typed Publish* methods come from events.Publisher. It holds no connection.
type Bus struct {
	events.Publisher
	c *Client
}

// NewBus is the Bus that publishes with c.
func NewBus(c *Client) *Bus {
	b := &Bus{c: c}
	b.Publisher = events.Publisher{Sender: b}
	return b
}

// Send publishes m to the topic of its key (TopicFor), with the ryft-* attributes and,
// on scoring's two ordered topics, the body's user_id as ordering key. Every send counts
// in ryft_events_published_total. What no retry can deliver is events.Rejected: a key
// without a topic (ErrNoTopic), an ordered event without a user, and what Publish
// rejects.
func (b *Bus) Send(ctx context.Context, m events.Message) error {
	topic, ok := TopicFor(m.Key)
	if !ok {
		err := events.Rejected(fmt.Errorf("%w: %.64q", ErrNoTopic, m.Key))
		events.CountPublished(m, err)
		return err
	}
	return send(ctx, b.c, topic, m)
}

// Client is the client the Bus publishes with (the push handlers dead-letter with it).
func (b *Bus) Client() *Client { return b.c }

// fixedTopic sends every message to one topic, whatever its key.
type fixedTopic struct {
	c     *Client
	topic string
}

// To is an events.Sender that publishes every message to topic, whatever its key, which
// stays its ryft-key: notify's outbox relays its deferrals into TopicNotifyWait with it,
// and /push/wait hands each back under its own key. topic must be in the topology.
func (c *Client) To(topic string) (events.Sender, error) {
	if !isTopic(topic) {
		return nil, fmt.Errorf("pubsub: %q is not a topic of the topology", topic)
	}
	return fixedTopic{c: c, topic: topic}, nil
}

func (f fixedTopic) Send(ctx context.Context, m events.Message) error {
	return send(ctx, f.c, f.topic, m)
}

// send publishes m to topic and counts it.
func send(ctx context.Context, c *Client, topic string, m events.Message) error {
	msg := Message{Data: m.Body, Attributes: attributes(m)}
	var err error
	if Ordered(topic) {
		msg.OrderingKey, err = orderingKey(m.Body)
	}
	if err == nil {
		_, err = c.Publish(ctx, topic, msg)
	}
	events.CountPublished(m, err)
	return err
}

// attributes are m's ryft-key, ryft-message-id and ryft-reason (empty values left out).
func attributes(m events.Message) map[string]string {
	a := map[string]string{AttrKey: m.Key}
	if m.ID != "" {
		a[AttrMessageID] = m.ID
	}
	if m.Reason != "" {
		a[AttrReason] = m.Reason
	}
	return a
}

// orderingKey is the body's user_id as a canonical UUID: Pub/Sub then holds one user's
// later messages until the current one is settled (D17 as amended in D218). A body
// without one can never be published there (events.Rejected).
func orderingKey(body []byte) (string, error) {
	var who struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(body, &who) == nil {
		if id, err := uuid.Parse(who.UserID); err == nil {
			return id.String(), nil
		}
	}
	return "", events.Rejected(errors.New("pubsub: an event for an ordered topic needs a user_id that is a UUID"))
}
