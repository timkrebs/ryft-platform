package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/scoring"
)

// dlq inspects and re-drives the dead-letter stream (docs/OPERATIONS.md →
// Dead letters), with scoring's own NATS user:
//
//	scoring dlq                     one JSON line per dead letter
//	scoring dlq redrive SEQ|all     handle scoring's (ryft.ingested, ryft.session.completed) again; delete on success
//	scoring dlq delete SEQ|all
//	scoring dlq drop                delete the stream itself, before rolling back to a release without it
//
// notify's dead letters (ryft.scored, ryft.session.scored) are only listed and
// deleted: a push hours late would be wrong.
func dlq(args []string) int {
	log := httpx.Logger("scoring")
	cfg, err := config.Load("scoring")
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	ctx := context.Background()
	bus, err := events.Connect(ctx, cfg.NatsURL, events.Options{Name: "scoring", User: cfg.NatsUser, Password: cfg.NatsPass})
	if err != nil {
		log.Error("nats", "err", err)
		return 1
	}
	defer bus.Close()
	letters, err := bus.DeadLetters(ctx)
	if err != nil {
		log.Error("list dead letters", "err", err)
		return 1
	}
	if len(args) == 1 && args[0] == "drop" {
		if len(letters) > 0 {
			log.Warn("dropping dead letters with the stream; list them first (scoring dlq) to keep a copy", "n", len(letters))
		}
		if err := bus.DropDeadLetterStream(ctx); err != nil {
			log.Error("drop stream", "err", err)
			return 1
		}
		log.Info("dead-letter stream deleted", "stream", events.DLQStreamName)
		return 0
	}
	if len(args) == 0 {
		enc := json.NewEncoder(os.Stdout)
		for _, l := range letters {
			_ = enc.Encode(l)
		}
		log.Info("dead letters", "n", len(letters))
		return 0
	}
	if len(args) != 2 || (args[0] != "redrive" && args[0] != "delete") {
		fmt.Fprintln(os.Stderr, "usage: scoring dlq [redrive SEQ|all | delete SEQ|all | drop]")
		return 2
	}
	var pick []events.DeadLetter
	for _, l := range letters {
		if args[1] == "all" || args[1] == strconv.FormatUint(l.Seq, 10) {
			pick = append(pick, l)
		}
	}
	var engine *scoring.Engine
	if args[0] == "redrive" {
		octx, _, e, done, err := offline(log)
		if err != nil {
			return 1
		}
		defer done()
		ctx, engine = octx, e
	}
	failed := 0
	for _, l := range pick {
		if args[0] == "redrive" {
			ev, err := events.DecodeIngested(l.Subject, []byte(l.Data))
			if err != nil {
				log.Warn("not re-driven", "seq", l.Seq, "subject", l.Subject, "err", err)
				continue
			}
			if err := handleEvent(ctx, engine, ev); err != nil {
				log.Warn("re-drive failed, kept", "seq", l.Seq, "user_id", ev.UserID, "err", err)
				failed++
				continue
			}
		}
		if err := bus.DeleteDeadLetter(ctx, l.Seq); err != nil {
			log.Warn("delete failed", "seq", l.Seq, "err", err)
			failed++
			continue
		}
		log.Info(map[string]string{"redrive": "dead letter re-driven", "delete": "dead letter deleted"}[args[0]],
			"seq", l.Seq, "subject", l.Subject)
	}
	if failed > 0 {
		return 1
	}
	return 0
}
