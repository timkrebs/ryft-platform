package retry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeSleep records the waits and returns at once.
func fakeSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	after = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	t.Cleanup(func() { after = time.After })
	return &waits
}

func TestUntilBacksOffThenSucceeds(t *testing.T) {
	waits := fakeSleep(t)
	calls := 0
	err := Until(context.Background(), quiet, "db", Backoff{First: 5 * time.Second, Max: 30 * time.Second}, func(context.Context) error {
		calls++
		if calls < 6 {
			return errors.New("password authentication failed")
		}
		return nil
	})
	if err != nil || calls != 6 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
	// 5, 10, 20, 30, 30 s, each within ±20 %.
	want := []time.Duration{5, 10, 20, 30, 30}
	if len(*waits) != len(want) {
		t.Fatalf("waits %v", *waits)
	}
	for i, w := range want {
		got, base := (*waits)[i], w*time.Second
		if got < base*8/10 || got > base*12/10 {
			t.Errorf("wait %d = %v, want %v ±20 %%", i, got, base)
		}
	}
}

func TestUntilGivesUpWithTheLastError(t *testing.T) {
	fakeSleep(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Until(ctx, quiet, "amqp", Startup, func(context.Context) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return errors.New("connection refused")
	})
	if err == nil || err.Error() != "amqp: connection refused" || calls != 3 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestUntilFirstTryIsQuiet(t *testing.T) {
	waits := fakeSleep(t)
	if err := Until(context.Background(), quiet, "db", Startup, func(context.Context) error { return nil }); err != nil || len(*waits) != 0 {
		t.Fatalf("err %v, waits %v", err, *waits)
	}
}
