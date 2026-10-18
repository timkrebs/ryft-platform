// scoring turns raw samples into daily Recovery / Strain / Sleep scores.
// It is driven by two things: ingest events on NATS (recompute the days a
// sync touched, for the user in the event) and a timer (keep today and
// yesterday fresh, in each user's timezone, for everyone who synced lately).
package main

import (
	"context"
	"errors"
	"log/slog"
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
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

const (
	refreshEvery = 15 * time.Minute
	// activeWindow: users whose devices synced this recently get the timer pass.
	activeWindow = 48 * time.Hour
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "rescore" {
		os.Exit(rescore(os.Args[2:]))
	}
	log := httpx.Logger("scoring")
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Retries until the role can log in and the migrate job has applied the
	// schema; this service's role cannot change it.
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	st := store.New(pool)
	engine := scoring.NewEngine(st, log)

	bus, err := events.Open(ctx, cfg.NatsURL, events.Options{Name: "scoring", User: cfg.NatsUser, Password: cfg.NatsPass, OwnStream: true}, log)
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
	mux.HandleFunc("/", httpx.NotFound)
	go func() {
		if err := httpx.Serve(log, ":"+cfg.Port, httpx.Recover(mux)); err != nil {
			log.Error("serve", "err", err)
		}
	}()

	handle := func(ctx context.Context, ev model.IngestedEvent) error {
		ctx = logx.WithFields(ctx, &logx.Fields{RequestID: ev.RequestID, UserID: ev.UserID.String()})
		err := handleEvent(ctx, engine, ev)
		if errors.Is(err, store.ErrNotFound) {
			// The user was deleted after the event was queued.
			return events.Permanent(err)
		}
		return err
	}

	go func() {
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshActive(ctx, log, st, engine)
			}
		}
	}()

	log.Info("consuming ingest events")
	if err := bus.ConsumeIngested(ctx, log, handle); err != nil {
		log.Error("consume", "err", err)
		os.Exit(1)
	}
}

func handleEvent(ctx context.Context, engine *scoring.Engine, ev model.IngestedEvent) error {
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

// refreshActive rescores yesterday and today for every user who synced within
// activeWindow, each in their own timezone.
func refreshActive(ctx context.Context, log *slog.Logger, st *store.Store, engine *scoring.Engine) {
	users, err := st.ActiveUsers(ctx, time.Now().Add(-activeWindow))
	if err != nil {
		log.Error("periodic: active users", "err", err)
		return
	}
	for _, id := range users {
		ctx := logx.WithFields(ctx, &logx.Fields{UserID: id.String()})
		_, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.ErrorContext(ctx, "periodic: user", "err", err)
			continue
		}
		today := time.Now().In(loc)
		for _, d := range []time.Time{today.AddDate(0, 0, -1), today} {
			if err := engine.ComputeDay(ctx, id, d.Format("2006-01-02")); err != nil {
				log.ErrorContext(ctx, "periodic recompute", "day", d.Format("2006-01-02"), "err", err)
			}
		}
	}
}
