package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The health paths. Cloud Run cannot route some paths that end in z (Google answers its
// own 404 for /healthz), so its probes, the synthetics and the runbook use /health/live
// and /health/ready; /healthz and /readyz stay for compose (design §3). Each pair has the
// same handler.
const (
	PathLive    = "/health/live"
	PathReady   = "/health/ready"
	PathHealthz = "/healthz"
	PathReadyz  = "/readyz"
)

// ReadyTimeout is how long a ready path waits for its checks.
const ReadyTimeout = 2 * time.Second

// ReadyCacheFor is how long a Cloud Run service reuses a ready answer, ok or not: every
// check costs a round trip to the pooler, and the path is public on api and ingest.
const ReadyCacheFor = 5 * time.Second

// NotReady is the body of every ready path's 503 on Cloud Run: the cause is in the log,
// never in the answer (a public service must not show its dependencies' errors).
const NotReady = "not ready"

// IsLive and IsReady say whether r asks a live or a ready path (GET, or the HEAD a
// ServeMux GET pattern also answers).
func IsLive(r *http.Request) bool { return probe(r, PathLive, PathHealthz) }

// IsReady is IsLive for the ready paths.
func IsReady(r *http.Request) bool { return probe(r, PathReady, PathReadyz) }

func probe(r *http.Request, paths ...string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	for _, p := range paths {
		if r.URL.Path == p {
			return true
		}
	}
	return false
}

// Live answers a live path: 200 {"status":"ok"} while the process runs.
func Live(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Health mounts the probes: GET /healthz and /health/live (the process runs) and GET
// /readyz and /health/ready (the dependencies answer). None requires auth. Off Cloud Run
// a ready path answers 200 {"status":"ready"} or 503 {"error": <the cause>} as it always
// did (API.md → Health); on Cloud Run it is CloudReady's.
func Health(mux *http.ServeMux, ready func(context.Context) error) {
	mux.HandleFunc("GET "+PathHealthz, Live)
	mux.HandleFunc("GET "+PathLive, Live)
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if CloudRunService() {
		h = CloudReady(slog.Default(), ReadyTimeout, ready)
	}
	mux.Handle("GET "+PathReadyz, h)
	mux.Handle("GET "+PathReady, h)
}

// CloudReady is a ready path on Cloud Run: 200 {"status":"ready"}, or 503
// {"error":"not ready"} without Retry-After (a probe's answer, not a client's) and the
// cause logged at warn as "not ready". The result, ok or not, is reused for
// ReadyCacheFor, so the checks run at most once per 5 s however often the path is asked
// (and the warning is logged at most as often). The checks run on the request's values
// but not its cancel, within timeout: a probe that hangs up does not leave a cancelled
// check cached as the service's state.
func CloudReady(log *slog.Logger, timeout time.Duration, check func(context.Context) error) http.Handler {
	c := &readyCache{log: log, timeout: timeout, check: check, now: time.Now}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.result(r.Context()); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": NotReady})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

// readyCache holds the last ready answer. A request that finds it stale runs the checks
// while holding the lock, so concurrent probes wait for that one run (at most timeout)
// instead of each asking the database.
type readyCache struct {
	log     *slog.Logger
	timeout time.Duration
	check   func(context.Context) error
	now     func() time.Time

	mu  sync.Mutex
	at  time.Time // when err was found; zero: never
	err error
}

func (c *readyCache) result(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && c.now().Sub(c.at) < ReadyCacheFor {
		return c.err
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	c.err, c.at = c.check(cctx), c.now()
	if c.err != nil {
		c.log.Warn("not ready", "err", oneLine(c.err.Error(), maxCause))
	}
	return c.err
}

// maxCause is the longest cause the "not ready" line carries.
const maxCause = 200

// oneLine is s on one line, cut to n bytes (at a rune boundary).
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
