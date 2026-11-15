package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// unavailableBody is the API's 503 for a request that came before the start was done.
const unavailableBody = `{"error":"temporarily unavailable, send the request again"}`

// pingRoute is GET /v1/ping answering pong.
func pingRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })
}

// waitForPort runs the service and returns once it listens, the start still going.
func (g *rig) waitForPort(t *testing.T, ctx context.Context, s Spec) <-chan error {
	t.Helper()
	done := g.run(ctx, s)
	eventually(t, func() bool { return g.addr("http") != "" }, "the listener")
	return done
}

// The port opens before the pool (Cloud Run's startup probe is /health/live): while the
// database is not there, the live paths answer 200, the ready paths 503, and a request
// waits for the start and is served once it is done.
func TestTheStartGate(t *testing.T) {
	g := newRig()
	g.poolGate = make(chan struct{})
	g.parts.Routes = pingRoute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := g.waitForPort(t, ctx, g.spec())
	for _, path := range []string{"/health/live", "/healthz"} {
		if code, body := g.get(t, path); code != 200 || body != `{"status":"ok"}` {
			t.Errorf("%s during the start = %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/health/ready", "/readyz"} {
		if code, body := g.get(t, path); code != 503 || body != `{"error":"starting"}` {
			t.Errorf("%s during the start = %d %s", path, code, body)
		}
	}
	type answer struct {
		code int
		body string
	}
	waiting := make(chan answer, 1)
	go func() {
		code, _, body := g.do(t, http.MethodGet, "/v1/ping", "")
		waiting <- answer{code, body}
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case a := <-waiting:
		t.Fatalf("answered %d %s before the start was done", a.code, a.body)
	default:
	}
	close(g.poolGate)
	if a := <-waiting; a.code != 200 || a.body != "pong" {
		t.Errorf("the waiting request = %d %s, want the route's answer", a.code, a.body)
	}
	<-g.started
	if code, body := g.get(t, "/health/ready"); code != 200 || body != `{"status":"ready","checks":{"db":"ok"}}` {
		t.Errorf("/health/ready after the start = %d %s", code, body)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
}

// A request that waited its time (a few seconds, and never past its own context) is
// answered with the API's 503 and Retry-After; a signal during the wait is a clean stop.
func TestAWaitingRequestGetsTheAPIs503(t *testing.T) {
	g := newRig()
	g.poolGate = make(chan struct{})
	g.e.startWait = 50 * time.Millisecond
	g.parts.Routes = pingRoute
	s := g.spec()
	s.Edge = true // the edge chain answers too: a request id, the security headers
	ctx, cancel := context.WithCancel(context.Background())
	done := g.waitForPort(t, ctx, s)
	begun := time.Now()
	code, header, body := g.do(t, http.MethodGet, "/v1/ping", "")
	if code != 503 || header.Get("Retry-After") != "5" || !strings.HasPrefix(body, strings.TrimSuffix(unavailableBody, "}")) ||
		!strings.Contains(body, `"request_id":"`) || header.Get("Cache-Control") != "no-store" {
		t.Errorf("a request during the start = %d %v %s", code, header, body)
	}
	if took := time.Since(begun); took < 50*time.Millisecond || took > 2*time.Second {
		t.Errorf("answered after %v, want the start wait of 50 ms", took)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Errorf("Run = %v, a signal during the start is a clean stop", err)
	}
	if got := g.rec.list(); !slices.Equal(got, []string{"load", "bus", "http", "metrics", "pool", "http closed", "metrics push", "metrics closed"}) {
		t.Errorf("records %v", got)
	}
	if len(g.logs.find("db")) != 0 || len(g.logs.find("start interrupted by the signal")) != 1 {
		t.Errorf("the interrupted wait is logged as an error: %v", g.logs.lines())
	}
}

// A tick before the pool is open reports the database down: ryft_db_up 0, a failed tick,
// the metrics push, and a non-2xx answer so Pub/Sub sends it again.
func TestATickBeforeThePoolIsOpen(t *testing.T) {
	g := newRig()
	g.poolGate = make(chan struct{})
	g.e.startWait = 50 * time.Millisecond
	metrics.DBUp.WithLabelValues("svc").Set(1)
	failed := testutil.ToFloat64(metrics.Ticks.WithLabelValues("svc", "error"))
	ctx, cancel := context.WithCancel(context.Background())
	done := g.waitForPort(t, ctx, g.spec())
	code, header, body := g.do(t, http.MethodPost, eventbus.PathTick, tickBody)
	if code != 503 || header.Get("Retry-After") != "5" || body != unavailableBody {
		t.Errorf("an early tick = %d %v %s", code, header, body)
	}
	if up := testutil.ToFloat64(metrics.DBUp.WithLabelValues("svc")); up != 0 {
		t.Errorf("ryft_db_up = %v, want 0", up)
	}
	if n := testutil.ToFloat64(metrics.Ticks.WithLabelValues("svc", "error")) - failed; n != 1 {
		t.Errorf("ryft_ticks_total{result=error} rose by %v, want 1", n)
	}
	if !slices.Contains(g.rec.list(), "metrics push") {
		t.Errorf("the early tick pushed nothing: %v", g.rec.list())
	}
	if m := g.logs.find("tick before the start was done"); len(m) != 1 || m[0]["waited"] == nil || m[0]["level"] != "WARN" {
		t.Errorf("early tick line: %v", m)
	}
	close(g.poolGate)
	<-g.started
	if code := g.tick(t); code != http.StatusNoContent {
		t.Errorf("a tick after the start = %d", code)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if got := g.rec.list(); !slices.Contains(got, "db ping") {
		t.Errorf("the tick after the start ran no db ping: %v", got)
	}
}

// On Cloud Run (K_SERVICE) the ready paths answer 503 {"error":"not ready"} without
// Retry-After and with the cause only in the log, the answer reused for 5 s; there is no
// /metrics listener (the push replaces it).
func TestCloudRunReadyAndNoMetricsPort(t *testing.T) {
	g := newRig()
	g.cloud = true
	g.poolGate = make(chan struct{})
	var pings atomic.Int32
	g.e.ping = func(context.Context, *pgxpool.Pool) error { pings.Add(1); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := g.waitForPort(t, ctx, g.spec())
	for range 3 {
		for _, path := range []string{"/health/ready", "/readyz"} {
			code, header, body := g.do(t, http.MethodGet, path, "")
			if code != 503 || body != `{"error":"not ready"}` || header.Get("Retry-After") != "" {
				t.Errorf("%s during the start = %d %v %s", path, code, header, body)
			}
		}
	}
	if m := g.logs.find("not ready"); len(m) != 1 || m[0]["level"] != "WARN" || !strings.Contains(m[0]["err"].(string), "starting") {
		t.Errorf("not ready lines (one per 5 s): %v", m)
	}
	if code, body := g.get(t, "/health/live"); code != 200 || body != `{"status":"ok"}` {
		t.Errorf("/health/live = %d %s", code, body)
	}
	close(g.poolGate)
	<-g.started
	for range 2 {
		if code, body := g.get(t, "/health/ready"); code != 200 || body != `{"status":"ready"}` {
			t.Errorf("/health/ready after the start = %d %s", code, body)
		}
	}
	if n := pings.Load(); n != 1 {
		t.Errorf("%d db pings for two probes within 5 s, want 1", n)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	want := []string{"load", "bus", "http", "pool", "build", "http closed", "metrics push", "pool closed"}
	if got := g.rec.list(); !slices.Equal(got, want) || g.addr("metrics") != "" {
		t.Errorf("records %v (metrics at %q)\nwant    %v and no metrics port", got, g.addr("metrics"), want)
	}
}

// The push and the tick's final step are the metrics push: without an OTLP endpoint there
// is none, and the shutdown has no push step.
func TestNoPushWithoutAnEndpoint(t *testing.T) {
	g := newRig()
	g.noPush = true
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, g.spec())
	if code := g.tick(t); code != http.StatusNoContent {
		t.Errorf("tick = %d", code)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if got := g.rec.list(); slices.Contains(got, "metrics push") {
		t.Errorf("pushed without an endpoint: %v", got)
	}
	for _, m := range g.logs.find("shutdown step") {
		if m["step"] == "push" {
			t.Errorf("a push step without a push: %v", m)
		}
	}
}

// Parts.Handler is the service's whole mux (api, ingest): app serves it behind the gate,
// mounts nothing of its own, and Deps.Tick builds a tick of exactly the given steps with
// the metrics push as its final step. With Routes beside it the start fails.
func TestPartsHandler(t *testing.T) {
	t.Run("the service's mux", func(t *testing.T) {
		g := newRig()
		g.build = func(context.Context) error { return nil }
		s := g.spec()
		build := s.Build
		s.Build = func(ctx context.Context, d Deps) (Parts, error) {
			if _, err := build(ctx, d); err != nil {
				return Parts{}, err
			}
			tick, err := d.Tick(step(g.rec, "own", nil))
			if err != nil {
				return Parts{}, err
			}
			mux := http.NewServeMux()
			httpx.Health(mux, func(context.Context) error { return nil })
			mux.Handle("POST "+eventbus.PathTick, tick)
			pingRoute(mux)
			return Parts{Handler: mux}, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		if code, body := g.get(t, "/readyz"); code != 200 || body != `{"status":"ready"}` {
			t.Errorf("/readyz = %d %s: the service's own health answer", code, body)
		}
		if code, body := g.get(t, "/v1/ping"); code != 200 || body != "pong" {
			t.Errorf("/v1/ping = %d %s", code, body)
		}
		if code := g.tick(t); code != http.StatusNoContent {
			t.Errorf("tick = %d", code)
		}
		cancel()
		if err := result(t, done); err != nil {
			t.Fatal(err)
		}
		if got, want := g.rec.after(len(started))[:2], []string{"own", "metrics push"}; !slices.Equal(got, want) {
			t.Errorf("the tick ran %v, want %v (only the given steps, then the push)", got, want)
		}
	})
	t.Run("with Routes beside it", func(t *testing.T) {
		g := newRig()
		g.parts = Parts{Handler: http.NotFoundHandler(), Routes: pingRoute}
		if err := run(context.Background(), g.spec(), g.e); err == nil || !strings.Contains(err.Error(), "Parts.Handler") {
			t.Fatalf("Run = %v", err)
		}
		if m := g.logs.find("build"); len(m) != 1 {
			t.Errorf("build line: %v", m)
		}
	})
}

// Review-a1 finding 6: a request (a push, a tick, an account deletion) still running when
// the http step's Shutdown runs out has its context ended (the handlers' base context, and
// httpx.Detach's on it), so its database call returns and gives its connection back, and
// the pool closes within its bound. Over HTTP/1.1 and h2c, as Cloud Run's front end speaks.
func TestAnInFlightRequestGivesItsConnectionBack(t *testing.T) {
	for _, h2c := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP/1.1", true: "h2c"}[h2c], func(t *testing.T) {
			g := newRig()
			entered, released := make(chan struct{}), make(chan struct{})
			var cause atomic.Value
			g.parts.Routes = func(mux *http.ServeMux) {
				mux.HandleFunc("POST /v1/hold", func(w http.ResponseWriter, r *http.Request) {
					// What a store call on a detached context does (account deletion, the audit row):
					// it holds a connection until its context ends.
					ctx, cancel := httpx.Detach(r.Context(), time.Minute)
					defer cancel()
					close(entered)
					<-ctx.Done()
					cause.Store(r.Context().Err() != nil)
					close(released)
				})
			}
			// pgxpool.Close waits for every acquired connection.
			g.e.closePool = func(p *pgxpool.Pool) { g.rec.add("pool closed"); <-released; p.Close() }
			s := g.spec()
			s.ShutdownTimeout = 100 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			done := g.serve(t, ctx, s)
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
			if h2c {
				var p http.Protocols
				p.SetUnencryptedHTTP2(true)
				client = &http.Client{Transport: &http.Transport{Protocols: &p}, Timeout: 10 * time.Second}
			}
			go func() {
				if resp, err := client.Post("http://"+g.addr("http")+"/v1/hold", "application/json", strings.NewReader("{}")); err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}()
			<-entered
			cancel()
			err := result(t, done)
			if err == nil || !strings.Contains(err.Error(), "http: ") {
				t.Errorf("Run = %v, want the http step's overrun", err)
			}
			if err != nil && strings.Contains(err.Error(), "pool: ") {
				t.Errorf("the pool step overran: the push held its connection past the http step: %v", err)
			}
			if v, _ := cause.Load().(bool); !v {
				t.Error("the request's own context did not end either")
			}
			client.CloseIdleConnections()
		})
	}
}

// The gate's own answers, without a service around it.
func TestGateAnswers(t *testing.T) {
	quiet := slog.New(slog.DiscardHandler)
	t.Run("an aborted start answers the waiting requests at once", func(t *testing.T) {
		g := newGate("svc", quiet, auth.InsecurePush(quiet), nil, false, time.Minute)
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
			answered <- w
		}()
		time.Sleep(20 * time.Millisecond)
		g.abort()
		select {
		case w := <-answered:
			if w.Code != 503 || w.Header().Get("Retry-After") != "5" || strings.TrimSpace(w.Body.String()) != unavailableBody {
				t.Errorf("after abort = %d %v %s", w.Code, w.Header(), w.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the waiting request was not answered after abort")
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
		if w.Code != 503 {
			t.Errorf("a request after abort = %d", w.Code)
		}
	})
	t.Run("a request that hangs up stops waiting", func(t *testing.T) {
		g := newGate("svc", quiet, auth.InsecurePush(quiet), nil, false, time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		begun := time.Now()
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ping", nil).WithContext(ctx))
		if w.Code != 503 || time.Since(begun) > time.Second {
			t.Errorf("%d after %v, want 503 at the request's deadline", w.Code, time.Since(begun))
		}
	})
	for _, tc := range []struct {
		name    string
		err     error
		code    int
		retry   string
		logLine string
	}{
		{"a tick without a token", auth.Unauthorized("missing_token", nil), 401, "", "push rejected"},
		{"a tick whose token cannot be checked", auth.Unavailable("jwks_unavailable", errors.New("no Google signing keys loaded")), 503, "5", "push token cannot be checked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuf{}
			pushed := false
			g := newGate("svc-gate", slog.New(slog.NewJSONHandler(logs, nil)), refuse{tc.err},
				func(context.Context) error { pushed = true; return nil }, false, time.Millisecond)
			metrics.DBUp.WithLabelValues("svc-gate").Set(1)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, eventbus.PathTick, strings.NewReader(tickBody)))
			if w.Code != tc.code || w.Header().Get("Retry-After") != tc.retry || w.Body.Len() != 0 {
				t.Errorf("%d %v %q, want %d as the tick answers it", w.Code, w.Header(), w.Body.String(), tc.code)
			}
			if pushed || testutil.ToFloat64(metrics.DBUp.WithLabelValues("svc-gate")) != 1 {
				t.Error("a tick whose token did not pass reported the database or pushed")
			}
			if m := logs.find(tc.logLine); len(m) != 1 || m[0]["path"] != eventbus.PathTick || m[0]["reason"] == "" {
				t.Errorf("%s: %v", tc.logLine, logs.lines())
			}
		})
	}
}

// refuse is a push verifier that refuses every request with err.
type refuse struct{ err error }

func (r refuse) VerifyPush(*http.Request) error { return r.err }
