package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/logx"
)

func get(t *testing.T, h http.Handler, path string) (int, http.Header, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Header(), strings.TrimSpace(rec.Body.String())
}

// Off Cloud Run both pairs of paths answer as /healthz and /readyz always did (API.md → Health): the
// cause of a 503 is in the body, for compose and the smoke.
func TestHealthPathsOffCloudRun(t *testing.T) {
	t.Setenv("K_SERVICE", "")
	var down atomic.Bool
	mux := http.NewServeMux()
	Health(mux, func(context.Context) error {
		if down.Load() {
			return errors.New("db: closed pool")
		}
		return nil
	})
	for _, p := range []string{"/healthz", "/health/live"} {
		if code, _, body := get(t, mux, p); code != 200 || body != `{"status":"ok"}` {
			t.Errorf("%s = %d %s", p, code, body)
		}
	}
	for _, p := range []string{"/readyz", "/health/ready"} {
		down.Store(false)
		if code, _, body := get(t, mux, p); code != 200 || body != `{"status":"ready"}` {
			t.Errorf("%s = %d %s", p, code, body)
		}
		down.Store(true)
		if code, h, body := get(t, mux, p); code != 503 || body != `{"error":"db: closed pool"}` || h.Get("Retry-After") != "" {
			t.Errorf("%s down = %d %s %v", p, code, body, h)
		}
	}
}

// On Cloud Run (K_SERVICE) a ready path shows no cause: 503 {"error":"not ready"} without Retry-After,
// the cause logged at warn. Each answer is reused for 5 s, ok or not, so a burst of probes costs one
// check and one warning.
func TestHealthPathsOnCloudRun(t *testing.T) {
	t.Setenv("K_SERVICE", "api")
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(logx.NewWriter(&logs, "api", slog.LevelInfo))
	t.Cleanup(func() { slog.SetDefault(prev) })
	var calls atomic.Int32
	mux := http.NewServeMux()
	Health(mux, func(context.Context) error {
		calls.Add(1)
		return errors.New(`dial tcp 10.1.2.3:6543: connect: connection refused (password for "ryft_api")`)
	})
	for _, p := range []string{"/readyz", "/health/ready", "/health/ready"} {
		code, h, body := get(t, mux, p)
		if code != 503 || body != `{"error":"not ready"}` || h.Get("Retry-After") != "" || h.Get("Content-Type") != "application/json" {
			t.Errorf("%s = %d %s %v", p, code, body, h)
		}
	}
	// One handler, so one cache, serves both paths.
	if n := calls.Load(); n != 1 {
		t.Errorf("%d checks for three probes within 5 s, want 1", n)
	}
	if n := strings.Count(logs.String(), `"msg":"not ready"`); n != 1 || !strings.Contains(logs.String(), `"level":"WARN"`) ||
		!strings.Contains(logs.String(), "connection refused") {
		t.Errorf("want one warning with the cause, got %d: %s", n, logs.String())
	}
	if code, _, body := get(t, mux, "/health/live"); code != 200 || body != `{"status":"ok"}` {
		t.Errorf("/health/live = %d %s", code, body)
	}
}

// The cache's 5 s, with a clock: an ok answer is reused too, and a stale one is checked again. A
// probe that hangs up does not leave a cancelled check behind as the state.
func TestCloudReadyCache(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	var fail atomic.Bool
	c := &readyCache{log: slog.New(slog.DiscardHandler), timeout: time.Second, now: func() time.Time { return now },
		check: func(ctx context.Context) error {
			calls.Add(1)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if fail.Load() {
				return errors.New("down")
			}
			return nil
		}}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.result(gone); err != nil {
		t.Fatalf("a hung-up probe's check = %v: it must run on the request's values, not its cancel", err)
	}
	fail.Store(true)
	now = now.Add(ReadyCacheFor - time.Millisecond)
	if err := c.result(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("within 5 s: %v after %d checks, want the cached ok", err, calls.Load())
	}
	now = now.Add(time.Millisecond)
	if err := c.result(context.Background()); err == nil || calls.Load() != 2 {
		t.Fatalf("after 5 s: %v after %d checks, want a new check", err, calls.Load())
	}
	fail.Store(false)
	now = now.Add(time.Second)
	if err := c.result(context.Background()); err == nil || calls.Load() != 2 {
		t.Fatalf("a failure is cached too: %v after %d checks", err, calls.Load())
	}
}

func TestTraceID(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	for _, tc := range []struct {
		name, traceparent, cloud, want string
	}{
		{"traceparent", "00-" + id + "-00f067aa0ba902b7-01", "", id},
		{"traceparent wins", "00-" + id + "-00f067aa0ba902b7-01", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/1;o=1", id},
		{"X-Cloud-Trace-Context", "", strings.ToUpper(id) + "/12345678901234567890;o=1", id},
		{"X-Cloud-Trace-Context, no span", "", id, id},
		{"a bad traceparent falls back", "00-" + id + "-xyz-01", id + "/1", id},
		{"traceparent version ff", "ff-" + id + "-00f067aa0ba902b7-01", "", ""},
		{"all zeros", "00-" + zeroTrace + "-00f067aa0ba902b7-01", zeroTrace + "/1", ""},
		{"upper-case traceparent", "00-" + strings.ToUpper(id) + "-00f067aa0ba902b7-01", "", ""},
		{"too short", "", id[:31], ""},
		{"an injection attempt", "", id + `","severity":"ERROR`, ""},
		{"neither", "", "", ""},
	} {
		h := http.Header{}
		if tc.traceparent != "" {
			h.Set("traceparent", tc.traceparent)
		}
		if tc.cloud != "" {
			h.Set("X-Cloud-Trace-Context", tc.cloud)
		}
		if got := traceID(h); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Instrument puts the trace on the request's lines; in Cloud Logging's format they carry the trace field.
func TestInstrumentTracesTheRequestsLines(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewCloud(&buf, "api", slog.LevelInfo, "ryftlab-prod")
	h := Instrument(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.InfoContext(r.Context(), "inside")
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	r.Header.Set("X-Cloud-Trace-Context", "4bf92f3577b34da6a3ce929d0e0e4736/1;o=1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if n := strings.Count(buf.String(), `"logging.googleapis.com/trace":"projects/ryftlab-prod/traces/4bf92f3577b34da6a3ce929d0e0e4736"`); n != 2 {
		t.Errorf("want the trace on the handler's and the access line, got %d: %s", n, buf.String())
	}
}

// Detach keeps the request's values, survives the client's cancel, and ends with the server's hard stop.
func TestDetach(t *testing.T) {
	stop, hardStop := context.WithCancel(context.Background())
	defer hardStop()
	client, hangUp := context.WithCancel(context.WithValue(WithStop(stop), ctxKey{}, "v"))
	ctx, cancel := Detach(client, time.Minute)
	defer cancel()
	hangUp()
	if ctx.Err() != nil || ctx.Value(ctxKey{}) != "v" {
		t.Fatalf("the client's cancel reached the detached work, or its values are gone: %v", ctx.Err())
	}
	hardStop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the hard stop did not end the detached work")
	}

	plain, cancel2 := Detach(context.Background(), 20*time.Millisecond)
	defer cancel2()
	<-plain.Done()
	if !errors.Is(plain.Err(), context.DeadlineExceeded) {
		t.Errorf("without a server: %v", plain.Err())
	}
}
