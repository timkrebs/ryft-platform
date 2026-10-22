// scoring turns raw samples into daily Recovery / Strain / Sleep scores.
// It is driven by two things: events from RabbitMQ (recompute the days a
// sync touched, for the user in the event) and a timer (keep today and
// yesterday fresh, in each user's timezone, for everyone who synced lately;
// on Sundays also the weekly sleep-baseline estimate).
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
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
	// behindPerRun: days the timer pass re-scores behind their history
	// (Engine.RescoreBehind, the days their re-score reaches included), all
	// users together, so a backlog is worked off over a few passes instead of
	// stretching one. The last user's share can overshoot it by the days its
	// re-score reaches.
	behindPerRun = 50
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "rescore" {
		os.Exit(rescore(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "learn-baselines" {
		os.Exit(learnBaselines(os.Args[2:]))
	}
	log := httpx.Logger("scoring")
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	// Only the consent mode; scoring needs no policy version (any recorded one
	// counts, D78), so no RequirePolicy here.
	beta, err := config.LoadBeta()
	if err != nil {
		log.Error("beta config", "err", err)
		os.Exit(1)
	}
	log.Info("beta", "consent", beta.Consent)
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
	engine.SetConsentMode(beta.Consent)

	// RabbitMQ (D97), retried at start-up.
	bus, err := eventbus.Open(ctx, cfg, "scoring", log)
	if err != nil {
		log.Error("events", "err", err)
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

	// What scoring tells notify goes through its outbox (D101): written to the
	// database, sent once the broker confirms it, so a broker outage loses no morning
	// push.
	ob := outbox.New(pool, "scoring", bus, log)
	go ob.Run(ctx)
	pub := events.Publisher{Sender: ob}

	// The morning push: notify sends it when today first gets a recovery. A failed
	// write loses that one push (the day is not "first" again); the score itself is
	// stored either way.
	engine.OnScored(func(ctx context.Context, ev model.ScoredEvent) {
		if f := logx.FieldsFrom(ctx); f != nil {
			ev.RequestID = f.RequestID
		}
		if err := pub.PublishScored(ctx, ev); err != nil {
			log.ErrorContext(ctx, "publish scored", "day", ev.Day, "err", err)
		}
	})

	handle := func(ctx context.Context, ev model.IngestedEvent) error {
		ctx = logx.WithFields(ctx, &logx.Fields{RequestID: ev.RequestID, UserID: ev.UserID.String()})
		err := handleEvent(ctx, engine, ev)
		if err == nil && ev.SessionID != nil && len(ev.Days) == 1 {
			// The session's load is final now: notify may announce it.
			if perr := pub.PublishSessionScored(ctx, model.SessionScoredEvent{UserID: ev.UserID, SessionID: *ev.SessionID,
				Day: ev.Days[0], RequestID: ev.RequestID}); perr != nil {
				log.ErrorContext(ctx, "publish session scored", "session_id", *ev.SessionID, "err", perr)
			}
		}
		if err == nil && ev.Reason == "sync" && len(ev.Days) > 0 {
			// The sync's days are scored: a silent push lets the app refetch (D92).
			newest := slices.Max(ev.Days)
			if perr := pub.PublishRefresh(ctx, model.RefreshEvent{UserID: ev.UserID, Day: newest, RequestID: ev.RequestID}); perr != nil {
				log.ErrorContext(ctx, "publish refresh", "day", newest, "err", perr)
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
		behind := catchUp{failed: map[uuid.UUID]string{}}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshActive(ctx, log, st, engine, &learn, &behind)
				if err := st.PruneScoreQueue(ctx); err != nil {
					log.Warn("prune score queue", "err", err)
				}
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
	// A range: a recompute, or the days of an account merged into this one.
	if (ev.Reason == "recompute" || ev.Reason == "merge") && len(days) == 2 {
		return engine.ComputeRange(ctx, ev.UserID, days[0], days[1])
	}
	// The sync's days, then the stored days after them whose history changed,
	// in this handler: a duplicate finds nothing changed and stops there, a
	// retry (events.Redelivered in ctx) re-scores the days after its days
	// anyway, finishing what the failed attempt began (D100).
	return engine.ComputeDays(ctx, ev.UserID, days)
}

// refreshActive rescores yesterday and today for every user who synced within
// activeWindow, each in their own timezone, re-scores days left behind their
// history (catchUp), then runs the weekly baseline estimate where it is due.
func refreshActive(ctx context.Context, log *slog.Logger, st *store.Store, engine *scoring.Engine, learn *learner, behind *catchUp) {
	users, err := st.ActiveUsers(ctx, time.Now().Add(-activeWindow))
	if err != nil {
		log.Error("periodic: active users", "err", err)
		return
	}
	users = behind.order(users)
	budget := behindPerRun
	for i, id := range users {
		ctx := logx.WithFields(ctx, &logx.Fields{UserID: id.String()})
		user, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.ErrorContext(ctx, "periodic: user", "err", err)
			continue
		}
		if !engine.Admits(user) {
			continue // withdrawn: no scores, no baseline estimate (D78)
		}
		now := time.Now().In(loc)
		today := now.Format("2006-01-02")
		yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
		if err := engine.ComputeDays(ctx, id, []string{yesterday, today}); err != nil {
			log.ErrorContext(ctx, "periodic recompute", "err", err)
		}
		if budget > 0 && behind.failed[id] != today {
			n, err := engine.RescoreBehind(ctx, id, today, budget)
			if err != nil {
				log.ErrorContext(ctx, "periodic: days behind their history", "err", err)
				behind.failed[id] = today
			}
			if budget -= n; budget <= 0 {
				behind.next = users[(i+1)%len(users)]
			}
		}
		learn.maybe(ctx, log, engine, user, now)
	}
}

// catchUp hands out the timer pass's behindPerRun days of RescoreBehind:
// round robin, each pass starting with the user after the one that used up
// the last pass's budget, so one long backlog does not keep the others
// waiting; and a user whose re-score failed is left until their next local
// day, so a day that cannot be scored does not take every pass's budget. One
// entry per active user whose re-score failed, overwritten daily.
type catchUp struct {
	next   uuid.UUID            // the first user of the next pass (uuid.Nil: from the start)
	failed map[uuid.UUID]string // user → the local day their re-score failed
}

// order sorts the users and starts them at next, wrapping around; the next
// pass starts from the beginning unless this one runs out of budget.
func (c *catchUp) order(users []uuid.UUID) []uuid.UUID {
	out := startAt(users, c.next)
	c.next = uuid.Nil
	return out
}

// startAt sorts ids and rotates them to begin at the first id not below from.
func startAt(ids []uuid.UUID, from uuid.UUID) []uuid.UUID {
	cmp := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	ids = slices.SortedFunc(slices.Values(ids), cmp)
	i, _ := slices.BinarySearchFunc(ids, from, cmp)
	return append(ids[i:len(ids):len(ids)], ids[:i]...)
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
