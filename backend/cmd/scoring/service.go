package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// build makes what scoring runs on the opened pool and broker: the engine, the
// 15-minute pass (a job) and the ingested-events consumer. What scoring tells notify
// goes through d.Outbox (D101): written to the database, sent once the broker
// confirms it, so a broker outage loses no morning push.
func build(d app.Deps, beta config.Beta) (app.Parts, error) {
	st := store.New(d.Pool)
	engine := scoring.NewEngine(st, d.Log)
	engine.SetConsentMode(beta.Consent)
	pub := events.Publisher{Sender: d.Outbox}

	// The morning push: notify sends it when today first gets a recovery. A failed
	// write loses that one push (the day is not "first" again); the score itself is
	// stored either way.
	engine.OnScored(func(ctx context.Context, ev model.ScoredEvent) {
		if f := logx.FieldsFrom(ctx); f != nil {
			ev.RequestID = f.RequestID
		}
		if err := pub.PublishScored(ctx, ev); err != nil {
			d.Log.ErrorContext(ctx, "publish scored", "day", ev.Day, "err", err)
		}
	})
	handle := ingestHandler(d.Log, engine, pub)
	return app.Parts{
		Jobs: []app.Task{{Name: "scoring pass", Run: every(refreshEvery, timerPass(d.Log, st, engine))}},
		Consumers: []app.Task{{Name: "ingested events", Run: func(ctx context.Context) error {
			d.Log.Info("consuming ingest events")
			return d.Bus.ConsumeIngested(ctx, d.Log, handle)
		}}},
	}, nil
}

// ingestHandler scores an ingested event, then tells notify what is new. The bus
// retries and dead-letters by what it returns (D100).
func ingestHandler(log *slog.Logger, engine *scoring.Engine, pub events.Publisher) func(context.Context, model.IngestedEvent) error {
	return func(ctx context.Context, ev model.IngestedEvent) error {
		ctx = logx.WithFields(ctx, &logx.Fields{RequestID: ev.RequestID, UserID: ev.UserID.String()})
		err := handleEvent(ctx, engine, ev)
		if err == nil {
			session, refresh := announce(ev)
			if session != nil {
				if perr := pub.PublishSessionScored(ctx, *session); perr != nil {
					log.ErrorContext(ctx, "publish session scored", "session_id", session.SessionID, "err", perr)
				}
			}
			if refresh != nil {
				if perr := pub.PublishRefresh(ctx, *refresh); perr != nil {
					log.ErrorContext(ctx, "publish refresh", "day", refresh.Day, "err", perr)
				}
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			// The user was deleted after the event was queued.
			return events.Permanent(err)
		}
		return err
	}
}

// timerPass is one pass of the timer: the active users, the score queue's pruning
// and the sync-age gauges. The learner and the catch-up keep their memory between
// passes. Every error is logged and the next pass runs.
func timerPass(log *slog.Logger, st *store.Store, engine *scoring.Engine) func(ctx context.Context) {
	learn := learner{tried: map[uuid.UUID]string{}}
	behind := catchUp{failed: map[uuid.UUID]string{}}
	return func(ctx context.Context) {
		refreshActive(ctx, log, st, engine, &learn, &behind)
		if ctx.Err() != nil {
			return // the shutdown: pruning and the gauges wait for the next pod
		}
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
