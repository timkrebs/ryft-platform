package events

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestGuardedTurnsAPanicIntoAnError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var nilMap map[string]*int
	err := guarded(log, SubjectRefresh, func() error { return errors.New(strings.Repeat("x", 1) + fmtNil(*nilMap["x"])) })
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("a panic must become an error: %v", err)
	}
	if err := guarded(log, SubjectRefresh, func() error { return nil }); err != nil {
		t.Fatalf("no panic, no error: %v", err)
	}
}

func fmtNil(int) string { return "" }
