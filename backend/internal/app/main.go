package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Main is what a service's main calls: Run under the process's one signal handler
// (the first SIGINT or SIGTERM starts the shutdown), then os.Exit with 0 when the
// service stopped cleanly and 1 when it failed. Run has logged why.
func Main(s Spec) {
	os.Exit(runSignals(s, defaultSeams(), syscall.SIGINT, syscall.SIGTERM))
}

func runSignals(s Spec, e seams, sigs ...os.Signal) int {
	ctx, stop := signal.NotifyContext(context.Background(), sigs...)
	defer stop()
	if err := run(ctx, s, e); err != nil {
		return 1
	}
	return 0
}
