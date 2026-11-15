package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// errStarting is the ready paths' cause while the start is not done.
var errStarting = errors.New("starting: the database or the service's build is not done")

// earlyPushBudget bounds the metrics push of a tick that came before the start was done:
// the tick's own final step has the same 10 s (pubsub.TickOptions.FinallyBudget).
const earlyPushBudget = 10 * time.Second

// gate is the service's handler from the moment it listens, which is before the pool
// opens: Cloud Run's startup probe is /health/live (1 s × 20), and an instance must start
// while the database is down, so that its ticks report ryft_db_up 0 instead of there
// being no instance and no signal at all (deploy/run/*.yaml). Until open:
//
//   - GET /healthz, /health/live: 200 {"status":"ok"} at once;
//   - GET /readyz, /health/ready: 503 (on Cloud Run {"error":"not ready"}, the cause
//     logged; else {"error":"starting"});
//   - any other request waits for the start, at most wait and its own context; then it
//     is served, or, the start still not done (or failed), answered 503 with
//     Retry-After: 5 in the API's error shape. A tick that waited in vain has its token
//     checked as the tick checks it, sets ryft_db_up{service} to 0, counts as a failed
//     tick, runs the metrics push and is answered the same 503, so Pub/Sub sends it
//     again with its backoff.
//
// open hands every request after it to the service's handler; abort (a start that failed
// or was interrupted) answers the waiting ones at once.
type gate struct {
	service string
	log     *slog.Logger
	verify  auth.PushVerifier           // the tick's token check (the transport's)
	push    func(context.Context) error // the metrics push; nil: none
	ready   http.Handler                // a ready path's answer before open
	wait    time.Duration

	once sync.Once
	done chan struct{} // closed by open or abort
	next http.Handler  // set before done closes; nil after abort
}

func newGate(service string, log *slog.Logger, verify auth.PushVerifier, push func(context.Context) error, cloud bool, wait time.Duration) *gate {
	g := &gate{service: service, log: log, verify: verify, push: push, wait: wait, done: make(chan struct{})}
	g.ready = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusServiceUnavailable, "starting")
	})
	if cloud {
		g.ready = httpx.CloudReady(log, defaultReadyTimeout, func(context.Context) error { return errStarting })
	}
	return g
}

// open makes h the handler of every request from now on, the waiting ones included.
func (g *gate) open(h http.Handler) { g.once.Do(func() { g.next = h; close(g.done) }) }

// abort ends the start: the waiting requests and every later one get the 503.
func (g *gate) abort() { g.once.Do(func() { close(g.done) }) }

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case <-g.done:
		g.after(w, r)
		return
	default:
	}
	switch {
	case httpx.IsLive(r):
		httpx.Live(w, r)
		return
	case httpx.IsReady(r):
		g.ready.ServeHTTP(w, r)
		return
	}
	began := time.Now()
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case <-g.done:
		g.after(w, r)
		return
	case <-timer.C:
	case <-r.Context().Done():
	}
	if r.Method == http.MethodPost && r.URL.Path == eventbus.PathTick {
		g.earlyTick(w, r, time.Since(began))
		return
	}
	unavailable(w)
}

// after serves r once the start is over: the service's handler, or the 503 after abort.
func (g *gate) after(w http.ResponseWriter, r *http.Request) {
	if g.next == nil {
		unavailable(w)
		return
	}
	g.next.ServeHTTP(w, r)
}

// unavailable is the API's 503 for a request the service cannot take yet.
func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	httpx.WriteError(w, http.StatusServiceUnavailable, "temporarily unavailable, send the request again")
}

// earlyTick is a tick the start did not get to in time: the database is what the start
// waits for, so the tick reports it down and pushes that, then asks to be sent again.
func (g *gate) earlyTick(w http.ResponseWriter, r *http.Request, waited time.Duration) {
	if err := g.verify.VerifyPush(r); err != nil {
		var ae *auth.Error
		if !errors.As(err, &ae) {
			ae = auth.Unauthorized("invalid", err)
		}
		if ae.Status == http.StatusUnauthorized {
			g.log.InfoContext(r.Context(), "push rejected", "path", eventbus.PathTick, "reason", ae.Reason, "err", errText(ae.Err))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.log.WarnContext(r.Context(), "push token cannot be checked", "path", eventbus.PathTick, "reason", ae.Reason, "err", errText(ae.Err))
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(ae.Status)
		return
	}
	metrics.DBUp.WithLabelValues(g.service).Set(0)
	metrics.Ticks.WithLabelValues(g.service, "error").Inc()
	g.log.WarnContext(r.Context(), "tick before the start was done", "waited", waited.Round(time.Millisecond).String())
	if g.push != nil {
		// Past the caller's cancel (a push that is cut is no answer for Pub/Sub either), not
		// past the shutdown's hard stop.
		ctx, cancel := httpx.Detach(r.Context(), earlyPushBudget)
		if err := g.push(ctx); err != nil {
			g.log.ErrorContext(ctx, "tick: the final step failed", "err", err)
		}
		cancel()
	}
	unavailable(w)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
