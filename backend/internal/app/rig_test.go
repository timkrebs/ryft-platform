package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/timkrebs/ryft/internal/eventbus"
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

// after is the recorded entries after the first n (the start).
func (r *rec) after(n int) []string { return r.list()[n:] }

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

// text is everything written so far.
func (s *syncBuf) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.b.String())
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

// recListener records its first Close, which is the server beginning to shut down.
// Closing fail makes Accept return failErr; closing boom makes it panic.
type recListener struct {
	net.Listener
	rec  *rec
	tag  string
	once sync.Once

	fail    <-chan struct{}
	failErr error
	boom    <-chan struct{}
}

func (l *recListener) Close() error {
	l.once.Do(func() { l.rec.add(l.tag) })
	return l.Listener.Close()
}

func (l *recListener) Accept() (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	got := make(chan result, 1)
	go func() { c, err := l.Listener.Accept(); got <- result{c, err} }()
	select {
	case res := <-got:
		return res.c, res.err
	case <-l.fail:
		_ = l.Listener.Close() // ends the Accept above
		return nil, l.failErr
	case <-l.boom:
		_ = l.Listener.Close()
		panic("kaboom")
	}
}

// insecureLocal is a transport that dials nothing and checks no token: the emulator
// settings with ALLOW_INSECURE_PUSH, as compose runs (an unreachable emulator, as the
// tests publish nothing).
var insecureLocal = config.Config{
	Port: "8080", MetricsPort: "9090", XFFTrustedHops: config.DefaultXFFTrustedHops,
	PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
	Push:   config.Push{AllowInsecure: true},
}

// rig is a service with fakes for the pool, the database ping, the outbox's sweep and
// the ports, on a real transport (insecureLocal). Set its fields before the run.
type rig struct {
	rec  *rec
	logs *syncBuf
	e    seams

	loadErr, poolErr, busErr, buildErr, httpErr error
	otlpErr, pushErr                            error         // the push's settings; its result
	noPush                                      bool          // no OTLP endpoint: no push at all
	cloud                                       bool          // a Cloud Run service (K_SERVICE)
	poolBlocks                                  bool          // the pool waits for the signal
	poolGate                                    chan struct{} // non-nil: the pool opens once it is closed
	stuck                                       bool          // everything that can hang does, until free
	release                                     chan struct{} // closed by free
	freed                                       sync.Once
	started                                     chan struct{} // closed when the start is over
	startedOnce                                 sync.Once
	outboxKind                                  OutboxKind
	parts                                       Parts
	build                                       func(ctx context.Context) error // sees Build's ctx; its error is Build's
	httpFail                                    chan struct{}                   // closed: the HTTP listener's Accept fails
	httpBoom                                    chan struct{}                   // closed: the HTTP listener's Accept panics
	pingErr, sweepErr                           error                           // the tick's db ping and sweep
	refusals                                    int                             // the next opens that refuse the login (errRefusedLogin); under mu

	exits    []int                      // the codes the watchdog asked for
	bounds   map[string][]time.Duration // every bound the shutdown waited, by step
	outboxes []string                   // what each outbox app built relays into, and its publisher
	deps     Deps                       // what Build got

	mu    sync.Mutex
	addrs map[string]string
}

// errListener is what Accept returns once httpFail is closed.
var errListener = errors.New("accept tcp: use of a broken listener")

// errRefusedLogin is the rig's db.LoginRefused: a login refused on Cloud Run.
var errRefusedLogin = errors.New(`database: FATAL: password authentication failed for user "ryft_svc" (SQLSTATE 28P01)`)

func newRig() *rig {
	g := &rig{rec: &rec{}, logs: &syncBuf{}, addrs: map[string]string{}, bounds: map[string][]time.Duration{},
		release: make(chan struct{}), started: make(chan struct{}), outboxKind: EventOutbox,
		httpFail: make(chan struct{}), httpBoom: make(chan struct{})}
	log := slog.New(slog.NewJSONHandler(g.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g.e = seams{
		logger:     func(string) *slog.Logger { return log },
		loadConfig: func(string) (config.Config, error) { return insecureLocal, nil },
		newPush: func(service string, _ *slog.Logger) (func(context.Context) error, error) {
			if g.otlpErr != nil || g.noPush {
				return nil, g.otlpErr
			}
			if service != "svc" {
				return nil, fmt.Errorf("the push was built for %q", service)
			}
			return func(context.Context) error {
				g.rec.add("metrics push")
				if g.stuck {
					<-g.release // a push that ignores its context: the step's own bound must hold
				}
				return g.pushErr
			}, nil
		},
		cloudRun:  func() bool { return g.cloud },
		startWait: 2 * time.Second,
		openPool: func(ctx context.Context, _ config.Config, _ *slog.Logger) (*pgxpool.Pool, error) {
			g.rec.add("pool")
			if g.poolBlocks {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if g.poolGate != nil {
				select {
				case <-g.poolGate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if g.poolErr != nil {
				return nil, g.poolErr
			}
			g.mu.Lock()
			refused := g.refusals > 0
			if refused {
				g.refusals--
			}
			g.mu.Unlock()
			if refused {
				return nil, errRefusedLogin
			}
			return pgxpool.New(ctx, "postgres://u:p@127.0.0.1:1/x") // does not dial until used
		},
		loginRefused: func(err error) bool { return errors.Is(err, errRefusedLogin) },
		ping:         func(context.Context, *pgxpool.Pool) error { return nil },
		dbPing: func(_ context.Context, p *pgxpool.Pool, service string) error {
			if p == nil || service != "svc" {
				return fmt.Errorf("the tick pinged %v as %q", p, service)
			}
			g.rec.add("db ping")
			return g.pingErr
		},
		sweep: func(_ context.Context, ob *outbox.Outbox) error {
			g.mu.Lock()
			ok := ob != nil && ob == g.deps.Outbox
			g.mu.Unlock()
			if !ok {
				return errors.New("the tick swept another outbox than Build's")
			}
			g.rec.add("outbox sweep")
			return g.sweepErr
		},
		closePool: func(p *pgxpool.Pool) {
			g.rec.add("pool closed")
			if g.stuck {
				<-g.release
			}
			p.Close()
		},
		openBus: func(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (*eventbus.Transport, error) {
			g.rec.add("bus")
			if g.busErr != nil {
				return nil, g.busErr
			}
			return eventbus.Open(ctx, cfg, service, log)
		},
		newOutbox: func(pool *pgxpool.Pool, publisher string, to events.Sender, log *slog.Logger) *outbox.Outbox {
			g.mu.Lock()
			g.outboxes = append(g.outboxes, fmt.Sprintf("%s %T", publisher, to))
			g.mu.Unlock()
			return outbox.New(pool, publisher, to, log)
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
			l := &recListener{Listener: ln, rec: g.rec, tag: name + " closed"}
			if name == "http" {
				l.fail, l.failErr, l.boom = g.httpFail, errListener, g.httpBoom
			}
			return l, nil
		},
		exit: func(code int) {
			g.mu.Lock()
			g.exits = append(g.exits, code)
			g.mu.Unlock()
		},
		readyTimeout: 2 * time.Second,
		launched:     func(task string) { g.rec.add(task) },
		serverExit: func(string) { // a server whose Serve does not return: http.Server.Close cannot be made to wait for it, so the hook stands in
			if g.stuck {
				<-g.release
			}
		},
		bounded: func(step string, d time.Duration) {
			g.mu.Lock()
			g.bounds[step] = append(g.bounds[step], d)
			g.mu.Unlock()
		},
		startDone: func() { g.startedOnce.Do(func() { close(g.started) }) },
		bounds:    defaultBounds(),
	}
	return g
}

func (g *rig) spec() Spec {
	return Spec{
		Name: "svc", Outbox: g.outboxKind,
		Load: func(*slog.Logger) error {
			g.rec.add("load")
			return g.loadErr
		},
		Build: func(ctx context.Context, d Deps) (Parts, error) {
			g.rec.add("build")
			g.mu.Lock()
			g.deps = d
			g.mu.Unlock()
			if (d.Outbox == nil) != (g.outboxKind == NoOutbox) || d.Pool == nil || d.Bus == nil || d.Log == nil {
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

// serve starts the service and returns once it listens and the start is over: a
// cancel before that would be a signal during the start, which ends Build's context.
func (g *rig) serve(t *testing.T, ctx context.Context, s Spec) <-chan error {
	t.Helper()
	done := g.run(ctx, s)
	select {
	case <-g.started:
	case err := <-done:
		t.Fatalf("Run ended before it served: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the start did not finish")
	}
	eventually(t, func() bool { return g.addr("http") != "" }, "the listener")
	return done
}

// free lets everything that is stuck go.
func (g *rig) free() { g.freed.Do(func() { close(g.release) }) }

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
	code, _, body := g.do(t, http.MethodGet, path, "")
	return code, body
}

func (g *rig) do(t *testing.T, method, path, body string) (int, http.Header, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	req, err := http.NewRequest(method, "http://"+g.addr("http")+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, strings.TrimSpace(string(b))
}

// tickBody is the envelope Pub/Sub pushes from ryft-tick: data {} and ryft-key=tick.
const tickBody = `{"message":{"data":"e30=","attributes":{"ryft-key":"tick"},"messageId":"4242","publishTime":"2026-11-15T10:00:00Z"},` +
	`"subscription":"projects/ryft-local/subscriptions/tick-svc","deliveryAttempt":1}`

// tick posts one tick and returns its status.
func (g *rig) tick(t *testing.T) int {
	t.Helper()
	code, _, _ := g.do(t, http.MethodPost, eventbus.PathTick, tickBody)
	return code
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

// step is a tick step that records its name.
func step(r *rec, name string, err error) eventbus.Tick {
	return eventbus.Tick{Name: name, Run: func(context.Context) error { r.add(name); return err }}
}

// pushRoute is a consumer's push route: POST path, answered 204 and recorded.
type pushRoute struct {
	rec  *rec
	path string
}

func (p pushRoute) Register(mux *http.ServeMux) {
	mux.HandleFunc(http.MethodPost+" "+p.path, func(w http.ResponseWriter, _ *http.Request) {
		p.rec.add("push " + p.path)
		w.WriteHeader(http.StatusNoContent)
	})
}
