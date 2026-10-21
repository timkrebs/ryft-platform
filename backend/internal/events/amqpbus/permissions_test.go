package amqpbus

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/timkrebs/ryft/internal/events"
)

// The least-privilege matrix (D99, replacing D43's NATS ACLs): with the generated
// users, permissions and topic permissions loaded into vhost ryft of the test broker,
// each service may do exactly its list and nothing else. Opt-in like the other
// integration tests; it imports into the broker's vhost ryft (use a throwaway one).
func TestPermissions(t *testing.T) {
	b := newBroker(t)
	users := map[string]string{}
	for _, g := range Grants {
		users[g.User] = "test-" + g.User + "-password"
	}
	b.call(http.MethodPost, "/api/definitions", Definitions(Spec(1), users))
	b.call(http.MethodPut, "/api/permissions/"+Vhost+"/"+b.amqp.User.Username(), map[string]string{"configure": ".*", "write": ".*", "read": ".*"})
	t.Cleanup(func() {
		for _, q := range Spec(1).Queues {
			b.call(http.MethodDelete, "/api/queues/"+Vhost+"/"+url.PathEscape(q.Name)+"/contents", nil)
		}
	})

	type pub struct{ exchange, key string }
	allowed := map[string][]pub{
		"ingest":  {{ExchangeEvents, events.KeyIngested}},
		"api":     {{ExchangeEvents, events.KeyIngested}, {ExchangeEvents, events.KeySessionCompleted}},
		"scoring": {{ExchangeEvents, events.KeyScored}, {ExchangeEvents, events.KeySessionScored}, {ExchangeEvents, events.KeyRefresh}, {ExchangeRetry, "scoring.retry.5s"}, {ExchangeDLX, "scoring"}},
		"notify":  {{ExchangeRetry, "notify.wait.1h"}, {ExchangeRetry, "notify.retry.30s"}, {ExchangeDLX, "notify"}},
	}
	every := []pub{{ExchangeEvents, events.KeyIngested}, {ExchangeEvents, events.KeySessionCompleted}, {ExchangeEvents, events.KeyScored},
		{ExchangeEvents, events.KeySessionScored}, {ExchangeEvents, events.KeyRefresh}, {ExchangeRetry, "scoring.retry.5s"},
		{ExchangeRetry, "notify.wait.1h"}, {ExchangeRetry, "notify.retry.30s"}, {ExchangeDLX, "scoring"}, {ExchangeDLX, "notify"},
		{ExchangeRequeue, "scoring"}, {ExchangeRequeue, "notify"}, {"", "scoring"}}
	reads := map[string]string{"scoring": "scoring", "notify": "notify"}

	for _, g := range Grants {
		t.Run(g.User, func(t *testing.T) {
			u := *b.amqp
			u.User = url.UserPassword(g.User, users[g.User])
			u.Path = "/" + Vhost
			conn, err := amqp.Dial(u.String())
			if err != nil {
				t.Fatalf("%s cannot log in: %v", g.User, err)
			}
			defer conn.Close()
			for _, p := range every {
				ok := false
				for _, a := range allowed[g.User] {
					ok = ok || a == p
				}
				err := tryPublish(conn, p.exchange, p.key)
				if ok && err != nil {
					t.Errorf("%s must be allowed to publish %s/%s: %v", g.User, p.exchange, p.key, err)
				}
				if !ok && !refused(err) {
					t.Errorf("%s must not publish %s/%q (got %v)", g.User, p.exchange, p.key, err)
				}
			}
			for _, q := range []string{"scoring", "notify", "dlq.scoring", "scoring.retry.5s"} {
				err := tryConsume(conn, q)
				if reads[g.User] == q && err != nil {
					t.Errorf("%s must consume %s: %v", g.User, q, err)
				}
				if reads[g.User] != q && !refused(err) {
					t.Errorf("%s must not consume %s (got %v)", g.User, q, err)
				}
			}
			if err := tryDeclare(conn); !refused(err) {
				t.Errorf("%s must not declare queues (configure is the operator's): %v", g.User, err)
			}
		})
	}
	// Nobody else: a wrong password.
	u := *b.amqp
	u.User = url.UserPassword("scoring", "wrong")
	u.Path = "/" + Vhost
	if c, err := amqp.Dial(u.String()); err == nil {
		c.Close()
		t.Fatal("a wrong password must not log in")
	}
}

// tryPublish publishes one message on a channel of its own with confirms; a refusal
// closes that channel with 403.
func tryPublish(conn *amqp.Connection, exchange, key string) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, false, false,
		amqp.Publishing{ContentType: "application/json", Type: key, Body: []byte(`{"permission":"test"}`)})
	if err != nil {
		return err
	}
	select {
	case e := <-closed:
		if e == nil {
			return errors.New("channel closed")
		}
		return e
	case <-dc.Done():
		if !dc.Acked() {
			return errors.New("nack")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func tryConsume(conn *amqp.Connection, queue string) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	_, err = ch.Consume(queue, "", false, true, false, false, nil)
	return err
}

func tryDeclare(conn *amqp.Connection) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	_, err = ch.QueueDeclare("evil", true, false, false, false, amqp.Table{"x-queue-type": "quorum"})
	return err
}

// refused is the broker's ACCESS_REFUSED (403).
func refused(err error) bool {
	var e *amqp.Error
	return errors.As(err, &e) && (e.Code == amqp.AccessRefused || strings.Contains(e.Reason, "ACCESS_REFUSED"))
}
