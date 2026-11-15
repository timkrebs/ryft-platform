package main

import (
	"context"
	"errors"
	"time"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/notify"
	"github.com/timkrebs/ryft/internal/store"
)

// pushSettings is the APNs key as Load read it; on is whether one was set.
type pushSettings struct {
	cfg config.APNs
	on  bool
}

// waitBox is where notify's deferrals wait (D219): app's wait box, the outbox of
// publisher notify that relays into ryft-notify-wait. SendAt writes a row with its
// not-before time; the tick's sweep publishes it when it is due, and /push/wait hands it
// back under its own key.
type waitBox interface {
	SendAt(ctx context.Context, m events.Message, at time.Time) error
}

// build makes what notify runs on the opened pool and transport: the push service, its
// push routes (/push/scored, /push/session.scored, /push/refresh and /push/wait, D218)
// and the tick's "stale check", the pass a 15-minute timer ran before Cloud Run (D219).
// It publishes no events: d.Outbox is its wait box, holding the pushes it defers (quiet
// hours, APNs down) until they are due.
func build(d app.Deps, push pushSettings) (app.Parts, error) {
	switch {
	case d.Outbox == nil: // without the wait box every deferral would be a nack, redelivered within seconds
		return app.Parts{}, errors.New("notify needs its wait box (Spec.Outbox)")
	case d.Bus == nil:
		return app.Parts{}, errors.New("notify needs the transport for its push routes")
	}
	svc, err := newService(d, push)
	if err != nil {
		return app.Parts{}, err
	}
	routes, err := pushRoutes(d.Bus, func(ctx context.Context, m events.NotifyMessage) error {
		return svc.Handle(logx.WithFields(ctx, fieldsOf(m)), m)
	}, d.Outbox)
	if err != nil {
		return app.Parts{}, err
	}
	d.Log.Info("consuming scored events") // the line the runbooks grep for
	return app.Parts{
		Push:  []app.PushRoutes{routes},
		Ticks: []eventbus.Tick{{Name: "stale check", Run: svc.CheckStale}},
	}, nil
}

// pushRoutes is notify's push handling with handle as its handler and box as where a
// Defer goes: the original key, ryft-message-id and body, due at now plus the delay.
func pushRoutes(tr *eventbus.Transport, handle func(context.Context, events.NotifyMessage) error, box waitBox) (app.PushRoutes, error) {
	p, err := tr.Push(events.ConsumerNotify, events.NotifyHandler(handle), box)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// newService is the push service: with an APNs key it sends, without one it counts
// what it would have sent. A key that does not parse is a ConfigError under "apns".
func newService(d app.Deps, push pushSettings) (*notify.Service, error) {
	svc := &notify.Service{St: store.New(d.Pool), Log: d.Log, Now: time.Now}
	if !push.on {
		d.Log.Warn("APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8 not set: nothing is sent (ryft_push_notifications_total{result=\"unconfigured\"})")
		return svc, nil
	}
	c, err := apns.New(apns.Config{KeyID: push.cfg.KeyID, TeamID: push.cfg.TeamID, PrivateKey: push.cfg.PrivateKey,
		Topic: push.cfg.Topic}, nil)
	if err != nil {
		return nil, app.ConfigError{Msg: "apns", Err: err}
	}
	svc.APNs = c
	d.Log.Info("apns configured", "topic", push.cfg.Topic, "key_id", push.cfg.KeyID)
	return svc, nil
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
