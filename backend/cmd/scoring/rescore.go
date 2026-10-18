package main

import (
	"context"
	"flag"
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
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		log.Error("db", "err", err)
		return 1
	}
	defer pool.Close()
	st := store.New(pool)
	engine := scoring.NewEngine(st, log)

	users, err := st.UserIDs(ctx)
	if err != nil {
		log.Error("list users", "err", err)
		return 1
	}
	start, failed := time.Now(), 0
	for i, id := range users {
		_, loc, err := st.GetUser(ctx, id)
		if err != nil {
			log.Warn("load user", "user_id", id, "err", err)
			failed++
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
	log.Info("rescore done", "users", len(users), "failed", failed, "took", time.Since(start).Round(time.Second).String())
	if failed > 0 {
		return 1
	}
	return 0
}
