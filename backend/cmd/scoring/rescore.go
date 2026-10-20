package main

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// rescore recomputes the last -days days of every user with the current
// scoring rules, oldest first so the baselines build up, then exits. It runs
// inside the scoring pod with scoring's own role and credentials
// (scripts/rescore_all.sh); no user tokens, no admin key, no recompute gate.
func rescore(args []string) int {
	fs := flag.NewFlagSet("rescore", flag.ContinueOnError)
	days := fs.Int("days", scoring.MaxRangeDays, "days back from each user's local today (max 400)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := httpx.Logger("scoring-rescore")
	if *days < 1 || *days > scoring.MaxRangeDays {
		log.Error("days must be 1-400", "days", *days)
		return 2
	}
	ctx, st, engine, done, err := offline(log)
	if err != nil {
		return 1
	}
	defer done()

	users, err := st.UserIDs(ctx)
	if err != nil {
		log.Error("list users", "err", err)
		return 1
	}
	start, failed, skipped := time.Now(), 0, 0
	for i, id := range users {
		user, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.Warn("load user", "user_id", id, "err", err)
			failed++
			continue
		}
		if !engine.Admits(user) {
			log.Info("user skipped, no consent", "user_id", id, "n", i+1, "of", len(users))
			skipped++
			continue
		}
		today := time.Now().In(loc)
		from := today.AddDate(0, 0, -(*days - 1)).Format("2006-01-02")
		if err := engine.ComputeRange(ctx, id, from, today.Format("2006-01-02")); err != nil {
			log.Warn("rescore user", "user_id", id, "err", err)
			failed++
			continue
		}
		log.Info("user rescored", "user_id", id, "from", from, "n", i+1, "of", len(users))
	}
	log.Info("rescore done", "users", len(users), "skipped", skipped, "failed", failed, "model_version", scoring.ModelVersion,
		"took", time.Since(start).Round(time.Second).String())
	if failed > 0 {
		return 1
	}
	return 0
}

// offline opens the database for a one-off subcommand with scoring's own role.
func offline(log *slog.Logger) (context.Context, *store.Store, *scoring.Engine, func(), error) {
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		return nil, nil, nil, nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		stop()
		log.Error("db", "err", err)
		return nil, nil, nil, nil, err
	}
	st := store.New(pool)
	engine := scoring.NewEngine(st, log)
	beta, err := config.LoadBeta()
	if err != nil {
		pool.Close()
		stop()
		log.Error("beta config", "err", err)
		return nil, nil, nil, nil, err
	}
	engine.SetConsentMode(beta.Consent)
	return ctx, st, engine, func() { pool.Close(); stop() }, nil
}

// learnBaselines runs the weekly sleep-baseline estimate for every user now,
// whatever the weekday (after a deploy, or after changing its rules), then
// exits: scoring learn-baselines.
func learnBaselines(args []string) int {
	fs := flag.NewFlagSet("learn-baselines", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := httpx.Logger("scoring-learn")
	ctx, st, engine, done, err := offline(log)
	if err != nil {
		return 1
	}
	defer done()
	users, err := st.UserIDs(ctx)
	if err != nil {
		log.Error("list users", "err", err)
		return 1
	}
	outcomes, failed := map[string]int{}, 0
	for _, id := range users {
		user, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.Warn("load user", "user_id", id, "err", err)
			failed++
			continue
		}
		if !engine.Admits(user) {
			outcomes["no_consent"]++
			continue
		}
		outcome, err := engine.LearnBaseline(ctx, id, time.Now().In(loc).Format("2006-01-02"))
		if err != nil {
			log.Warn("learn sleep baseline", "user_id", id, "err", err)
			failed++
			continue
		}
		outcomes[outcome]++
	}
	log.Info("learn-baselines done", "users", len(users), "applied", outcomes[scoring.LearnApplied],
		"kept", outcomes[scoring.LearnKept], "insufficient", outcomes[scoring.LearnInsufficient], "no_consent", outcomes["no_consent"], "failed", failed)
	if failed > 0 {
		return 1
	}
	return 0
}
