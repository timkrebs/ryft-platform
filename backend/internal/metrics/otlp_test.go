package metrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// testToken is built at run time: no literal that looks like a credential.
func testToken() string { return "glc_" + strings.Repeat("q9", 24) }

// lockedBuffer is a log sink the pushes of several goroutines may share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// received is what the test gateway saw of one push.
type received struct {
	method, path, contentType, encoding string
	user, pass                          string
	basic                               bool
	body                                map[string]any
	raw                                 string
}

// gateway is an OTLP endpoint over TLS that records each push and answers with answer.
type gateway struct {
	srv    *httptest.Server
	mu     sync.Mutex
	pushes []received
	answer func(w http.ResponseWriter, r *http.Request)
}

func newGateway(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *gateway {
	t.Helper()
	g := &gateway{answer: answer}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := received{method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
			encoding: r.Header.Get("Content-Encoding")}
		rec.user, rec.pass, rec.basic = r.BasicAuth()
		zr, err := gzip.NewReader(r.Body)
		if err == nil {
			raw, _ := io.ReadAll(zr)
			rec.raw = string(raw)
			_ = json.Unmarshal(raw, &rec.body)
		}
		g.mu.Lock()
		g.pushes = append(g.pushes, rec)
		g.mu.Unlock()
		if g.answer != nil {
			g.answer(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateway) seen() []received {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]received(nil), g.pushes...)
}

// pusher is an OTLP pusher of reg to g, as api with instance api-1.
func pusher(t *testing.T, g *gateway, reg prometheus.Gatherer, log *slog.Logger) *OTLP {
	t.Helper()
	p, err := NewOTLP(OTLPOptions{Endpoint: g.srv.URL + "/otlp", User: "1234567", Token: testToken(), Service: "api",
		InstanceID: "api-1", Gatherer: reg, Client: g.srv.Client(), Log: log})
	if err != nil || p == nil {
		t.Fatalf("NewOTLP = %v, %v", p, err)
	}
	return p
}

func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// smallRegistry has one series of each common kind, with values whose sums are exact.
func smallRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	ticks := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ryft_ticks_total", Help: "Ticks."}, []string{"service", "result"})
	ticks.WithLabelValues("api", "ok").Add(3)
	ticks.WithLabelValues("api", "").Inc() // an empty label value is left out, as Prometheus does
	up := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ryft_db_up", Help: "Up."}, []string{"service"})
	up.WithLabelValues("api").Set(1)
	lat := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "ryft_x_seconds", Help: "X.", Buckets: []float64{0.5, 1}})
	for _, v := range []float64{0.25, 0.5, 2} {
		lat.Observe(v)
	}
	empty := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ryft_never_total", Help: "No series."}, []string{"x"})
	reg.MustRegister(ticks, up, lat, empty)
	return reg
}

// TestOTLPEncodingGolden pins the JSON of a counter, a gauge and a histogram byte for
// byte: proto3 JSON (64-bit integers as strings, temporality 2 = cumulative), names as
// the registry has them (one _total, no unit), per-bucket counts from the cumulative
// ones with the bounds lacking +Inf, no scope, a family without series left out.
func TestOTLPEncodingGolden(t *testing.T) {
	p := pusher(t, newGateway(t, nil), smallRegistry(), quiet())
	p.start, p.now = time.Unix(1763190000, 0), func() time.Time { return time.Unix(1763200000, 0) }
	mfs, err := p.o.Gatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	gz, err := encode(p.request(mfs))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	const (
		start = `"startTimeUnixNano":"1763190000000000000"`
		now   = `"timeUnixNano":"1763200000000000000"`
		svc   = `{"key":"service","value":{"stringValue":"api"}}`
	)
	want := `{"resourceMetrics":[{"resource":{"attributes":[` +
		`{"key":"service.namespace","value":{"stringValue":"ryft"}},` +
		`{"key":"service.name","value":{"stringValue":"api"}},` +
		`{"key":"service.instance.id","value":{"stringValue":"api-1"}}]},` +
		`"scopeMetrics":[{"metrics":[` +
		`{"name":"ryft_db_up","description":"Up.","gauge":{"dataPoints":[{"attributes":[` + svc + `],` + now + `,"asDouble":1}]}},` +
		`{"name":"ryft_ticks_total","description":"Ticks.","sum":{"dataPoints":[` +
		`{"attributes":[` + svc + `],` + start + `,` + now + `,"asDouble":1},` +
		`{"attributes":[{"key":"result","value":{"stringValue":"ok"}},` + svc + `],` + start + `,` + now + `,"asDouble":3}],` +
		`"aggregationTemporality":2,"isMonotonic":true}},` +
		`{"name":"ryft_x_seconds","description":"X.","histogram":{"dataPoints":[{` + start + `,` + now +
		`,"count":"3","sum":2.75,"bucketCounts":["2","0","1"],"explicitBounds":[0.5,1]}],"aggregationTemporality":2}}` +
		`]}]}]}`
	if s := strings.TrimSpace(string(got)); s != want {
		t.Fatalf("encoding\n got %s\nwant %s", s, want)
	}
}

// oddCollector emits what the registry's own metrics never do: a summary without
// observations (NaN quantiles), an untyped value, a gauge at +Inf, a histogram that
// lists +Inf itself, a sample with its own timestamp.
type oddCollector struct{}

func (oddCollector) Describe(chan<- *prometheus.Desc) {} // unchecked

func (oddCollector) Collect(ch chan<- prometheus.Metric) {
	d := func(name string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(name, name+".", labels, nil)
	}
	ch <- prometheus.MustNewConstSummary(d("odd_summary_seconds", "q"), 0, 0, map[float64]float64{0.5: math.NaN(), 0.9: math.NaN()}, "a")
	ch <- prometheus.MustNewConstSummary(d("odd_summary_seconds", "q"), 4, 10, map[float64]float64{0.5: 2, 0.9: 4}, "b")
	ch <- prometheus.MustNewConstMetric(d("odd_untyped"), prometheus.UntypedValue, 7)
	ch <- prometheus.MustNewConstMetric(d("odd_gauge"), prometheus.GaugeValue, math.Inf(1))
	ch <- prometheus.MustNewConstHistogram(d("odd_hist"), 5, 1.5, map[float64]uint64{1: 2, math.Inf(1): 5})
	ch <- prometheus.NewMetricWithTimestamp(time.Unix(1700000000, 5e6),
		prometheus.MustNewConstMetric(d("odd_stamped_total"), prometheus.CounterValue, 2))
}

// metricsOf is the body's metrics by name.
func metricsOf(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	rms, _ := body["resourceMetrics"].([]any)
	if len(rms) != 1 {
		t.Fatalf("resourceMetrics: %v", body)
	}
	sms, _ := rms[0].(map[string]any)["scopeMetrics"].([]any)
	if len(sms) != 1 {
		t.Fatalf("scopeMetrics: %v", rms[0])
	}
	if _, ok := sms[0].(map[string]any)["scope"]; ok {
		t.Errorf("a scope was sent: %v", sms[0])
	}
	ms, _ := sms[0].(map[string]any)["metrics"].([]any)
	for _, m := range ms {
		mm := m.(map[string]any)
		out[mm["name"].(string)] = mm
	}
	return out
}

func points(t *testing.T, m map[string]any, kind string) []map[string]any {
	t.Helper()
	k, ok := m[kind].(map[string]any)
	if !ok {
		t.Fatalf("%s is not a %s: %v", m["name"], kind, m)
	}
	var out []map[string]any
	for _, p := range k["dataPoints"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestOTLPEncodesEveryKind(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(oddCollector{})
	g := newGateway(t, nil)
	if err := pusher(t, g, reg, quiet()).Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms := metricsOf(t, g.seen()[0].body)

	sum := points(t, ms["odd_summary_seconds"], "summary")
	if len(sum) != 2 {
		t.Fatalf("summary points: %v", sum)
	}
	if sum[0]["count"] != "0" || sum[0]["sum"] != 0.0 || sum[0]["startTimeUnixNano"] == nil {
		t.Errorf("an empty summary: %v", sum[0])
	}
	for _, q := range sum[0]["quantileValues"].([]any) {
		if q.(map[string]any)["value"] != "NaN" {
			t.Errorf("a quantile without observations must be \"NaN\": %v", q)
		}
	}
	qs := sum[1]["quantileValues"].([]any)
	if sum[1]["count"] != "4" || sum[1]["sum"] != 10.0 || len(qs) != 2 ||
		qs[0].(map[string]any)["quantile"] != 0.5 || qs[0].(map[string]any)["value"] != 2.0 {
		t.Errorf("a summary: %v", sum[1])
	}

	if u := points(t, ms["odd_untyped"], "gauge"); len(u) != 1 || u[0]["asDouble"] != 7.0 || u[0]["startTimeUnixNano"] != nil {
		t.Errorf("untyped is a gauge: %v", u)
	}
	if gg := points(t, ms["odd_gauge"], "gauge"); gg[0]["asDouble"] != "Infinity" {
		t.Errorf("+Inf: %v", gg)
	}
	h := points(t, ms["odd_hist"], "histogram")[0]
	if b, _ := json.Marshal([]any{h["explicitBounds"], h["bucketCounts"], h["count"]}); string(b) != `[[1],["2","3"],"5"]` {
		t.Errorf("a histogram listing +Inf: %s", b)
	}
	st := points(t, ms["odd_stamped_total"], "sum")[0]
	if st["timeUnixNano"] != "1700000000005000000" {
		t.Errorf("a sample's own timestamp: %v", st)
	}
}

func TestOTLPPushRequest(t *testing.T) {
	g := newGateway(t, nil)
	var logs lockedBuffer
	p := pusher(t, g, smallRegistry(), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err := p.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := g.seen()
	if len(seen) != 1 {
		t.Fatalf("%d pushes", len(seen))
	}
	r := seen[0]
	if r.method != http.MethodPost || r.path != "/otlp/v1/metrics" || r.contentType != "application/json" || r.encoding != "gzip" {
		t.Errorf("request: %s %s, %q, %q", r.method, r.path, r.contentType, r.encoding)
	}
	if !r.basic || r.user != "1234567" || r.pass != testToken() {
		t.Errorf("basic auth: %v, user %q, password of %d chars", r.basic, r.user, len(r.pass))
	}
	if strings.Contains(r.raw, testToken()) {
		t.Error("the token is in the body")
	}
	res := r.body["resourceMetrics"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	attrs := map[string]string{}
	for _, a := range res["attributes"].([]any) {
		kv := a.(map[string]any)
		attrs[kv["key"].(string)] = kv["value"].(map[string]any)["stringValue"].(string)
	}
	if attrs["service.namespace"] != "ryft" || attrs["service.name"] != "api" || attrs["service.instance.id"] != "api-1" || len(attrs) != 3 {
		t.Errorf("resource: %v", attrs)
	}
	ms := metricsOf(t, r.body)
	if _, ok := ms["ryft_ticks_total"]; !ok {
		t.Errorf("no ryft_ticks_total: %v", ms)
	}
	if _, ok := ms["ryft_never_total"]; ok {
		t.Error("a family without series was sent")
	}
	for name, m := range ms {
		if _, ok := m["unit"]; ok {
			t.Errorf("%s has a unit", name)
		}
	}
	if logs.String() != "" {
		t.Errorf("a push that worked logged: %s", logs.String())
	}
}

func TestOTLPInstanceIDIsRandomPerPusher(t *testing.T) {
	ids := map[string]bool{}
	for range 2 {
		p, err := NewOTLP(OTLPOptions{Endpoint: "https://otlp.example/otlp", User: "1", Token: testToken(), Service: "notify",
			Gatherer: prometheus.NewRegistry(), Log: quiet()})
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(p.o.InstanceID) {
			t.Errorf("instance id %q", p.o.InstanceID)
		}
		ids[p.o.InstanceID] = true
		if p.url != "https://otlp.example/otlp/v1/metrics" || p.o.Client == nil {
			t.Errorf("url %q, client %v", p.url, p.o.Client)
		}
	}
	if len(ids) != 2 {
		t.Error("two pushers drew the same instance id")
	}
}

func TestOTLPRefusedPush(t *testing.T) {
	tok := testToken()
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, "invalid credentials for " + tok + "\nsecond line"},
		{http.StatusBadRequest, strings.Repeat("é", 300)},
		{http.StatusTooManyRequests, ""},
		{http.StatusServiceUnavailable, tok},
	} {
		var n atomic.Int32
		g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		})
		var logs lockedBuffer
		err := pusher(t, g, smallRegistry(), slog.New(slog.NewJSONHandler(&logs, nil))).Push(context.Background())
		switch {
		case err == nil:
			t.Fatalf("%d: no error", c.status)
		case !strings.HasPrefix(err.Error(), "otlp push: HTTP "+strconv.Itoa(c.status)):
			t.Errorf("%d: %v", c.status, err)
		case strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "second line"):
			t.Errorf("%d: the error carries the token or more than a line: %v", c.status, err)
		case len(err.Error()) > len("otlp push: HTTP 401: ")+maxCut:
			t.Errorf("%d: %d bytes", c.status, len(err.Error()))
		}
		if n.Load() != 1 {
			t.Errorf("%d: %d requests, want one (no retry)", c.status, n.Load())
		}
		if logs.String() != "" {
			t.Errorf("%d: Push logged what it returns: %s", c.status, logs.String())
		}
	}
}

func TestOTLPFollowsNoRedirect(t *testing.T) {
	var n atomic.Int32
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	err := pusher(t, g, smallRegistry(), quiet()).Push(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "otlp push: HTTP 307") {
		t.Fatalf("Push = %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("%d requests: the redirect was followed", n.Load())
	}
}

func TestOTLPRedactsTheBasicAuthValue(t *testing.T) {
	p := &OTLP{o: OTLPOptions{User: "1234567", Token: testToken()}}
	basic := httptest.NewRequest(http.MethodGet, "/", nil)
	basic.SetBasicAuth("1234567", testToken())
	echoed := "you sent " + basic.Header.Get("Authorization")
	if got := p.cut([]byte(echoed)); got != ": you sent Basic [token]" {
		t.Errorf("cut = %q", got)
	}
	if got := p.cut([]byte("  \n")); got != "" {
		t.Errorf("an empty answer: %q", got)
	}
	if got := p.cut([]byte("\n  rate limited\nmore")); got != ": rate limited" {
		t.Errorf("a leading newline: %q", got)
	}
}

func TestOTLPPartialSuccessIsLoggedOnce(t *testing.T) {
	for _, body := range []string{
		`{"partialSuccess":{"rejectedDataPoints":"3","errorMessage":"err-mimir-sample-out-of-order"}}`,
		`{"partialSuccess":{"rejectedDataPoints":3,"errorMessage":"err-mimir-sample-out-of-order"}}`,
	} {
		g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		var logs lockedBuffer
		if err := pusher(t, g, smallRegistry(), slog.New(slog.NewJSONHandler(&logs, nil))).Push(context.Background()); err != nil {
			t.Fatalf("a partial success is a success: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
		var rec map[string]any
		if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &rec) != nil {
			t.Fatalf("logs: %q", logs.String())
		}
		if rec["msg"] != "metrics push: points rejected" || rec["level"] != "WARN" || rec["rejected"] != 3.0 ||
			rec["err"] != "err-mimir-sample-out-of-order" {
			t.Errorf("line: %v", rec)
		}
	}
	// An empty partial success, an empty answer and one that is not JSON log nothing.
	for _, body := range []string{`{"partialSuccess":{}}`, ``, `ok`} {
		g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		var logs lockedBuffer
		if err := pusher(t, g, smallRegistry(), slog.New(slog.NewJSONHandler(&logs, nil))).Push(context.Background()); err != nil || logs.String() != "" {
			t.Errorf("%q: %v, %s", body, err, logs.String())
		}
	}
}

func TestOTLPHonoursContext(t *testing.T) {
	t.Run("ended before", func(t *testing.T) {
		g := newGateway(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := pusher(t, g, smallRegistry(), quiet()).Push(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Push = %v", err)
		}
		if len(g.seen()) != 0 {
			t.Error("a request after the context ended")
		}
	})
	t.Run("ends during the request", func(t *testing.T) {
		release := make(chan struct{})
		g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		t.Cleanup(func() { close(release) }) // before the server's Close (cleanups run last first)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		began := time.Now()
		err := pusher(t, g, smallRegistry(), quiet()).Push(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Push = %v", err)
		}
		if took := time.Since(began); took > 5*time.Second {
			t.Errorf("Push returned after %s", took)
		}
	})
	t.Run("ends while another push runs", func(t *testing.T) {
		entered, release := make(chan struct{}, 1), make(chan struct{})
		g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
			entered <- struct{}{}
			<-release
		})
		p := pusher(t, g, smallRegistry(), quiet())
		first := make(chan error, 1)
		go func() { first <- p.Push(context.Background()) }()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := p.Push(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the waiting push = %v", err)
		}
		close(release)
		if err := <-first; err != nil {
			t.Errorf("the first push = %v", err)
		}
		if n := len(g.seen()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})
}

func TestOTLPOnePushAtATime(t *testing.T) {
	var in, most atomic.Int32
	g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		n := in.Add(1)
		for {
			if m := most.Load(); n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		in.Add(-1)
	})
	p := pusher(t, g, smallRegistry(), quiet())
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Push(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 1 || len(g.seen()) != 3 {
		t.Errorf("at most %d at once, %d pushes", most.Load(), len(g.seen()))
	}
}

// dupCollector makes Gather fail: one series twice.
type dupCollector struct{}

func (dupCollector) Describe(chan<- *prometheus.Desc) {}

func (dupCollector) Collect(ch chan<- prometheus.Metric) {
	d := prometheus.NewDesc("dup_total", "Twice.", nil, nil)
	ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, 1)
	ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, 2)
}

func TestOTLPPushesWhatGatherHadOnAnError(t *testing.T) {
	reg := smallRegistry()
	reg.MustRegister(dupCollector{})
	g := newGateway(t, nil)
	err := pusher(t, g, reg, quiet()).Push(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gather") {
		t.Fatalf("Push = %v", err)
	}
	if seen := g.seen(); len(seen) != 1 || metricsOf(t, seen[0].body)["ryft_ticks_total"] == nil {
		t.Errorf("the rest was not pushed: %v", seen)
	}
}

func TestNewOTLP(t *testing.T) {
	p, err := NewOTLP(OTLPOptions{Token: testToken()})
	if p != nil || err != nil {
		t.Fatalf("no endpoint: %v, %v", p, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Push(ctx); err != nil {
		t.Errorf("a nil pusher: %v", err)
	}

	ok := OTLPOptions{Endpoint: "https://otlp.example/otlp", User: "1", Token: testToken(), Service: "api",
		Gatherer: prometheus.NewRegistry(), Log: quiet()}
	for name, change := range map[string]func(o *OTLPOptions){
		"http":        func(o *OTLPOptions) { o.Endpoint = "http://otlp.example/otlp" },
		"not a URL":   func(o *OTLPOptions) { o.Endpoint = "https://otlp example/otlp" },
		"no host":     func(o *OTLPOptions) { o.Endpoint = "https:///otlp" },
		"userinfo":    func(o *OTLPOptions) { o.Endpoint = "https://1:" + testToken() + "@otlp.example/otlp" },
		"a query":     func(o *OTLPOptions) { o.Endpoint = "https://otlp.example/otlp?x=1" },
		"a fragment":  func(o *OTLPOptions) { o.Endpoint = "https://otlp.example/otlp#x" },
		"no user":     func(o *OTLPOptions) { o.User = "" },
		"no token":    func(o *OTLPOptions) { o.Token = "" },
		"no service":  func(o *OTLPOptions) { o.Service = "" },
		"no gatherer": func(o *OTLPOptions) { o.Gatherer = nil },
		"no logger":   func(o *OTLPOptions) { o.Log = nil },
	} {
		o := ok
		change(&o)
		p, err := NewOTLP(o)
		if err == nil || p != nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), testToken()) {
			t.Errorf("%s: the error carries the token", name)
		}
	}
}
