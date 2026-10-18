// Package retry runs start-up steps that depend on something that may not be up
// yet: the database before the migrate job has set the role passwords, NATS
// before its pod is ready.
//
// Exiting and letting Kubernetes restart the pod would retry too, but fast and
// in lockstep across pods. Supabase's pooler blocks a user for a while after
// repeated failed logins (ECIRCUITBREAKER), which turned a few seconds of
// "password not set yet" into minutes of crash loops on the first deploy.
package retry

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Backoff is the wait between attempts: First, doubling up to Max, each with
// ±20 % jitter so pods that started together drift apart.
type Backoff struct{ First, Max time.Duration }

// Startup suits a dependency that comes up within minutes.
var Startup = Backoff{First: 5 * time.Second, Max: time.Minute}

// after is time.After; tests replace it.
var after = time.After

// Until calls fn until it succeeds or ctx ends, logging every failed attempt.
// It returns the last error, wrapped with what.
func Until(ctx context.Context, log *slog.Logger, what string, b Backoff, fn func(context.Context) error) error {
	wait := b.First
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			if attempt > 1 {
				log.InfoContext(ctx, what+" ready", "attempts", attempt)
			}
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		d := jitter(wait)
		log.WarnContext(ctx, what+" not ready, retrying", "attempt", attempt, "retry_in", d.Round(time.Second).String(), "err", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, err)
		case <-after(d):
		}
		wait = min(wait*2, b.Max)
	}
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // spread, not secrecy
}
