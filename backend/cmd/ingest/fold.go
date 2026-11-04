package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// foldRequest is the fold of one sync (D-c): the request's workout sources, starts from
// one day before its first workout to one day after its last. nil without workouts.
func foldRequest(ws []model.Workout) *store.FoldScope {
	if len(ws) == 0 {
		return nil
	}
	seen := map[string]bool{}
	sc := store.FoldScope{Sources: []string{}, From: ws[0].Start, To: ws[0].Start}
	for _, w := range ws {
		if !seen[w.Source] {
			seen[w.Source] = true
			sc.Sources = append(sc.Sources, w.Source)
		}
		if w.Start.Before(sc.From) {
			sc.From = w.Start
		}
		if w.Start.After(sc.To) {
			sc.To = w.Start
		}
	}
	sc.From, sc.To = sc.From.Add(-24*time.Hour), sc.To.Add(24*time.Hour)
	return &sc
}

// foldWorkouts is `ingest fold-workouts [-user <uuid>] [-dry-run]`: the one-off for copies
// uploaded before the app sent deletions (D-c). It exits non-zero when any user failed.
func foldWorkouts(args []string) int {
	log := httpx.Logger("ingest-fold")
	cfg, err := config.Load("ingest")
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
	beta, err := config.LoadBeta() // the consent mode, as scoring's rescore reads it
	if err != nil {
		log.Error("beta config", "err", err)
		return 1
	}
	return runFold(ctx, store.New(pool), beta.Consent, args, os.Stdout, log)
}

// runFold is foldWorkouts after the connection, so tests can hand in a store.
func runFold(ctx context.Context, st *store.Store, consent config.Mode, args []string, out io.Writer, log *slog.Logger) int {
	fs := flag.NewFlagSet("fold-workouts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	user := fs.String("user", "", "fold only this user (uuid); default every user")
	dry := fs.Bool("dry-run", false, "list what would be folded; write nothing")
	all := fs.Bool("all", false, "a real run (no -dry-run) over every user; required when -user is not given")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 { // "fold-workouts dry-run" or "<uuid> -dry-run" would otherwise run for real
		log.Error("unexpected arguments; flags only: -user <uuid>, -dry-run, -all", "n", fs.NArg())
		return 2
	}
	if *user != "" && *all {
		log.Error("-user and -all exclude each other")
		return 2
	}
	if *user == "" && !*dry && !*all {
		log.Error("a real run over every user needs -all; use -user <uuid> for one, or -dry-run to list")
		return 2
	}
	var users []uuid.UUID
	if *user != "" {
		id, err := uuid.Parse(*user)
		if err != nil {
			log.Error("-user must be a uuid")
			return 2
		}
		users = []uuid.UUID{id}
	} else {
		var err error
		if users, err = st.UserIDs(ctx); err != nil {
			log.Error("list users", "err", err)
			return 1
		}
	}
	var folded, groups, affected, failed, skipped int
	for i, id := range users {
		if ctx.Err() != nil {
			break
		}
		u, _, err := st.GetUser(ctx, id)
		if err == nil && u.Consent == nil && consent == config.ModeOn { // no consent, no processing (as scoring)
			skipped++
			continue
		}
		var gs []store.FoldGroup
		if err == nil {
			gs, err = st.FoldWorkouts(ctx, id, *dry, func(tx pgx.Tx, days []string) error {
				m, err := events.Ingested(model.IngestedEvent{UserID: id, Days: days, Reason: model.ReasonSync, RequestID: "fold-workouts"}, "")
				if err != nil {
					return err
				}
				return outbox.Insert(ctx, tx, "ingest", m)
			})
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Error("fold user", "user_id", id, "n", i+1, "of", len(users), "err", err)
			}
			failed++
			continue
		}
		if len(gs) > 0 {
			affected++
		}
		for _, g := range gs {
			groups++
			folded += len(g.Folded)
			fmt.Fprintf(out, "user=%s day=%s kept=%s folded=%s\n", id, g.Day, g.Keep, joinIDs(g.Folded))
		}
	}
	mode := "folded"
	if *dry {
		mode = "would fold"
	}
	fmt.Fprintf(out, "%s %d workouts in %d groups, %d of %d users, %d skipped without consent, %d failed\n", mode, folded, groups, affected, len(users), skipped, failed)
	if err := ctx.Err(); err != nil {
		log.Error("interrupted", "err", err)
		return 1
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func joinIDs(ids []uuid.UUID) string {
	s := ""
	for i, id := range ids {
		if i > 0 {
			s += ","
		}
		s += id.String()
	}
	return s
}
