package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestContextFieldsOnEveryLine(t *testing.T) {
	var buf bytes.Buffer
	log := NewWriter(&buf, "api", slog.LevelInfo)
	ctx, f := NewContext(context.Background())
	f.RequestID, f.UserID, f.Route = "req-1", "user-1", "GET /v1/me"
	log.With("extra", 1).InfoContext(ctx, "hello", "k", "v")
	log.DebugContext(ctx, "hidden at info level")
	log.Info("no context")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (debug filtered), got %d: %s", len(lines), buf.String())
	}
	var first, second map[string]any
	_ = json.Unmarshal(lines[0], &first)
	_ = json.Unmarshal(lines[1], &second)
	for k, want := range map[string]any{"service": "api", "request_id": "req-1", "user_id": "user-1",
		"route": "GET /v1/me", "k": "v", "extra": float64(1)} {
		if first[k] != want {
			t.Errorf("%s = %v, want %v", k, first[k], want)
		}
	}
	if _, ok := second["request_id"]; ok {
		t.Error("a line without a request context must not carry a request_id")
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn,
		"error": slog.LevelError, "": slog.LevelInfo, "verbose": slog.LevelInfo} {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v", in, got)
		}
	}
}
