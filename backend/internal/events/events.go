// Package events is the thin NATS JetStream layer between ingest and scoring.
//
// Stream RYFT persists subject ryft.ingested; scoring consumes it with a
// durable pull consumer so a scoring restart never loses a recompute request.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

const (
	StreamName      = "RYFT"
	SubjectIngested = "ryft.ingested"
	ConsumerScoring = "scoring"
	maxDeliver      = 10
)

type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Connect dials NATS and ensures the stream exists (idempotent).
func Connect(ctx context.Context, url, name string) (*Bus, error) {
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{"ryft.>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	return &Bus{nc: nc, js: js}, nil
}

func (b *Bus) Close() { _ = b.nc.Drain() }

func (b *Bus) Ready(ctx context.Context) error {
	if !b.nc.IsConnected() {
		return fmt.Errorf("nats disconnected")
	}
	return nil
}

func (b *Bus) PublishIngested(ctx context.Context, ev model.IngestedEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, SubjectIngested, data)
	metrics.EventsPublished.WithLabelValues(ev.Reason, metrics.Result(err)).Inc()
	return err
}

// ConsumeIngested blocks, handing every event to fn. fn returning an error
// NAKs the message so JetStream redelivers it after a delay.
func (b *Bus) ConsumeIngested(ctx context.Context, log *slog.Logger, fn func(context.Context, model.IngestedEvent) error) error {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       ConsumerScoring,
		FilterSubject: SubjectIngested,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       5 * time.Minute,
		MaxDeliver:    maxDeliver,
	})
	if err != nil {
		return fmt.Errorf("ensure consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		delivered := uint64(1)
		if md, err := msg.Metadata(); err == nil {
			delivered = md.NumDelivered
		}
		if delivered > 1 {
			metrics.Redeliveries.WithLabelValues(ConsumerScoring).Inc()
		}
		var ev model.IngestedEvent
		if err := json.Unmarshal(msg.Data(), &ev); err != nil {
			log.Error("bad event, dropping", "err", err)
			metrics.EventsHandled.WithLabelValues("unknown", "bad").Inc()
			_ = msg.Term()
			return
		}
		if err := fn(ctx, ev); err != nil {
			if delivered >= maxDeliver {
				log.Error("handle event failed on last delivery, giving up", "err", err, "days", ev.Days)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "failed").Inc()
			} else {
				log.Error("handle event failed, will retry", "err", err, "days", ev.Days, "delivery", delivered)
				metrics.EventsHandled.WithLabelValues(ev.Reason, "retry").Inc()
			}
			_ = msg.NakWithDelay(30 * time.Second)
			return
		}
		metrics.EventsHandled.WithLabelValues(ev.Reason, "ok").Inc()
		_ = msg.Ack()
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}
