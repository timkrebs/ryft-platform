package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// build makes what scoring runs on the opened pool and transport: the engine, the push
// routes of the ingested events (/push/ingested, /push/session.completed, D218) and the
// tick's "scoring pass", the pass a 15-minute timer ran before Cloud Run (D219).
//
// What scoring tells notify goes through d.Outbox (D101): Send writes the row and
// flushes it inline (D219), so every publish of a push or a tick (OnScored's scored,
// session.scored, refresh) has been handed to Pub/Sub, or left for the next tick's
// sweep, before the push or the tick answers. Nothing runs after the answer.
func build(d app.Deps, beta config.Beta) (app.Parts, error) {
	switch {
	case d.Outbox == nil: // a nil one would panic on the first publish, in every push and in the pass
		return app.Parts{}, errors.New("scoring needs the outbox (Spec.Outbox)")
	case d.Bus == nil:
		return app.Parts{}, errors.New("scoring needs the transport for its push routes")
	}
	st := store.New(d.Pool)
	engine := scoring.NewEngine(st, d.Log)
	engine.SetConsentMode(beta.Consent)
	pub := events.Publisher{Sender: d.Outbox}
	engine.OnScored(onScored(d.Log, pub))

	handle := ingestHandler(d.Log, func(ctx context.Context, ev model.IngestedEvent) error {
		return handleEvent(ctx, d.Log, engine, st, ev)
	}, pub)
	push, err := d.Bus.Push(events.ConsumerScoring, events.ScoringHandler(handle), nil) // nothing of scoring's is deferred
	if err != nil {
		return app.Parts{}, err
	}
	d.Log.Info("consuming ingest events", "paths", push.Paths()) // the line the runbooks grep for

	pass := timerPass(&learner{tried: map[uuid.UUID]string{}}, &catchUp{failed: map[uuid.UUID]string{}},
		func(ctx context.Context, learn *learner, behind *catchUp) {
			refreshActive(ctx, d.Log, st, engine, learn, behind)
		}, housekeeping(d.Log, st))
	return app.Parts{
		Push:  []app.PushRoutes{push},
		Ticks: []eventbus.Tick{{Name: "scoring pass", Run: passStep(pass)}},
	}, nil
}

// onScored is the morning push: notify sends it when today first gets a recovery. A
// failed write loses that one push (the day is not "first" again); the score itself is
// stored either way.
func onScored(log *slog.Logger, pub events.Publisher) func(context.Context, model.ScoredEvent) {
	return func(ctx context.Context, ev model.ScoredEvent) {
		if f := logx.FieldsFrom(ctx); f != nil {
			ev.RequestID = f.RequestID
		}
		if err := pub.PublishScored(ctx, ev); err != nil {
			log.ErrorContext(ctx, "publish scored", "day", ev.Day, "err", err)
		}
	}
}

// ingestHandler scores an ingested event with score, then tells notify what is new, all
// before it returns: the push answers with what it returns (D100), and nothing runs
// after the answer (D219).
func ingestHandler(log *slog.Logger, score func(context.Context, model.IngestedEvent) error, pub events.Publisher) func(context.Context, model.IngestedEvent) error {
	return func(ctx context.Context, ev model.IngestedEvent) error {
		ctx = logx.WithFields(ctx, &logx.Fields{RequestID: ev.RequestID, UserID: ev.UserID.String()})
		err := score(ctx, ev)
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

// timerPass is one pass: refresh the active users, then the housekeeping (unless the
// tick's budget has ended). The learner and the catch-up are the pass's memory between
// ticks, in this process (an instance that starts anew forgets them, as a pod did): it
// is handed the same two every time, never new ones. Every error is logged by the steps.
func timerPass(learn *learner, behind *catchUp, refresh func(context.Context, *learner, *catchUp), after func(context.Context)) func(context.Context) {
	return func(ctx context.Context) {
		refresh(ctx, learn, behind)
		if ctx.Err() != nil {
			return // out of budget: pruning and the gauges wait for the next tick
		}
		after(ctx)
	}
}

// passStep is the pass as a tick step: its errors are its own to log (one user's failure
// is not the pass's), so the step fails only when the tick's budget ended the pass,
// which stops between two users, and the tick counts an error.
func passStep(pass func(context.Context)) func(context.Context) error {
	return func(ctx context.Context) error {
		pass(ctx)
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the pass ran out of the tick's budget: %w", err)
		}
		return nil
	}
}

// housekeeping prunes the score queue and sets the sync-age gauges.
func housekeeping(log *slog.Logger, st *store.Store) func(context.Context) {
	return func(ctx context.Context) {
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
