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

// errHeld is their cause while the start holds on a refused login (hold).
var errHeld = errors.New("starting: the database refused the login; a tick tries again, at most every 5 minutes")

// earlyPushBudget bounds the metrics push of a tick that came before the start was done:
// the tick's own final step has the same 10 s (pubsub.TickOptions.FinallyBudget).
const earlyPushBudget = 10 * time.Second

const (
	// defaultLoginRetryEvery is the least time between two logins while the start holds on
	// a refused one: each failed login feeds Supavisor's circuit breaker for the whole role
	// (design §4), and a tick comes every 15 minutes, so in practice one login a tick.
	defaultLoginRetryEvery = 5 * time.Minute
	// loginRetryWait is how long a tick waits for its login retry before it is answered.
	loginRetryWait = 20 * time.Second
)

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
// While the start holds on a refused login (hold: on Cloud Run, db.LoginRefused), nothing
// waits: every request but the health paths gets that 503 at once, and a tick gets the
// early tick's handling at once. A tick with a valid token is also when the instance has
// CPU (request-based billing), so one such tick at a time, and none sooner than `every`
// after the last login, hands the start a login retry (retries) and waits for it, at most
// loginRetryWait: when the login passes, the start opens the gate and the tick is served
// as any tick; otherwise it is answered as before.
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

	holdOnce sync.Once
	held     chan struct{}   // closed by hold
	retries  chan loginRetry // a due tick's retry, taken by the start (service.holdLogin)
	every    time.Duration   // the least time between two logins
	now      func() time.Time

	mu      sync.Mutex
	lastTry time.Time // the last login: the refused one, then each retry's
	trying  bool      // a retry is in flight
}

// loginRetry is a tick's request for one login retry: the start runs it within the tick's
// context and answers ok (the login passed: the start goes on to open the gate) on result.
type loginRetry struct {
	ctx    context.Context
	result chan bool // buffered: the start never waits for the tick
}

func newGate(service string, log *slog.Logger, verify auth.PushVerifier, push func(context.Context) error, cloud bool, wait time.Duration) *gate {
	g := &gate{service: service, log: log, verify: verify, push: push, wait: wait, done: make(chan struct{}),
		held: make(chan struct{}), retries: make(chan loginRetry), every: defaultLoginRetryEvery, now: time.Now}
	g.ready = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusServiceUnavailable, "starting")
	})
	if cloud {
		g.ready = httpx.CloudReady(log, defaultReadyTimeout, func(context.Context) error {
			if g.isHeld() {
				return errHeld
			}
			return errStarting
		})
	}
	return g
}

// open makes h the handler of every request from now on, the waiting ones included.
func (g *gate) open(h http.Handler) { g.once.Do(func() { g.next = h; close(g.done) }) }

// abort ends the start: the waiting requests and every later one get the 503.
func (g *gate) abort() { g.once.Do(func() { close(g.done) }) }

// hold says the start waits on a refused login, refused at: from now on nothing waits
// for the start, and the first retry is due every after at.
func (g *gate) hold(at time.Time) {
	g.mu.Lock()
	g.lastTry = at
	g.mu.Unlock()
	g.holdOnce.Do(func() { close(g.held) })
}

func (g *gate) isHeld() bool {
	select {
	case <-g.held:
		return true
	default:
		return false
	}
}

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
	case g.isHeld():
		g.whileHeld(w, r)
		return
	}
	began := time.Now()
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case <-g.done:
		g.after(w, r)
		return
	case <-g.held:
		g.whileHeld(w, r)
		return
	case <-timer.C:
	case <-r.Context().Done():
	}
	if isTick(r) {
		if g.tickToken(w, r) {
			g.log.WarnContext(r.Context(), "tick before the start was done", "waited", time.Since(began).Round(time.Millisecond).String())
			g.reportDown(w, r)
		}
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

// whileHeld answers r at once while the start holds on a refused login: the 503, and for
// a tick with a valid token a login retry first when one is due (claimRetry). A tick whose
// retry passed waits for the start to open the gate and is served by the service's tick.
func (g *gate) whileHeld(w http.ResponseWriter, r *http.Request) {
	select {
	case <-g.done: // opened (or aborted) since
		g.after(w, r)
		return
	default:
	}
	if !isTick(r) {
		unavailable(w)
		return
	}
	if !g.tickToken(w, r) {
		return
	}
	if g.claimRetry() && g.retry(r.Context(), loginRetryWait) {
		select {
		case <-g.done:
			g.after(w, r)
			return
		case <-r.Context().Done():
		}
	}
	g.reportDown(w, r)
}

// claimRetry takes the one retry slot when a retry is due: none in flight, and every
// passed since the last login.
func (g *gate) claimRetry() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.trying || now.Sub(g.lastTry) < g.every {
		return false
	}
	g.trying, g.lastTry = true, now
	return true
}

// retry hands the start one login retry within the tick's ctx and at most wait, and
// returns whether the login passed. The start answers every retry it takes (result is
// buffered); one it cannot take any more (the start ended), a tick that hung up or a wait
// that ran out first is a failed retry.
func (g *gate) retry(ctx context.Context, wait time.Duration) bool {
	defer func() {
		g.mu.Lock()
		g.trying = false
		g.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req := loginRetry{ctx: ctx, result: make(chan bool, 1)}
	select {
	case g.retries <- req:
	case <-g.done:
		return false
	case <-ctx.Done():
		return false
	}
	select {
	case ok := <-req.result:
		return ok
	case <-ctx.Done():
		return false
	}
}

func isTick(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == eventbus.PathTick
}

// unavailable is the API's 503 for a request the service cannot take yet.
func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	httpx.WriteError(w, http.StatusServiceUnavailable, "temporarily unavailable, send the request again")
}

// tickToken checks a tick's token as the tick does and answers a refusal (401, or 503
// when it cannot be checked now); true when the token passed.
func (g *gate) tickToken(w http.ResponseWriter, r *http.Request) bool {
	err := g.verify.VerifyPush(r)
	if err == nil {
		return true
	}
	var ae *auth.Error
	if !errors.As(err, &ae) {
		ae = auth.Unauthorized("invalid", err)
	}
	if ae.Status == http.StatusUnauthorized {
		g.log.InfoContext(r.Context(), "push rejected", "path", eventbus.PathTick, "reason", ae.Reason, "err", errText(ae.Err))
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	g.log.WarnContext(r.Context(), "push token cannot be checked", "path", eventbus.PathTick, "reason", ae.Reason, "err", errText(ae.Err))
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(ae.Status)
	return false
}

// reportDown answers a tick the start did not get to: the database is what the start
// waits for, so the tick reports it down and pushes that, then asks to be sent again.
func (g *gate) reportDown(w http.ResponseWriter, r *http.Request) {
	metrics.DBUp.WithLabelValues(g.service).Set(0)
	metrics.Ticks.WithLabelValues(g.service, "error").Inc()
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
