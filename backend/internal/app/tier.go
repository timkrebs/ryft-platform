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

// taskPanic is the error of a task that panicked. Main exits 2 for it, as an
// unrecovered panic does.
type taskPanic struct{ v any }

func (p *taskPanic) Error() string { return fmt.Sprintf("panic: %v", p.v) }

// run starts fn as the task name. A panic is recovered, counted and logged, and
// is a failure. So is an error returned while the tier is still running; one
// returned after stop is the task winding down: logged at warn, not a failure.
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
				err = &taskPanic{v}
			}
			switch {
			case err != nil && (panicked || t.ctx.Err() == nil):
				t.log.Error(t.msg, "task", name, "err", err)
				t.fail(name, err)
			case err != nil:
				t.log.Warn("task ended with error during shutdown", "task", name, "err", oneLine(err))
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
func (t *tier) wait(grace limit) error {
	if t == nil {
		return nil
	}
	return within(grace, func() error { _ = t.g.Wait(); return nil })
}

// stop is stopping and wait.
func (t *tier) stop(grace limit) error {
	t.stopping()
	return t.wait(grace)
}

// limit is how long a shutdown step may wait. Only service.bound makes one, so
// every bound a step waits is the one the budget adds up (the compiler refuses a
// bare duration).
type limit time.Duration

// within runs fn and gives up waiting for it after d. fn keeps running when it
// overruns (nothing can cancel a blocked Close); the process is about to exit.
func within(d limit, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(time.Duration(d))
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out after %s", time.Duration(d))
	}
}
