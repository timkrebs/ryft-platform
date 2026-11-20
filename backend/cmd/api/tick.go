package main

import (
	"context"
	"fmt"
	"time"

	"github.com/timkrebs/ryft/internal/eventbus"
)

// tickBudget is the api's POST /internal/tick, all steps together (D219).
const tickBudget = 45 * time.Second

// tickSteps are the api's work between requests, run by the 15-minute tick on whichever
// instance it reaches, as Cloud Run gives no CPU outside a request (D219). Each was a
// timer goroutine of every pod before; all are idempotent, so one instance per tick is
// enough.
type tickSteps struct {
	ping      func(context.Context) error // db.Ping: ryft_db_up, ryft_db_ping_failures_total
	sweep     func(context.Context) error // the outbox's Sweep: what the inline flushes left, and the gauges
	reconcile func(context.Context)       // accounts.reconcile: it logs its own errors
	settings  func(context.Context)       // confirmGuard.Refresh: it logs and counts its own errors
	purge     func(context.Context) error // purgeInviteEmails
	attest    func(context.Context) error // purgeAppAttest
}

// ticks are the steps in order, under the names the tick logs.
func (s tickSteps) ticks() []eventbus.Tick {
	return []eventbus.Tick{
		{Name: "db ping", Run: s.ping},
		{Name: "outbox sweep", Run: s.sweep},
		{Name: "account reconcile", Run: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, reconcileBudget)
			defer cancel()
			s.reconcile(ctx)
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("the reconcile ran out of its %s, the next tick goes on: %w", reconcileBudget, err)
			}
			return nil
		}},
		{Name: "invite settings", Run: func(ctx context.Context) error { s.settings(ctx); return nil }},
		{Name: "invite email purge", Run: s.purge},
		{Name: "app attest purge", Run: s.attest},
	}
}
