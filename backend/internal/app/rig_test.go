package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/goleak"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// rec records what the fakes were asked to do, in order.
type rec struct {
	mu  sync.Mutex
	got []string
}

func (r *rec) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, s)
}

func (r *rec) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// waitLen blocks until n entries are recorded.
func (r *rec) waitLen(t *testing.T, n int) {
	t.Helper()
	eventually(t, func() bool { return len(r.list()) >= n }, "records, have "+strings.Join(r.list(), ", "))
}

// shutdown is the recorded entries after the first n (the start), with the
// relay and the db watch, which stop together, as one entry.
func (r *rec) shutdown(n int) []string {
	var out []string
	for _, s := range r.list()[n:] {
		if s == "watch stopped" {
			s = "relay stopped"
		}
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func eventually(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// syncBuf is a log sink the service's goroutines can write to.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) lines() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		m := map[string]any{}
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// find returns the log lines with the message msg.
func (s *syncBuf) find(msg string) []map[string]any {
	var out []map[string]any
	for _, m := range s.lines() {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// fakeBus is an events.Bus that only knows whether it is ready and that it was closed.
type fakeBus struct {
	events.Bus
	rec   *rec
	ready func(context.Context) error
}

func (b *fakeBus) Ready(ctx context.Context) error {
	if b.ready != nil {
		return b.ready(ctx)
	}
	return nil
}

func (b *fakeBus) Close() { b.rec.add("bus closed") }

// recListener records its first Close, which is the server beginning to shut down.
type recListener struct {
	net.Listener
	rec  *rec
	tag  string
	once sync.Once
}

func (l *recListener) Close() error {
	l.once.Do(func() { l.rec.add(l.tag) })
	return l.Listener.Close()
}

// rig is a service with fakes for the pool, the broker and the ports. Set its fields
// before the run.
type rig struct {
	rec  *rec
	logs *syncBuf
	bus  *fakeBus
	e    seams

	loadErr, poolErr, busErr, buildErr, httpErr error
	poolBlocks                                  bool
	noOutbox                                    bool
	parts                                       Parts
	build                                       func(ctx context.Context) error // sees Build's ctx; its error is Build's

	exits []int // the codes the watchdog asked for

	mu    sync.Mutex
	addrs map[string]string
}

func newRig() *rig {
	g := &rig{rec: &rec{}, logs: &syncBuf{}, addrs: map[string]string{}}
	g.bus = &fakeBus{rec: g.rec}
	log := slog.New(slog.NewJSONHandler(g.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g.e = seams{
		logger:     func(string) *slog.Logger { return log },
		loadConfig: func(string) (config.Config, error) { return config.Config{Port: "8080", MetricsPort: "9090"}, nil },
		openPool: func(ctx context.Context, _ config.Config, _ *slog.Logger) (*pgxpool.Pool, error) {
			g.rec.add("pool")
			if g.poolBlocks {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if g.poolErr != nil {
				return nil, g.poolErr
			}
			return pgxpool.New(ctx, "postgres://u:p@127.0.0.1:1/x") // does not dial until used
		},
		watch: func(ctx context.Context, _ *pgxpool.Pool, _ string, _ *slog.Logger) {
			<-ctx.Done()
			g.rec.add("watch stopped")
		},
		ping: func(context.Context, *pgxpool.Pool) error { return nil },
		closePool: func(p *pgxpool.Pool) {
			g.rec.add("pool closed")
			p.Close()
		},
		openBus: func(context.Context, config.Config, string, *slog.Logger) (events.Bus, error) {
			g.rec.add("bus")
			if g.busErr != nil {
				return nil, g.busErr
			}
			return g.bus, nil
		},
		relay: func(ctx context.Context, _ *outbox.Outbox) {
			<-ctx.Done()
			g.rec.add("relay stopped")
		},
		listen: func(_ context.Context, addr string) (net.Listener, error) {
			name := map[string]string{":8080": "http", ":9090": "metrics"}[addr]
			if name == "http" && g.httpErr != nil {
				return nil, g.httpErr
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			g.mu.Lock()
			g.addrs[name] = ln.Addr().String()
			g.mu.Unlock()
			return &recListener{Listener: ln, rec: g.rec, tag: name + " closed"}, nil
		},
		exit: func(code int) {
			g.mu.Lock()
			g.exits = append(g.exits, code)
			g.mu.Unlock()
		},
		readyTimeout:   2 * time.Second,
		launched:       g.rec.add,
		consumerMargin: consumerMargin, stepWait: stepWait, afterClose: afterClose,
	}
	return g
}

func (g *rig) spec() Spec {
	return Spec{
		Name: "svc", Outbox: !g.noOutbox,
		Load: func(*slog.Logger) error {
			g.rec.add("load")
			return g.loadErr
		},
		Build: func(ctx context.Context, d Deps) (Parts, error) {
			g.rec.add("build")
			if (d.Outbox != nil) == g.noOutbox || d.Pool == nil || d.Bus == nil || d.ConsumerDrain <= 0 {
				return Parts{}, errors.New("Build got the wrong Deps")
			}
			if g.build != nil {
				if err := g.build(ctx); err != nil {
					return Parts{}, err
				}
			}
			return g.parts, g.buildErr
		},
	}
}

// run starts the service and returns the channel its result arrives on.
func (g *rig) run(ctx context.Context, s Spec) <-chan error {
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, g.e) }()
	return done
}

// serve starts the service and returns once it listens.
func (g *rig) serve(t *testing.T, ctx context.Context, s Spec) <-chan error {
	t.Helper()
	done := g.run(ctx, s)
	eventually(t, func() bool { return g.addr("http") != "" }, "the listener")
	return done
}

func (g *rig) exitCodes() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.exits...)
}

func (g *rig) addr(name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addrs[name]
}

// get calls the service's HTTP port.
func (g *rig) get(t *testing.T, path string) (int, string) {
	t.Helper()
	code, _, body := g.do(t, path)
	return code, body
}

func (g *rig) do(t *testing.T, path string) (int, http.Header, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + g.addr("http") + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, strings.TrimSpace(string(b))
}

// result waits for Run's answer.
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// until is a task that records tag when its ctx ends.
func until(r *rec, tag string) func(context.Context) error {
	return func(ctx context.Context) error {
		<-ctx.Done()
		r.add(tag)
		return nil
	}
}
