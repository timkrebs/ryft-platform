package app

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/events/pubsub"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/metrics"
)

// fakeClock is the edge limit's clock (seams.limitClock): it moves only when told, so a refill is
// a step of the test, not a sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// edgeOf is the Edge chain listen builds (service.edge) in front of next, with hops trusted
// X-Forwarded-For entries, the service's exemption hook and the clock.
func edgeOf(log *slog.Logger, hops int, exempt func(*http.Request) bool, clk *fakeClock, next http.Handler) http.Handler {
	r := &service{s: Spec{Name: "svc", Edge: true, LimitExempt: exempt}, e: seams{limitClock: clk.now}, log: log,
		clients: httpx.NewClientKeys(hops)}
	return r.edge(next)
}

// served answers 204: what got past the limit.
var served = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

var quietLog = slog.New(slog.DiscardHandler)

// call sends one request through h; hdr are header pairs, added in order (a name twice is two lines).
func call(h http.Handler, method, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil) // RemoteAddr 192.0.2.1:1234, which the limit never reads
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// from is a GET /v1/ping with one X-Forwarded-For line.
func from(h http.Handler, xff string) int {
	return call(h, http.MethodGet, "/v1/ping", "X-Forwarded-For", xff).Code
}

// drain spends key's burst (the 50 it may send at once) and fails unless all of it is served.
func drain(t *testing.T, h http.Handler, hdr ...string) {
	t.Helper()
	for i := range ipBurst {
		if w := call(h, http.MethodGet, "/v1/ping", hdr...); w.Code != http.StatusNoContent {
			t.Fatalf("request %d of the burst (%v): %d, want it served", i+1, hdr, w.Code)
		}
	}
}

func ipLimited() float64 { return testutil.ToFloat64(metrics.RateLimited.WithLabelValues(ipClass)) }

// The edge limit's values are D53's and D240's: a change here is a decision and an API.md entry, not a
// tweak. ipIdle must stay at least the refill time (burst/rate, 2.5 s), or a sweep would hand a flooder a
// full bucket again; ipMaxKeys bounds the memory (about 6 MiB at the cap).
func TestEdgeLimitValuesArePinned(t *testing.T) {
	if ipPerSecond != 20 || ipBurst != 50 || ipMaxKeys != 65536 || ipIdle != time.Minute {
		t.Fatalf("edge limit %v/s, burst %v, %d keys, idle %v: want 20/s, 50, 65536, 1m (D53, D240)", ipPerSecond, ipBurst, ipMaxKeys, ipIdle)
	}
	if refill := time.Duration(float64(ipBurst) / float64(ipPerSecond) * float64(time.Second)); ipIdle < refill {
		t.Fatalf("idle %v is shorter than the refill %v: a sweep would reset a flooder's bucket", ipIdle, refill)
	}
}

// One address gets bursts of 50: the 51st request is 429, while another address (and another /64)
// is not affected, and every 429 is counted under class ip. Addresses in one /64 are one client.
func TestEdgeLimitIsPerAddress(t *testing.T) {
	h := edgeOf(quietLog, 1, nil, newFakeClock(), served)
	before := ipLimited()

	drain(t, h, "X-Forwarded-For", "203.0.113.7")
	if c := from(h, "203.0.113.7"); c != http.StatusTooManyRequests {
		t.Fatalf("the 51st request of a burst: %d, want 429", c)
	}
	if c := from(h, "203.0.113.8"); c != http.StatusNoContent {
		t.Errorf("another address after the first was refused: %d, want it served", c)
	}

	drain(t, h, "X-Forwarded-For", "2001:db8:1:2::1")
	for _, a := range []string{"2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff", "::ffff:203.0.113.7"} {
		if c := from(h, a); c != http.StatusTooManyRequests {
			t.Errorf("%s, a drained client's: %d, want 429", a, c)
		}
	}
	if c := from(h, "2001:db8:1:3::1"); c != http.StatusNoContent {
		t.Errorf("the next /64: %d, want its own bucket", c)
	}
	if got := ipLimited() - before; got != 4 {
		t.Errorf("ryft_rate_limited_total{class=ip} rose by %v, want 4 (the 429s)", got)
	}
}

// The bucket refills at 20 a second: one token every 50 ms, the whole burst after 2.5 s. The clock is
// the test's: nothing sleeps.
func TestEdgeLimitRefills(t *testing.T) {
	clk := newFakeClock()
	h := edgeOf(quietLog, 1, nil, clk, served)
	const a = "198.51.100.20"
	drain(t, h, "X-Forwarded-For", a)
	if c := from(h, a); c != http.StatusTooManyRequests {
		t.Fatalf("after the burst: %d", c)
	}
	clk.add(49 * time.Millisecond)
	if c := from(h, a); c != http.StatusTooManyRequests {
		t.Errorf("49 ms later (0.98 of a token): %d, want 429", c)
	}
	clk.add(50 * time.Millisecond)
	if c := from(h, a); c != http.StatusNoContent {
		t.Errorf("another 50 ms later (one token): %d, want it served", c)
	}
	if c := from(h, a); c != http.StatusTooManyRequests {
		t.Errorf("right after the refilled token: %d, want 429", c)
	}
	clk.add(3 * time.Second)
	drain(t, h, "X-Forwarded-For", a)
	if c := from(h, a); c != http.StatusTooManyRequests {
		t.Errorf("after the refilled burst: %d, want 429", c)
	}
}

// The health paths, the tick (POST /internal/tick, exactly) and what the service's own hook passes are
// never limited and take no token, with the bucket empty or full, for an address and for the fallback.
func TestEdgeLimitExemptions(t *testing.T) {
	hook := func(r *http.Request) bool {
		return r.URL.Path == "/v1/admin/flags" && r.Header.Get("Authorization") == "Bearer k"
	}
	exempt := []struct{ method, path string }{
		{http.MethodGet, httpx.PathLive}, {http.MethodHead, httpx.PathLive}, {http.MethodGet, httpx.PathHealthz},
		{http.MethodGet, httpx.PathReady}, {http.MethodHead, httpx.PathReady}, {http.MethodGet, httpx.PathReadyz},
		{http.MethodPost, "/internal/tick"},
	}
	limited := []struct{ method, path string }{
		{http.MethodGet, "/internal/tick"}, {http.MethodPost, "/internal/tick/"}, {http.MethodPost, "/internal/ticks"},
		{http.MethodPost, "/push/ingested"}, {http.MethodPost, httpx.PathLive}, {http.MethodGet, "/health"},
		{http.MethodGet, "/v1/admin/flags"}, // the hook's path without its bearer
	}
	for _, who := range []struct {
		name string
		hdr  []string
	}{
		{"an address", []string{"X-Forwarded-For", "203.0.113.30"}},
		{"the fallback", nil},
	} {
		t.Run(who.name, func(t *testing.T) {
			h := edgeOf(quietLog, 1, hook, newFakeClock(), served)
			// Full bucket: a hundred exempt requests take none of its 50 tokens.
			for range 100 / len(exempt) {
				for _, e := range exempt {
					call(h, e.method, e.path, who.hdr...)
				}
				call(h, http.MethodGet, "/v1/admin/flags", append([]string{"Authorization", "Bearer k"}, who.hdr...)...)
			}
			drain(t, h, who.hdr...)
			// Empty bucket: the exempt requests still pass, the others do not.
			for _, e := range exempt {
				if w := call(h, e.method, e.path, who.hdr...); w.Code != http.StatusNoContent {
					t.Errorf("%s %s with the bucket empty: %d, want it served", e.method, e.path, w.Code)
				}
			}
			if w := call(h, http.MethodGet, "/v1/admin/flags", append([]string{"Authorization", "Bearer k"}, who.hdr...)...); w.Code != http.StatusNoContent {
				t.Errorf("the service's hook with the bucket empty: %d, want it served", w.Code)
			}
			for _, l := range limited {
				if w := call(h, l.method, l.path, who.hdr...); w.Code != http.StatusTooManyRequests {
					t.Errorf("%s %s with the bucket empty: %d, want 429", l.method, l.path, w.Code)
				}
			}
		})
	}
	// Without a hook (ingest) only the common exemptions apply.
	h := edgeOf(quietLog, 1, nil, newFakeClock(), served)
	drain(t, h)
	if w := call(h, http.MethodGet, "/v1/admin/flags", "Authorization", "Bearer k"); w.Code != http.StatusTooManyRequests {
		t.Errorf("no hook: %d, want 429", w.Code)
	}
	if w := call(h, http.MethodPost, "/internal/tick"); w.Code != http.StatusNoContent {
		t.Errorf("no hook, the tick: %d, want it served", w.Code)
	}
}

// Every request whose client cannot be told shares one bucket (it fails closed): no header, an empty one,
// an entry that is no address, fewer entries than the hops. Clients that can be told are not affected.
func TestEdgeLimitFallbackIsShared(t *testing.T) {
	h := edgeOf(quietLog, 1, nil, newFakeClock(), served)
	unknown := [][]string{
		nil,
		{"X-Forwarded-For", ""},
		{"X-Forwarded-For", " , "},
		{"X-Forwarded-For", "not-an-address"},
		{"X-Forwarded-For", "203.0.113.40, garbage"},
		{"X-Forwarded-For", "203.0.113.40:443"},
	}
	for i := range ipBurst {
		if w := call(h, http.MethodGet, "/v1/ping", unknown[i%len(unknown)]...); w.Code != http.StatusNoContent {
			t.Fatalf("request %d of the shared burst: %d", i+1, w.Code)
		}
	}
	for _, hdr := range unknown {
		if w := call(h, http.MethodGet, "/v1/ping", hdr...); w.Code != http.StatusTooManyRequests {
			t.Errorf("%q after the shared burst: %d, want 429", hdr, w.Code)
		}
	}
	if c := from(h, "203.0.113.40"); c != http.StatusNoContent {
		t.Errorf("a told client after the fallback was drained: %d, want its own bucket", c)
	}

	// Behind the load balancer (hops 2) one entry is too few: the fallback as well.
	lb := edgeOf(quietLog, 2, nil, newFakeClock(), served)
	drain(t, lb)
	if c := from(lb, "203.0.113.41"); c != http.StatusTooManyRequests {
		t.Errorf("hops 2 with one entry: %d, want the drained fallback", c)
	}
	if c := from(lb, "203.0.113.41, 198.51.100.200"); c != http.StatusNoContent {
		t.Errorf("hops 2 with two entries: %d, want the client's own bucket", c)
	}
}

// What the client writes left of the entry Google appended is ignored: a forged entry, another forged
// line, or Google's own front end appended after it gives no new bucket. Behind the load balancer the
// client is the second entry from the right, whichever LB address added the last.
func TestEdgeLimitIgnoresForgedEntries(t *testing.T) {
	const client = "203.0.113.50"
	h := edgeOf(quietLog, 1, nil, newFakeClock(), served)
	drain(t, h, "X-Forwarded-For", client)
	for _, hdr := range [][]string{
		{"X-Forwarded-For", "198.51.100.1, " + client},
		{"X-Forwarded-For", "10.0.0.1, 198.51.100.2, " + client},
		{"X-Forwarded-For", "2001:db8::1, , " + client},
		{"X-Forwarded-For", "198.51.100.3", "X-Forwarded-For", client},
		{"X-Forwarded-For", client + ", 35.191.0.1"},
		{"X-Forwarded-For", "198.51.100.4, " + client + ", 130.211.0.9"},
	} {
		if w := call(h, http.MethodGet, "/v1/ping", hdr...); w.Code != http.StatusTooManyRequests {
			t.Errorf("%q from a drained client: %d, want 429", hdr, w.Code)
		}
	}

	lb := edgeOf(quietLog, 2, nil, newFakeClock(), served)
	drain(t, lb, "X-Forwarded-For", "198.51.100.9, "+client+", 34.110.0.1")
	for _, xff := range []string{client + ", 34.110.0.2", "10.0.0.1, " + client + ", 34.110.0.3"} {
		if c := from(lb, xff); c != http.StatusTooManyRequests {
			t.Errorf("hops 2, %q from a drained client: %d, want 429", xff, c)
		}
	}
	if c := from(lb, "198.51.100.9, 34.110.0.1"); c != http.StatusNoContent {
		t.Errorf("hops 2, the forged entry as the client: %d, want its own bucket", c)
	}
}

// The edge's 429 is Traefik's, byte for byte (docs/API.md, Errors → 429): the plain-text body without a
// newline, its content type, Retry-After in whole seconds (at least 1) and X-Retry-In as a Go duration,
// no CORS headers even for a page's Origin. It is inside Instrument and SecurityHeaders, so it carries
// the request id and the security headers.
func TestEdgeLimitAnswer(t *testing.T) {
	h := edgeOf(quietLog, 1, nil, newFakeClock(), served)
	const a = "198.51.100.60"
	drain(t, h, "X-Forwarded-For", a)
	w := call(h, http.MethodGet, "/v1/admin/flags", "X-Forwarded-For", a, "Origin", "https://ryftlab.de", "Authorization", "Bearer x")
	if w.Code != http.StatusTooManyRequests || w.Body.String() != "Too Many Requests" {
		t.Fatalf("answer %d %q", w.Code, w.Body.String())
	}
	hdr := w.Header()
	for k, want := range map[string]string{
		"Content-Type": "text/plain; charset=utf-8", "Retry-After": "1", "X-Retry-In": "50ms",
		"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'", "Referrer-Policy": "no-referrer",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := hdr.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if _, err := uuid.Parse(hdr.Get(httpx.RequestIDHeader)); err != nil {
		t.Errorf("X-Request-ID %q: %v", hdr.Get(httpx.RequestIDHeader), err)
	}
	for k := range hdr {
		if strings.HasPrefix(k, "Access-Control-") || k == "Vary" {
			t.Errorf("the edge's 429 carries %s", k)
		}
	}

	for _, c := range []struct {
		wait              time.Duration
		retryAfter, retry string
	}{
		{0, "1", "0s"},
		{time.Millisecond, "1", "1ms"},
		{37500 * time.Microsecond, "1", "37.5ms"},
		{time.Second, "1", "1s"},
		{1200 * time.Millisecond, "2", "1.2s"},
		{2500 * time.Millisecond, "3", "2.5s"},
	} {
		w := httptest.NewRecorder()
		writeTooMany(w, c.wait)
		if w.Code != 429 || w.Body.String() != tooManyRequests || w.Header().Get("Retry-After") != c.retryAfter ||
			w.Header().Get("X-Retry-In") != c.retry {
			t.Errorf("wait %v: %d %q, Retry-After %q, X-Retry-In %q; want Retry-After %q, X-Retry-In %q", c.wait, w.Code, w.Body.String(),
				w.Header().Get("Retry-After"), w.Header().Get("X-Retry-In"), c.retryAfter, c.retry)
		}
	}
}

// docs/API.md (Errors → 429) quotes the edge's body and its values: the doc and the code change together.
func TestAPIDocQuotesTheEdgeLimit(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, want := range []string{"`" + tooManyRequests + "`", "20/s, bursts of 50", "`X-Retry-In`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/API.md does not say %s, which the edge limit answers", want)
		}
	}
}

// Every push that Pub/Sub sends to api or ingest is exempt from the edge limit: Google's push servers
// share a few addresses, so a limited push path would be refused once traffic grows, and the event would
// wait in the retry backoff. Today that is the tick alone; a new push path for either service must be
// added to limitExempt (with its token checked behind it) before this passes.
func TestEdgeLimitExemptsEveryPushOfApiAndIngest(t *testing.T) {
	n := 0
	for _, p := range pubsub.Spec().Push {
		if p.Service != pubsub.ServiceAPI && p.Service != pubsub.ServiceIngest {
			continue
		}
		n++
		if !limitExempt(httptest.NewRequest(http.MethodPost, p.Path, nil)) {
			t.Errorf("push subscription %s posts to %s's %s, which the edge limit counts: exempt it in limitExempt", p.Name, p.Service, p.Path)
		}
	}
	if n == 0 {
		t.Fatal("no push subscription for api or ingest: the guard checks nothing")
	}
}

// fetch is a GET over the service's port with X-Forwarded-For lines; the body untrimmed.
func (g *rig) fetch(t *testing.T, path string, xff ...string) (int, http.Header, string) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+g.addr("http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// Through a running Edge service: the limit sits outside the start gate (a flood during the start gets
// 429 at once instead of waiting for it) and inside Instrument (its 429s are access lines with code
// ip_limit). It logs nothing else, and no line holds an address or its network (D222): the 429s show
// only in ryft_rate_limited_total{class="ip"}, which rises by their number.
func TestEdgeLimitLogsNoAddress(t *testing.T) {
	g := newRig()
	g.e.logger = func(string) *slog.Logger { return logx.NewWriter(g.logs, "api", slog.LevelDebug) }
	g.poolGate = make(chan struct{})
	g.e.startWait = 10 * time.Millisecond
	g.e.limitClock = newFakeClock().now
	g.parts.Routes = pingRoute
	s := g.spec()
	s.Edge = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := ipLimited()
	const v4, v4net = "203.0.113.77", "203.0.113."
	const v6, v6net = "2001:db8:77:88::5", "2001:db8:77:88"
	const forged = "198.51.100.66"

	done := g.waitForPort(t, ctx, s)
	refused := 0
	// During the start every request waits for the gate and gets its 503; the 51st never gets there.
	for i := range ipBurst {
		if code, _, _ := g.fetch(t, "/v1/ping", forged+", "+v4); code != http.StatusServiceUnavailable {
			t.Fatalf("request %d during the start: %d, want the gate's 503", i+1, code)
		}
	}
	code, hdr, body := g.fetch(t, "/v1/ping", v4)
	if code != http.StatusTooManyRequests || body != tooManyRequests || hdr.Get("Content-Length") != "17" ||
		hdr.Get(httpx.RequestIDHeader) == "" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the 51st during the start: %d %q %v, want the edge's 429 before the gate", code, body, hdr)
	}
	refused++
	close(g.poolGate)
	<-g.started

	for i := range ipBurst {
		if code, _, body := g.fetch(t, "/v1/ping", forged, v6); code != http.StatusOK || body != "pong" {
			t.Fatalf("request %d after the start: %d %q", i+1, code, body)
		}
	}
	for _, xff := range [][]string{{v6}, {"2001:db8:77:88:1:2:3:4"}, {v4}, {forged + ", " + v4}} {
		if code, _, _ := g.fetch(t, "/v1/ping", xff...); code != http.StatusTooManyRequests {
			t.Errorf("%v after its burst: %d, want 429", xff, code)
		}
		refused++
	}
	for _, path := range []string{httpx.PathLive, httpx.PathReady} {
		if code, _, _ := g.fetch(t, path, v4); code != http.StatusOK {
			t.Errorf("%s from a drained client: %d, want 200", path, code)
		}
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}

	if got := ipLimited() - before; got != float64(refused) {
		t.Errorf("ryft_rate_limited_total{class=ip} rose by %v, want %d (the 429s)", got, refused)
	}
	lines := maps.Clone(lifecycleLogKeys)
	lines["http"] = logcheck.HTTP(ipCode)
	lines["client address"] = logcheck.Keys{"picked": logcheck.Enum("xff", "fallback"), "xff_index": logcheck.CountMax(64),
		"xff_entries": logcheck.CountMax(64)}
	forbid := logcheck.Forbidden{Texts: []string{v4, v4net, v6, v6net, "2001:db8:77:88:", forged, "198.51.100.", "X-Forwarded-For"}}
	out, problems := logcheck.Check(g.logs.text(), logcheck.Standard("api"), lines, forbid)
	for _, p := range problems {
		t.Error(p)
	}
	n := 0
	for _, l := range out["http"] {
		if l["status"] == float64(http.StatusTooManyRequests) {
			n++
			if l["code"] != ipCode {
				t.Errorf("a 429's access line: %v, want code %s", l, ipCode)
			}
		} else if l["code"] != nil {
			t.Errorf("an access line with a code that is not the limit's: %v", l)
		}
	}
	if n != refused {
		t.Errorf("%d access lines of a 429, want %d", n, refused)
	}
	if len(out["client address"]) != 1 {
		t.Errorf("client address lines: %v, want the one sample of the minute", out["client address"])
	}
}
