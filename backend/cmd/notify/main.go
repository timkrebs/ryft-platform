package main

import (
	"context"
	"log/slog"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
)

func main() { app.Main(spec()) }

// spec is what notify asks app for: its wait box as the outbox (it publishes no events;
// the box holds its deferrals until they are due, D219), swept by the tick.
func spec() app.Spec {
	var push pushSettings
	return app.Spec{
		Name:   "notify",
		Outbox: app.WaitOutbox,
		// The APNs key is read before anything connects (D78); without one the service
		// runs and counts what it would have sent.
		Load: func(*slog.Logger) (err error) {
			if push.cfg, push.on, err = config.LoadAPNs(); err != nil {
				return app.ConfigError{Msg: "apns config", Err: err}
			}
			return nil
		},
		Build: func(_ context.Context, d app.Deps) (app.Parts, error) { return build(d, push) },
	}
}
