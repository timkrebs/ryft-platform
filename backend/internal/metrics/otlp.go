package metrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// OTLPOptions configure the push of the registry's metrics to an OTLP/HTTP endpoint (D224).
type OTLPOptions struct {
	Endpoint   string // OTLP_ENDPOINT: the gateway's base URL; "" means no push (NewOTLP returns nil)
	User       string // OTLP_USER: the instance ID (basic auth user)
	Token      string // OTLP_TOKEN: the write-only token (basic auth password)
	Service    string // service.name: api | ingest | scoring | notify
	InstanceID string // service.instance.id; "" generates a random one per process
	Gatherer   prometheus.Gatherer
	Client     *http.Client // nil: a client with a 10 s timeout; redirects are never followed
	Log        *slog.Logger
}

// OTLP pushes the registry once per call: one OTLP/HTTP JSON request, gzipped, with
// every series the Gatherer has (the package doc has the encoding and why). A nil
// *OTLP pushes nothing.
type OTLP struct {
	o     OTLPOptions
	url   string           // <Endpoint>/v1/metrics
	start time.Time        // startTimeUnixNano of every cumulative point
	now   func() time.Time // the points' timeUnixNano (a test hook)
	turn  chan struct{}    // one push at a time: a series' samples leave in time order
}

// processStart stands for the process start: every series of the registry counts from
// zero since the package was initialised (promauto registers them then).
var processStart = time.Now()

// pushTimeout is the default client's timeout; the caller's ctx is meant to be shorter.
const pushTimeout = 10 * time.Second

// maxAnswer is how much of the gateway's answer is read; maxCut how much of it an
// error carries.
const (
	maxAnswer = 4 << 10
	maxCut    = 200
)

// NewOTLP returns nil, nil when Endpoint is empty. Otherwise the endpoint must be an
// https URL without userinfo, query or fragment (config.LoadOTLP is stricter: Grafana's
// gateway only), and User, Token, Service, Gatherer and Log must be set.
func NewOTLP(o OTLPOptions) (*OTLP, error) {
	if o.Endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(o.Endpoint)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "":
		return nil, errors.New("metrics: the OTLP endpoint must be an https URL")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, errors.New("metrics: the OTLP endpoint may hold no userinfo, query or fragment")
	case o.User == "" || o.Token == "":
		return nil, errors.New("metrics: the OTLP push needs its user and token")
	case o.Service == "" || o.Gatherer == nil || o.Log == nil:
		return nil, errors.New("metrics: the OTLP push needs its service, a gatherer and a logger")
	}
	if o.InstanceID == "" {
		o.InstanceID = randomID()
	}
	c := http.Client{Timeout: pushTimeout}
	if o.Client != nil {
		c = *o.Client // a copy: the caller's client keeps its own redirect policy
	}
	c.CheckRedirect = noRedirect
	o.Client = &c
	return &OTLP{o: o, url: u.JoinPath("v1", "metrics").String(), start: processStart, now: time.Now,
		turn: make(chan struct{}, 1)}, nil
}

// noRedirect answers a redirect as it is (a failed push) instead of following it: the
// basic auth goes to the configured endpoint and nowhere else.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// randomID is a service.instance.id for this process: 16 hex digits.
func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails: crypto/rand.Read crashes the program instead (Go 1.24)
	return hex.EncodeToString(b)
}

// Push sends one snapshot of every series. It makes one request, never retries (the
// next tick pushes the cumulative values again), and returns when ctx ends. Its error
// carries the HTTP status and at most 200 bytes of the answer, never the token, for
// the caller to log once (the tick's final step, the shutdown); Push itself logs only
// an accepted push whose answer says points were rejected (OTLP's partial success).
// A push waits for one still running in this process, within ctx. nil receiver: no-op.
func (p *OTLP) Push(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("otlp push: %w", err)
	}
	select {
	case p.turn <- struct{}{}:
		defer func() { <-p.turn }()
	case <-ctx.Done():
		return fmt.Errorf("otlp push: waiting for the push before it: %w", ctx.Err())
	}
	mfs, gerr := p.o.Gatherer.Gather()
	if gerr != nil && len(mfs) == 0 {
		return fmt.Errorf("otlp push: gather: %w", gerr)
	}
	body, err := encode(p.request(mfs))
	if err != nil {
		return fmt.Errorf("otlp push: %w", err)
	}
	if err := p.post(ctx, body); err != nil {
		return err
	}
	if gerr != nil {
		return fmt.Errorf("otlp push: gather (the rest was pushed): %w", gerr)
	}
	return nil
}

// post sends the gzipped request and reads the answer.
func (p *OTLP) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body)) // p.url: the configured endpoint (config.LoadOTLP: Grafana's gateway only)
	if err != nil {
		return fmt.Errorf("otlp push: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.SetBasicAuth(p.o.User, p.o.Token)
	resp, err := p.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("otlp push: %w", err) // a *url.Error: the URL holds no credential
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("otlp push: HTTP %d%s", resp.StatusCode, p.cut(raw))
	}
	p.partial(ctx, raw)
	return nil
}

// partial logs an accepted push whose answer reports rejected points or a warning.
func (p *OTLP) partial(ctx context.Context, raw []byte) {
	var a struct {
		PartialSuccess struct {
			RejectedDataPoints json.Number `json:"rejectedDataPoints"` // int64: a string in proto3 JSON, a number accepted
			ErrorMessage       string      `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return // empty, or not JSON: nothing to report
	}
	n, _ := a.PartialSuccess.RejectedDataPoints.Int64()
	if n > 0 || a.PartialSuccess.ErrorMessage != "" {
		p.o.Log.WarnContext(ctx, "metrics push: points rejected", "rejected", n, "err", p.firstLine([]byte(a.PartialSuccess.ErrorMessage)))
	}
}

// cut is ": " and firstLine(raw) for an error; "" for an empty answer.
func (p *OTLP) cut(raw []byte) string {
	if s := p.firstLine(raw); s != "" {
		return ": " + s
	}
	return ""
}

// firstLine is the answer's first line with the token and the basic auth value
// replaced, at most maxCut bytes of valid UTF-8.
func (p *OTLP) firstLine(raw []byte) string {
	s := strings.ReplaceAll(string(raw), p.o.Token, "[token]")
	s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(p.o.User+":"+p.o.Token)), "[token]")
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxCut {
		s = s[:maxCut]
	}
	return strings.TrimSpace(strings.ToValidUTF8(s, ""))
}

// encode is the request as gzipped JSON.
func encode(r exportRequest) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// request is the OTLP ExportMetricsServiceRequest for mfs: one resource (this service's
// instance), one scope without a name, one metric per family that has series.
func (p *OTLP) request(mfs []*dto.MetricFamily) exportRequest {
	ts := span{start: unixNano(p.start), now: unixNano(p.now())}
	ms := make([]metric, 0, len(mfs))
	for _, mf := range mfs {
		if m, ok := ts.convert(mf); ok {
			ms = append(ms, m)
		}
	}
	return exportRequest{ResourceMetrics: []resourceMetrics{{
		Resource: resource{Attributes: []keyValue{
			attr("service.namespace", "ryft"),
			attr("service.name", p.o.Service),
			attr("service.instance.id", p.o.InstanceID),
		}},
		ScopeMetrics: []scopeMetrics{{Metrics: ms}},
	}}}
}

func unixNano(t time.Time) fixed64 { return fixed64(max(t.UnixNano(), 0)) }

// span is a push's two times: the cumulative points' start and every point's time.
type span struct{ start, now fixed64 }

// convert is one family as an OTLP metric; false for a family without series or of a
// type OTLP has no cumulative form for (a gauge histogram; the registry has none).
func (ts span) convert(mf *dto.MetricFamily) (metric, bool) {
	m := metric{Name: mf.GetName(), Description: mf.GetHelp()}
	xs := mf.GetMetric()
	if len(xs) == 0 {
		return m, false
	}
	switch mf.GetType() {
	case dto.MetricType_COUNTER:
		s := &sum{AggregationTemporality: cumulative, IsMonotonic: true, DataPoints: make([]numberPoint, 0, len(xs))}
		for _, x := range xs {
			s.DataPoints = append(s.DataPoints, numberPoint{Attributes: labels(x), StartTimeUnixNano: ts.start,
				TimeUnixNano: ts.at(x), AsDouble: double(x.GetCounter().GetValue())})
		}
		m.Sum = s
	case dto.MetricType_GAUGE, dto.MetricType_UNTYPED:
		g := &gauge{DataPoints: make([]numberPoint, 0, len(xs))}
		for _, x := range xs {
			v := x.GetGauge().GetValue()
			if mf.GetType() == dto.MetricType_UNTYPED {
				v = x.GetUntyped().GetValue()
			}
			g.DataPoints = append(g.DataPoints, numberPoint{Attributes: labels(x), TimeUnixNano: ts.at(x), AsDouble: double(v)})
		}
		m.Gauge = g
	case dto.MetricType_HISTOGRAM:
		h := &histogram{AggregationTemporality: cumulative, DataPoints: make([]histogramPoint, 0, len(xs))}
		for _, x := range xs {
			h.DataPoints = append(h.DataPoints, ts.histogramPoint(x))
		}
		m.Histogram = h
	case dto.MetricType_SUMMARY:
		s := &summary{DataPoints: make([]summaryPoint, 0, len(xs))}
		for _, x := range xs {
			q := x.GetSummary()
			pt := summaryPoint{Attributes: labels(x), StartTimeUnixNano: ts.start, TimeUnixNano: ts.at(x),
				Count: fixed64(q.GetSampleCount()), Sum: double(q.GetSampleSum())}
			for _, v := range q.GetQuantile() {
				pt.QuantileValues = append(pt.QuantileValues, quantileValue{Quantile: double(v.GetQuantile()), Value: double(v.GetValue())})
			}
			s.DataPoints = append(s.DataPoints, pt)
		}
		m.Summary = s
	default:
		return m, false
	}
	return m, true
}

// at is the point's time: the sample's own timestamp when it has one, else the push's.
func (ts span) at(x *dto.Metric) fixed64 {
	if x.TimestampMs != nil {
		return fixed64(max(x.GetTimestampMs(), 0) * int64(time.Millisecond))
	}
	return ts.now
}

// histogramPoint turns Prometheus' cumulative buckets into OTLP's per-bucket counts:
// explicitBounds are the finite upper bounds, and bucketCounts has one count more, the
// overflow bucket (+Inf), which is the total less the last finite bucket.
func (ts span) histogramPoint(x *dto.Metric) histogramPoint {
	h := x.GetHistogram()
	pt := histogramPoint{Attributes: labels(x), StartTimeUnixNano: ts.start, TimeUnixNano: ts.at(x),
		Count: fixed64(h.GetSampleCount()), Sum: double(h.GetSampleSum()),
		ExplicitBounds: make([]double, 0, len(h.GetBucket())), BucketCounts: make([]fixed64, 0, len(h.GetBucket())+1)}
	var below uint64 // the cumulative count so far
	for _, b := range h.GetBucket() {
		if math.IsInf(b.GetUpperBound(), +1) {
			break // a collector that lists +Inf itself: it is the overflow bucket below
		}
		c := b.GetCumulativeCount()
		pt.ExplicitBounds = append(pt.ExplicitBounds, double(b.GetUpperBound()))
		pt.BucketCounts = append(pt.BucketCounts, fixed64(less(c, below)))
		below = max(below, c)
	}
	pt.BucketCounts = append(pt.BucketCounts, fixed64(less(h.GetSampleCount(), below)))
	return pt
}

// less is a - b, or 0 when b is larger (a collector whose counts are not cumulative).
func less(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// labels are the point's attributes: the series' labels, those with an empty value left
// out (Prometheus drops them too).
func labels(x *dto.Metric) []keyValue {
	var kv []keyValue
	for _, l := range x.GetLabel() {
		if l.GetValue() != "" {
			kv = append(kv, attr(l.GetName(), l.GetValue()))
		}
	}
	return kv
}

func attr(k, v string) keyValue { return keyValue{Key: k, Value: anyValue{StringValue: v}} }

// The OTLP/JSON shapes (opentelemetry-proto, metrics/v1 and collector/metrics/v1) in the
// proto3 JSON mapping: lowerCamelCase keys, 64-bit integers as decimal strings (fixed64),
// enums as numbers, NaN and the infinities as strings (double), default values left out.
type (
	exportRequest struct {
		ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
	}
	resourceMetrics struct {
		Resource     resource       `json:"resource"`
		ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
	}
	resource struct {
		Attributes []keyValue `json:"attributes"`
	}
	// scopeMetrics has no scope: a scope name or version could become labels
	// (otel_scope_name) on every series.
	scopeMetrics struct {
		Metrics []metric `json:"metrics"`
	}
	// metric has no unit, on purpose (package doc: names).
	metric struct {
		Name        string     `json:"name"`
		Description string     `json:"description,omitempty"`
		Gauge       *gauge     `json:"gauge,omitempty"`
		Sum         *sum       `json:"sum,omitempty"`
		Histogram   *histogram `json:"histogram,omitempty"`
		Summary     *summary   `json:"summary,omitempty"`
	}
	gauge struct {
		DataPoints []numberPoint `json:"dataPoints"`
	}
	sum struct {
		DataPoints             []numberPoint `json:"dataPoints"`
		AggregationTemporality int           `json:"aggregationTemporality"`
		IsMonotonic            bool          `json:"isMonotonic"`
	}
	histogram struct {
		DataPoints             []histogramPoint `json:"dataPoints"`
		AggregationTemporality int              `json:"aggregationTemporality"`
	}
	summary struct {
		DataPoints []summaryPoint `json:"dataPoints"`
	}
	numberPoint struct {
		Attributes        []keyValue `json:"attributes,omitempty"`
		StartTimeUnixNano fixed64    `json:"startTimeUnixNano,omitempty"` // cumulative sums only
		TimeUnixNano      fixed64    `json:"timeUnixNano"`
		AsDouble          double     `json:"asDouble"`
	}
	histogramPoint struct {
		Attributes        []keyValue `json:"attributes,omitempty"`
		StartTimeUnixNano fixed64    `json:"startTimeUnixNano"`
		TimeUnixNano      fixed64    `json:"timeUnixNano"`
		Count             fixed64    `json:"count"`
		Sum               double     `json:"sum"`
		BucketCounts      []fixed64  `json:"bucketCounts"`
		ExplicitBounds    []double   `json:"explicitBounds"`
	}
	summaryPoint struct {
		Attributes        []keyValue      `json:"attributes,omitempty"`
		StartTimeUnixNano fixed64         `json:"startTimeUnixNano"`
		TimeUnixNano      fixed64         `json:"timeUnixNano"`
		Count             fixed64         `json:"count"`
		Sum               double          `json:"sum"`
		QuantileValues    []quantileValue `json:"quantileValues,omitempty"`
	}
	quantileValue struct {
		Quantile double `json:"quantile"`
		Value    double `json:"value"`
	}
	keyValue struct {
		Key   string   `json:"key"`
		Value anyValue `json:"value"`
	}
	anyValue struct {
		StringValue string `json:"stringValue"`
	}
)

// cumulative is AGGREGATION_TEMPORALITY_CUMULATIVE: Prometheus counts since the start.
const cumulative = 2

// fixed64 is a uint64 in proto3 JSON: a decimal string.
type fixed64 uint64

// MarshalJSON writes n as a quoted decimal.
func (n fixed64) MarshalJSON() ([]byte, error) {
	b := append(make([]byte, 0, 22), '"')
	b = strconv.AppendUint(b, uint64(n), 10)
	return append(b, '"'), nil
}

// double is a float64 in proto3 JSON: a number, or "NaN", "Infinity", "-Infinity"
// (encoding/json refuses those three).
type double float64

// MarshalJSON writes d as a number, or as the string proto3 JSON has for it.
func (d double) MarshalJSON() ([]byte, error) {
	f := float64(d)
	switch {
	case math.IsNaN(f):
		return []byte(`"NaN"`), nil
	case math.IsInf(f, +1):
		return []byte(`"Infinity"`), nil
	case math.IsInf(f, -1):
		return []byte(`"-Infinity"`), nil
	}
	return json.Marshal(f)
}
