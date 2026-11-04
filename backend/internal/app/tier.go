package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/timkrebs/ryft/internal/metrics"
)

// tier is a set of goroutines that stop together: one context, one errgroup. The
// service has one per kind of work (timers, consumers, HTTP, relay and watch), so
// the shutdown can stop them in order instead of all at the signal.
type tier struct {
	log    *slog.Logger
	msg    string // the log message of a failing task: "consume", "serve", ...
	fail   func(task string, err error)
	launch func(task string) // a test hook: called as a task starts (nil in production)
	ctx    context.Context
	cancel context.CancelFunc
	g      errgroup.Group
}

func newTier(parent context.Context, log *slog.Logger, msg string, fail func(task string, err error)) *tier {
	ctx, cancel := context.WithCancel(parent)
	return &tier{log: log, msg: msg, fail: fail, ctx: ctx, cancel: cancel}
}

// run starts fn as the task name. A panic is recovered, counted and logged, and
// is a failure. So is an error returned while the tier is still running; one
// returned after stop is the task winding down and is dropped.
func (t *tier) run(name string, fn func(ctx context.Context) error) {
	if t.launch != nil {
		t.launch(name)
	}
	t.g.Go(func() (err error) {
		panicked := false
		defer func() {
			if v := recover(); v != nil {
				panicked = true
				metrics.GoroutinePanics.Inc()
				t.log.Error("panic", "task", name, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				err = fmt.Errorf("panic: %v", v)
			}
			if err != nil && (panicked || t.ctx.Err() == nil) {
				t.log.Error(t.msg, "task", name, "err", err)
				t.fail(name, err)
			}
		}()
		return fn(t.ctx)
	})
}

// stopping ends the tier's context without waiting. A nil tier (the start never
// got that far) is already stopped.
func (t *tier) stopping() {
	if t != nil {
		t.cancel()
	}
}

// wait blocks until every task has returned, at most grace.
func (t *tier) wait(grace time.Duration) error {
	if t == nil {
		return nil
	}
	return within(grace, func() error { _ = t.g.Wait(); return nil })
}

// stop is stopping and wait.
func (t *tier) stop(grace time.Duration) error {
	t.stopping()
	return t.wait(grace)
}

// within runs fn and gives up waiting for it after d. fn keeps running when it
// overruns (nothing can cancel a blocked Close); the process is about to exit.
func within(d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out after %s", d)
	}
}
