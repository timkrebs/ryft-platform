// scoring turns raw samples into daily Recovery / Strain / Sleep scores.
// It is driven by two things: ingest events on NATS (recompute the days a
// sync touched) and a timer (keep today and yesterday fresh while the day is
// still accumulating strain).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

const refreshEvery = 15 * time.Minute

func main() {
	log := httpx.Logger("scoring")
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	st := store.New(pool)
	engine := scoring.NewEngine(st, log)

	bus, err := events.Connect(ctx, cfg.NatsURL, "scoring")
	if err != nil {
		log.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	// Health endpoints only; no public API.
	mux := http.NewServeMux()
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		return bus.Ready(ctx)
	})
	go func() {
		if err := httpx.Serve(log, ":"+cfg.Port, mux); err != nil {
			log.Error("serve", "err", err)
		}
	}()

	handle := func(ctx context.Context, ev model.IngestedEvent) error {
		days := append([]string(nil), ev.Days...)
		sort.Strings(days) // oldest first so baselines chain correctly
		if ev.Reason == "recompute" && len(days) == 2 {
			return engine.ComputeRange(ctx, ev.UserID, days[0], days[1])
		}
		for _, d := range days {
			if err := engine.ComputeDay(ctx, ev.UserID, d); err != nil {
				return err
			}
		}
		return nil
	}

	go func() {
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, loc, err := st.GetUser(ctx, cfg.UserID)
				if err != nil {
					log.Error("periodic: user", "err", err)
					continue
				}
				today := time.Now().In(loc)
				for _, d := range []time.Time{today.AddDate(0, 0, -1), today} {
					if err := engine.ComputeDay(ctx, cfg.UserID, d.Format("2006-01-02")); err != nil {
						log.Error("periodic recompute", "day", d.Format("2006-01-02"), "err", err)
					}
				}
			}
		}
	}()

	log.Info("consuming ingest events")
	if err := bus.ConsumeIngested(ctx, log, handle); err != nil {
		log.Error("consume", "err", err)
		os.Exit(1)
	}
}
