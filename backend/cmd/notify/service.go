package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/notify"
	"github.com/timkrebs/ryft/internal/store"
)

// staleEvery is how often the no-data check runs (each user at most once a day).
const staleEvery = 15 * time.Minute

// pushSettings is the APNs key as Load read it; on is whether one was set.
type pushSettings struct {
	cfg config.APNs
	on  bool
}

// build makes what notify runs on the opened pool and broker: the push service,
// the stale check (a job) and the scored-events consumer. It publishes nothing, so
// it has no outbox.
func build(d app.Deps, push pushSettings) (app.Parts, error) {
	svc := &notify.Service{St: store.New(d.Pool), Log: d.Log, Now: time.Now}
	if push.on {
		c, err := apns.New(apns.Config{KeyID: push.cfg.KeyID, TeamID: push.cfg.TeamID, PrivateKey: push.cfg.PrivateKey,
			Topic: push.cfg.Topic}, nil)
		if err != nil {
			return app.Parts{}, app.ConfigError{Msg: "apns", Err: err}
		}
		svc.APNs = c
		d.Log.Info("apns configured", "topic", push.cfg.Topic, "key_id", push.cfg.KeyID)
	} else {
		d.Log.Warn("APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8 not set: nothing is sent (ryft_push_notifications_total{result=\"unconfigured\"})")
	}
	return app.Parts{
		Jobs:      []app.Task{{Name: "stale check", Run: every(staleEvery, staleCheck(d.Log, svc))}},
		Consumers: []app.Task{{Name: "scored events", Run: consume(d, svc)}},
	}, nil
}

// staleCheck is one pass of the no-data check. A failed pass is logged and the
// next one runs; a pass cut short by the shutdown is not an error.
func staleCheck(log *slog.Logger, svc *notify.Service) func(ctx context.Context) {
	return func(ctx context.Context) {
		if err := svc.CheckStale(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "stale check", "err", err)
		}
	}
}

// consume is the scored-events consumer. The bus retries, dead-letters and
// resubscribes by itself; it returns only when ctx ends or the bus is gone.
func consume(d app.Deps, svc *notify.Service) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		d.Log.Info("consuming scored events")
		return d.Bus.ConsumeNotify(ctx, d.Log, func(ctx context.Context, m events.NotifyMessage) error {
			return svc.Handle(logx.WithFields(ctx, fieldsOf(m)), m)
		})
	}
}

// fieldsOf is the user and request an event's log lines carry.
func fieldsOf(m events.NotifyMessage) *logx.Fields {
	f := &logx.Fields{}
	switch {
	case m.Scored != nil:
		f.UserID, f.RequestID = m.Scored.UserID.String(), m.Scored.RequestID
	case m.Refresh != nil:
		f.UserID, f.RequestID = m.Refresh.UserID.String(), m.Refresh.RequestID
	case m.SessionScored != nil:
		f.UserID, f.RequestID = m.SessionScored.UserID.String(), m.SessionScored.RequestID
	}
	return f
}

// every is a Task's Run that calls pass each d until ctx ends. pass must log and
// swallow its own errors: a Run that returns one stops the service.
func every(d time.Duration, pass func(ctx context.Context)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				pass(ctx)
			}
		}
	}
}
