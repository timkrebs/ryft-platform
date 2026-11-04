package main

import (
	"context"
	"log/slog"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
)

func main() {
	var push pushSettings
	app.Main(app.Spec{
		Name:           "notify",
		BrokerRequired: true, // /readyz needs the queue it consumes
		// The APNs key is read before anything connects (D78); without one the service
		// runs and counts what it would have sent.
		Load: func(*slog.Logger) (err error) {
			if push.cfg, push.on, err = config.LoadAPNs(); err != nil {
				return app.ConfigError{Msg: "apns config", Err: err}
			}
			return nil
		},
		Build: func(_ context.Context, d app.Deps) (app.Parts, error) { return build(d, push) },
	})
}
