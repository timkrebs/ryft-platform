package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/logx"
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
	app.Main(spec())
}

// spec is what scoring asks app for. Outbox is not optional: build refuses a Deps
// without one, and BrokerRequired keeps /readyz honest about the queue it consumes.
func spec() app.Spec {
	var beta config.Beta
	return app.Spec{
		Name:           "scoring",
		BrokerRequired: true, // /readyz needs the queue it consumes
		Outbox:         true, // what scoring tells notify goes through the outbox (D101)
		// Only the consent mode; scoring needs no policy version (any recorded one
		// counts, D78), so no RequirePolicy here.
		Load: func(log *slog.Logger) (err error) {
			if beta, err = config.LoadBeta(); err != nil {
				return app.ConfigError{Msg: "beta config", Err: err}
			}
			log.Info("beta", "consent", beta.Consent)
			return nil
		},
		Build: func(_ context.Context, d app.Deps) (app.Parts, error) { return build(d, beta) },
	}
}

// announce is what scoring tells notify once an event's days are scored:
//
//   - a completed session (session.completed carries its id): session.scored, its load
//     is final, notify may announce it;
//   - a sync's or an import's days (D92, D125): refresh with the newest, the silent push
//     that lets the app refetch.
//
// An import's event never carries a session id, so it never leads to SESSION_SCORED or
// PR; notify refuses an imported session as well.
func announce(ev model.IngestedEvent) (*model.SessionScoredEvent, *model.RefreshEvent) {
	var session *model.SessionScoredEvent
	var refresh *model.RefreshEvent
	if ev.SessionID != nil && len(ev.Days) == 1 {
		session = &model.SessionScoredEvent{UserID: ev.UserID, SessionID: *ev.SessionID, Day: ev.Days[0], RequestID: ev.RequestID}
	}
	if (ev.Reason == model.ReasonSync || ev.Reason == model.ReasonImport) && len(ev.Days) > 0 {
		refresh = &model.RefreshEvent{UserID: ev.UserID, Day: slices.Max(ev.Days), RequestID: ev.RequestID}
	}
	return session, refresh
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
	// The sync's (or an import's, D128: an explicit list, which may hold two days) days,
	// then the stored days after them whose history changed, in this handler: a
	// duplicate finds nothing changed and stops there, a retry or a replayed dead letter
	// (events.Redelivered in ctx) re-scores the days after its days anyway, finishing
	// what the failed attempt began (D100).
	return engine.ComputeDays(ctx, ev.UserID, days)
}

// refreshActive rescores yesterday and today for every user who synced within
// activeWindow, each in their own timezone, re-scores days left behind their
// history (catchUp), then runs the weekly baseline estimate where it is due. It stops
// between two users when ctx ends.
func refreshActive(ctx context.Context, log *slog.Logger, st *store.Store, engine *scoring.Engine, learn *learner, behind *catchUp) {
	users, err := st.ActiveUsers(ctx, time.Now().Add(-activeWindow))
	if err != nil {
		if ctx.Err() == nil { // a query cut by the shutdown is not an error
			log.Error("periodic: active users", "err", err)
		}
		return
	}
	users = behind.order(users)
	budget := behindPerRun
	eachUser(ctx, users, func(ctx context.Context, i int, id uuid.UUID) {
		ctx = logx.WithFields(ctx, &logx.Fields{UserID: id.String()})
		user, loc, err := st.GetUser(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				log.ErrorContext(ctx, "periodic: user", "err", err)
			}
			return
		}
		if !engine.Admits(user) {
			return // withdrawn: no scores, no baseline estimate (D78)
		}
		now := time.Now().In(loc)
		today := now.Format("2006-01-02")
		yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
		if err := engine.ComputeDays(ctx, id, []string{yesterday, today}); err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "periodic recompute", "err", err)
		}
		if budget > 0 && behind.failed[id] != today {
			n, err := engine.RescoreBehind(ctx, id, today, budget)
			if err != nil && ctx.Err() == nil { // cut by the shutdown: not the day's fault, tried again next pass
				log.ErrorContext(ctx, "periodic: days behind their history", "err", err)
				behind.failed[id] = today
			}
			if budget -= n; budget <= 0 {
				behind.next = users[(i+1)%len(users)]
			}
		}
		learn.maybe(ctx, log, engine, user, now)
	})
}

// eachUser calls fn for the users in turn, and stops before the next one once ctx
// has ended: a pass over every active user must not outlive the shutdown by the
// users it has left (each would only log a cancelled query).
func eachUser(ctx context.Context, users []uuid.UUID, fn func(ctx context.Context, i int, id uuid.UUID)) {
	for i, id := range users {
		if ctx.Err() != nil {
			return
		}
		fn(ctx, i, id)
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
	if _, err := engine.LearnBaseline(ctx, u.ID, day); err != nil && ctx.Err() == nil {
		log.ErrorContext(ctx, "learn sleep baseline", "err", err)
	}
}
