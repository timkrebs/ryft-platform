package app

import (
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// The per-address limit of api and ingest (D53, which Traefik's middleware kept on EKS; on Cloud Run
// nothing in front does it, D240): 20 requests a second per client address, bursts of 50, before any
// token is checked, so a flood without a token costs no verification and no database call. Per instance,
// like every limit here; the per-user limits (D36) take over behind auth.
const (
	ipPerSecond = 20
	ipBurst     = 50
	// ipMaxKeys is how many addresses an instance keeps a bucket for (ratelimit.Capped): one IPv6 /48
	// holds 65,536 /64s, and at the cap the map is about 6 MiB. Newcomers beyond it share one bucket.
	ipMaxKeys = 65536
	// ipIdle is when an address is forgotten: its bucket is full again 2.5 s after its last request,
	// so dropping it after a minute loses nothing and keeps the map to the addresses of the last minute.
	ipIdle = time.Minute
)

// The hook class (D257): a provider's pushes to ingest's POST /connect/{provider}/push come from the provider's few
// addresses and in bursts (a day's records at once), so they get a bucket of their own and take nothing from the
// ip class: 50 a second, bursts of 200, per instance. The edge (infra/gcp, Cloud Armor rule 1900) throttles the
// same traffic first, at 6,000 per 60 s per address, above one instance's ceiling here (200 + 50*60 = 3,200).
const (
	hookPerSecond = 50
	hookBurst     = 200
	hookMaxKeys   = 4096
	hookIdle      = time.Minute
	hookPrefix    = "/connect/"
)

const (
	// ipClass is ryft_rate_limited_total's class of the edge limit's refusals.
	ipClass = "ip"
	// hookClass is the class of the hook's refusals.
	hookClass = "hook"
	hookCode  = "hook_limit"
	// ipCode is the access line's code of a refusal.
	ipCode = "ip_limit"
	// tooManyRequests is the body of the edge's 429, as docs/API.md (Errors → 429) has it: Traefik's.
	tooManyRequests = "Too Many Requests"
)

// ipLimit is the edge limit: the client's key (httpx.ClientKeys, an HMAC of its address, never the
// address) into a capped token bucket. It logs nothing (D222): a refusal shows only as
// ryft_rate_limited_total{class="ip"} and the access line's code ip_limit.
type ipLimit struct {
	clients *httpx.ClientKeys
	lim     *ratelimit.Capped
	hook    *ratelimit.Capped        // POST /connect/ on an Edge service
	exempt  func(*http.Request) bool // the service's own (Spec.LimitExempt); nil: none
}

func newIPLimit(clients *httpx.ClientKeys, exempt func(*http.Request) bool, now func() time.Time) *ipLimit {
	// The series exists at 0 from the start, as the admin series do: increase() over a counter born at 1
	// sees no increase, so an instance's first refusal would otherwise be invisible.
	metrics.RateLimited.WithLabelValues(ipClass)
	metrics.RateLimited.WithLabelValues(hookClass)
	return &ipLimit{clients: clients, exempt: exempt,
		lim:  ratelimit.NewCapped(ipPerSecond, ipBurst, ipMaxKeys, ratelimit.Idle(ipIdle), ratelimit.Clock(now)),
		hook: ratelimit.NewCapped(hookPerSecond, hookBurst, hookMaxKeys, ratelimit.Idle(hookIdle), ratelimit.Clock(now))}
}

// wrap limits next. What limitExempt or the service's hook passes takes no token.
func (l *ipLimit) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limitExempt(r) || (l.exempt != nil && l.exempt(r)) {
			next.ServeHTTP(w, r)
			return
		}
		lim, class, code := l.lim, ipClass, ipCode
		if isHook(r) {
			lim, class, code = l.hook, hookClass, hookCode
		}
		if ok, wait := lim.Allow(l.clients.Key(r)); !ok {
			metrics.RateLimited.WithLabelValues(class).Inc()
			httpx.SetCode(r, code)
			writeTooMany(w, wait)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitExempt is what the edge limit never counts, whatever the service: the health paths (Cloud Run's
// probes reach the container without X-Forwarded-For, so they would share the fallback bucket; the uptime
// checks; compose's /healthz and /readyz) and the tick, Pub/Sub's push of ryft-tick from Google's push
// servers, the only push api and ingest take (TestEdgeLimitExemptsEveryPushOfApiAndIngest); its token is
// the tick's to check. Neither must fail because someone else floods the same front end.
func limitExempt(r *http.Request) bool {
	return httpx.IsLive(r) || httpx.IsReady(r) || isTick(r) || isEdgePush(r)
}

// edgePushPaths are the push routes api and ingest serve besides the tick (ingest's /push/inbox.received): Pub/Sub's
// push servers deliver them, and the push token is checked behind the limit like the tick's.
var edgePushPaths = eventbus.EdgePushPaths()

func isEdgePush(r *http.Request) bool {
	return r.Method == http.MethodPost && slices.Contains(edgePushPaths, r.URL.Path)
}

// isHook says whether r is a provider's push to a hook (POST /connect/...): the hook class's.
func isHook(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, hookPrefix)
}

// writeTooMany is the edge's 429 as Traefik answered it and docs/API.md documents it: the plain-text body
// Too Many Requests (no trailing newline, no JSON), Retry-After the wait rounded up to whole seconds (at
// least 1), X-Retry-In the wait as a Go duration (37.5ms). No CORS headers: the operator's page reads none
// of this. The security headers and X-Request-ID are the outer layers'.
func writeTooMany(w http.ResponseWriter, wait time.Duration) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	h.Set("X-Retry-In", wait.String())
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, tooManyRequests)
}
