package app

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// Main is what a service's main calls: Run under the process's one signal handler
// (the first SIGINT or SIGTERM starts the shutdown), then os.Exit with 0 when the
// service stopped cleanly, 1 when it failed and 2 when a task panicked (the exit an
// unrecovered panic gives, so alerts on the exit code see it). Run has logged why.
func Main(s Spec) {
	os.Exit(runSignals(s, defaultSeams(), syscall.SIGINT, syscall.SIGTERM))
}

func runSignals(s Spec, e seams, sigs ...os.Signal) int {
	ctx, stop := signal.NotifyContext(context.Background(), sigs...)
	defer stop()
	return exitCode(run(ctx, s, e))
}

// exitCode is 0 for a clean stop, 1 for a failure and 2 when a task panicked.
func exitCode(err error) int {
	var tp *taskPanic
	switch {
	case err == nil:
		return 0
	case errors.As(err, &tp):
		return 2
	}
	return 1
}
