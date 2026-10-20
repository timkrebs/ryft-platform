// scoring turns raw samples into daily Recovery / Strain / Sleep scores.
// It is driven by two things: ingest events on NATS (recompute the days a
// sync touched, for the user in the event) and a timer (keep today and
// yesterday fresh, in each user's timezone, for everyone who synced lately;
// on Sundays also the weekly sleep-baseline estimate).
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

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
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
	if len(os.Args) > 1 && os.Args[1] == "learn-baselines" {
		os.Exit(learnBaselines(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "dlq" {
		os.Exit(dlq(os.Args[2:]))
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
	go db.Watch(ctx, pool, "scoring", log)
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

	// The morning push: notify sends it when today first gets a recovery. A
	// failed publish loses that one push (the day is not "first" again); the
	// score itself is stored either way.
	engine.OnScored(func(ctx context.Context, ev model.ScoredEvent) {
		if f := logx.FieldsFrom(ctx); f != nil {
			ev.RequestID = f.RequestID
		}
		if err := bus.PublishScored(ctx, ev); err != nil {
			log.ErrorContext(ctx, "publish scored", "day", ev.Day, "err", err)
		}
	})

	handle := func(ctx context.Context, ev model.IngestedEvent) error {
		ctx = logx.WithFields(ctx, &logx.Fields{RequestID: ev.RequestID, UserID: ev.UserID.String()})
		err := handleEvent(ctx, engine, ev)
		if err == nil && ev.SessionID != nil && len(ev.Days) == 1 {
			// The session's load is final now: notify may announce it.
			if perr := bus.PublishSessionScored(ctx, model.SessionScoredEvent{UserID: ev.UserID, SessionID: *ev.SessionID,
				Day: ev.Days[0], RequestID: ev.RequestID}); perr != nil {
				log.ErrorContext(ctx, "publish session scored", "session_id", *ev.SessionID, "err", perr)
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			// The user was deleted after the event was queued.
			return events.Permanent(err)
		}
		return err
	}

	go func() {
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		learn := learner{tried: map[uuid.UUID]string{}}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshActive(ctx, log, st, engine, &learn)
				if ages, err := st.SyncAges(ctx); err == nil {
					for age, n := range ages {
						metrics.UsersByLastSync.WithLabelValues(age).Set(float64(n))
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

func handleEvent(ctx context.Context, engine *scoring.Engine, ev model.IngestedEvent) error {
	err := scoreEvent(ctx, engine, ev)
	if errors.Is(err, scoring.ErrNoConsent) {
		return nil // withdrawn since the sync: nothing to score, nothing to retry (D78)
	}
	return err
}

func scoreEvent(ctx context.Context, engine *scoring.Engine, ev model.IngestedEvent) error {
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
// activeWindow, each in their own timezone, then runs the weekly baseline
// estimate where it is due.
func refreshActive(ctx context.Context, log *slog.Logger, st *store.Store, engine *scoring.Engine, learn *learner) {
	users, err := st.ActiveUsers(ctx, time.Now().Add(-activeWindow))
	if err != nil {
		log.Error("periodic: active users", "err", err)
		return
	}
	for _, id := range users {
		ctx := logx.WithFields(ctx, &logx.Fields{UserID: id.String()})
		user, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.ErrorContext(ctx, "periodic: user", "err", err)
			continue
		}
		if user.Consent == nil {
			continue // withdrawn: no scores, no baseline estimate (D78)
		}
		today := time.Now().In(loc)
		for _, d := range []time.Time{today.AddDate(0, 0, -1), today} {
			if err := engine.ComputeDay(ctx, id, d.Format("2006-01-02")); err != nil {
				log.ErrorContext(ctx, "periodic recompute", "day", d.Format("2006-01-02"), "err", err)
			}
		}
		learn.maybe(ctx, log, engine, user, today)
	}
}

// learner runs the weekly sleep-baseline estimate from the timer pass. It
// remembers whom it tried on which local day, so a user with too few nights
// (no estimate stored) is tried once that Sunday, not every 15 minutes. One
// entry per active user, overwritten weekly.
type learner struct{ tried map[uuid.UUID]string }

func (l *learner) maybe(ctx context.Context, log *slog.Logger, engine *scoring.Engine, u model.User, now time.Time) {
	day := now.Format("2006-01-02")
	if l.tried[u.ID] == day || !scoring.LearnDue(now, u.SleepBaseLearnedAt) {
		return
	}
	l.tried[u.ID] = day
	if _, err := engine.LearnBaseline(ctx, u.ID, day); err != nil {
		log.ErrorContext(ctx, "learn sleep baseline", "err", err)
	}
}
