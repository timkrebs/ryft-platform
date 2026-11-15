package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

// pingFunc is a Pinger.
type pingFunc func(ctx context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

// The tick's ping sets ryft_db_up and counts a failure; a ping cut by its caller
// (ctx ended) is no verdict and sets nothing. Each case has its own service label,
// so the cases do not see each other's numbers.
func TestPingReportsTheDatabase(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:5432: connection refused")
	for _, c := range []struct {
		name      string
		ping      func(ctx context.Context) error
		cancelled bool
		wantErr   string // "" for none
		up        float64
		failures  float64
		set       bool // whether ryft_db_up is checked (a cut ping sets nothing; failures stays 0)
	}{
		{name: "reachable", ping: func(context.Context) error { return nil }, up: 1, set: true},
		{name: "unreachable", ping: func(context.Context) error { return refused }, wantErr: "database unreachable: dial tcp", failures: 1, set: true},
		{name: "slower than the timeout", ping: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			wantErr: "database unreachable: context deadline exceeded", failures: 1, set: true},
		{name: "the caller hung up", ping: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, cancelled: true,
			wantErr: "context canceled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			service := "ping-" + strings.ReplaceAll(c.name, " ", "-")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancelled {
				cancel()
			}
			ping := c.ping
			if c.name == "slower than the timeout" {
				// PingTimeout is 5 s; a deadline sooner than that stands in for it.
				short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
				ping = func(context.Context) error { return c.ping(short) }
			}
			err := Ping(ctx, pingFunc(ping), service)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("Ping = %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("Ping = %v, want %q", err, c.wantErr)
			}
			if got := testutil.ToFloat64(metrics.DBPingFailures.WithLabelValues(service)); got != c.failures {
				t.Errorf("ryft_db_ping_failures_total = %v, want %v", got, c.failures)
			}
			if c.set {
				if got := testutil.ToFloat64(metrics.DBUp.WithLabelValues(service)); got != c.up {
					t.Errorf("ryft_db_up = %v, want %v", got, c.up)
				}
			}
		})
	}
}

// The ping gets its own short deadline, so a stuck pooler cannot take the tick's budget.
func TestPingHasItsOwnDeadline(t *testing.T) {
	var deadline time.Time
	_ = Ping(context.Background(), pingFunc(func(ctx context.Context) error {
		deadline, _ = ctx.Deadline()
		return nil
	}), "ping-deadline")
	if left := time.Until(deadline); left <= 0 || left > PingTimeout {
		t.Fatalf("the ping's deadline is %v away, want at most %v", left, PingTimeout)
	}
}
