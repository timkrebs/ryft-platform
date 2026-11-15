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

// On Cloud Run the lines are Cloud Logging's (design §6): severity with its names and message
// instead of level and msg, the trace of a request's line as projects/<p>/traces/<id>, and
// every other key as before.
func TestCloudFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewCloud(&buf, "api", slog.LevelDebug, "ryftlab-prod")
	ctx, f := NewContext(context.Background())
	f.RequestID, f.Trace = "req-1", "4bf92f3577b34da6a3ce929d0e0e4736"
	log.DebugContext(ctx, "d")
	log.With("extra", 1).InfoContext(ctx, "i", "k", "v")
	log.Warn("w")
	log.Error("e")
	log.Log(context.Background(), slog.LevelWarn+1, "between")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %d: %s", len(lines), buf.String())
	}
	var recs []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, m)
	}
	for i, want := range []struct{ sev, msg string }{{"DEBUG", "d"}, {"INFO", "i"}, {"WARNING", "w"}, {"ERROR", "e"}, {"WARNING", "between"}} {
		r := recs[i]
		if r["severity"] != want.sev || r["message"] != want.msg || r["level"] != nil || r["msg"] != nil || r["service"] != "api" || r["time"] == nil {
			t.Errorf("line %d = %v, want severity %s and message %q", i, r, want.sev, want.msg)
		}
	}
	if got := recs[1][TraceKey]; got != "projects/ryftlab-prod/traces/4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace = %v", got)
	}
	if recs[1]["request_id"] != "req-1" || recs[1]["k"] != "v" || recs[1]["extra"] != float64(1) {
		t.Errorf("the other keys stay: %v", recs[1])
	}
	if _, ok := recs[2][TraceKey]; ok {
		t.Error("a line without a request context carries a trace")
	}

	// No usable project: no trace field, the rest as above. The plain format never writes one.
	for name, l := range map[string]*slog.Logger{
		"cloud, no project":  NewCloud(&buf, "api", slog.LevelInfo, ""),
		"cloud, bad project": NewCloud(&buf, "api", slog.LevelInfo, "Not A Project"),
		"plain":              NewWriter(&buf, "api", slog.LevelInfo),
	} {
		buf.Reset()
		l.InfoContext(ctx, "i")
		if bytes.Contains(buf.Bytes(), []byte("traces")) {
			t.Errorf("%s: %s", name, buf.String())
		}
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
