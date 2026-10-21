// notify sends RYFT's push notifications (docs/ARCHITECTURE.md → notify): the
// morning score and a silent refresh when scoring publishes scored, a
// scored strength session (session.scored), and a reminder when a user's
// data has stopped arriving (a 15-minute check). Without an APNs key it runs
// and counts what it would have sent.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/notify"
	"github.com/timkrebs/ryft/internal/store"
)

// staleEvery is how often the no-data check runs (each user at most once a day).
const staleEvery = 15 * time.Minute

func main() {
	log := httpx.Logger("notify")
	cfg, err := config.Load("notify")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	apnsCfg, apnsOn, err := config.LoadAPNs()
	if err != nil {
		log.Error("apns config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	go db.Watch(ctx, pool, "notify", log)
	st := store.New(pool)

	// RabbitMQ (D97), retried at start-up.
	bus, err := eventbus.Open(ctx, cfg, "notify", log)
	if err != nil {
		log.Error("events", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	svc := &notify.Service{St: st, Log: log, Now: time.Now}
	if apnsOn {
		c, err := apns.New(apns.Config{KeyID: apnsCfg.KeyID, TeamID: apnsCfg.TeamID, PrivateKey: apnsCfg.PrivateKey,
			Topic: apnsCfg.Topic}, nil)
		if err != nil {
			log.Error("apns", "err", err)
			os.Exit(1)
		}
		svc.APNs = c
		log.Info("apns configured", "topic", apnsCfg.Topic, "key_id", apnsCfg.KeyID)
	} else {
		log.Warn("APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8 not set: nothing is sent (ryft_push_notifications_total{result=\"unconfigured\"})")
	}

	mux := http.NewServeMux()
	httpx.ServeMetrics(log, ":"+cfg.MetricsPort)
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		return bus.Ready(ctx)
	})
	mux.HandleFunc("/", httpx.NotFound)
	go func() {
		if err := httpx.Serve(log, ":"+cfg.Port, httpx.Recover(mux)); err != nil {
			log.Error("serve", "err", err)
		}
	}()

	go staleLoop(ctx, log, svc)

	log.Info("consuming scored events")
	err = bus.ConsumeNotify(ctx, log, func(ctx context.Context, m events.NotifyMessage) error {
		f := &logx.Fields{}
		switch {
		case m.Scored != nil:
			f.UserID, f.RequestID = m.Scored.UserID.String(), m.Scored.RequestID
		case m.Refresh != nil:
			f.UserID, f.RequestID = m.Refresh.UserID.String(), m.Refresh.RequestID
		case m.SessionScored != nil:
			f.UserID, f.RequestID = m.SessionScored.UserID.String(), m.SessionScored.RequestID
		}
		return svc.Handle(logx.WithFields(ctx, f), m)
	})
	if err != nil {
		log.Error("consume", "err", err)
		os.Exit(1)
	}
}

func staleLoop(ctx context.Context, log *slog.Logger, svc *notify.Service) {
	t := time.NewTicker(staleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := svc.CheckStale(ctx); err != nil {
				log.ErrorContext(ctx, "stale check", "err", err)
			}
		}
	}
}
