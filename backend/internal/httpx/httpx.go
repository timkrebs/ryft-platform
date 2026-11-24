package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// Request body limits: settings and small commands, a whole Train document
// (a session or a plan), a training import (5 plans, 50 sessions), and a
// HealthKit upload (a first backfill page can be large).
const (
	MaxBodyDefault = 64 << 10
	MaxBodyTrain   = 256 << 10
	MaxBodyImport  = 1 << 20
	MaxBodySync    = 32 << 20
)

// RequestIDHeader carries the request ID on every response (and is logged).
const RequestIDHeader = "X-Request-ID"

// WriteJSON writes v as the JSON body of status. An encoding failure comes after the header
// is sent, so there is nothing left to answer with and the error is dropped.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the API's error shape {"error": msg, "request_id": id};
// the request ID lets a user report a failure we can find in the logs.
func WriteError(w http.ResponseWriter, status int, msg string) {
	body := map[string]string{"error": msg}
	if id := w.Header().Get(RequestIDHeader); id != "" {
		body["request_id"] = id
	}
	WriteJSON(w, status, body)
}

// WriteCodedError is WriteError with the body's code, which tells the client
// what to do (model.Code*).
func WriteCodedError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	SetCode(r, code)
	body := map[string]string{"error": msg, "code": code}
	if id := w.Header().Get(RequestIDHeader); id != "" {
		body["request_id"] = id
	}
	WriteJSON(w, status, body)
}

// SetCode records the answer's error code (or auth reason) for the access line.
func SetCode(r *http.Request, code string) {
	if ri := Info(r.Context()); ri != nil {
		ri.Code = code
	}
}

// GateHeader carries, in warn mode, the code(s) a gate would have refused the
// request with (API.md → Errors).
const GateHeader = "X-RYFT-Gate"

// StatusClientClosed is logged (and counted) when the client gave up before
// the answer: nginx's 499. The client never sees it.
const StatusClientClosed = 499

// Fail answers an unexpected server-side error. The client gets a generic
// message and the request ID; the details go to the log. A request that ran
// out of time is a 503 (retry); one the client abandoned is no server error
// and is logged at info as 499; text the database refused (a NUL that got past
// ReadJSON and WithRoutes) is the client's: a 422, counted, not logged; anything
// else a 500.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	if refusedText(err) {
		countRefused(r, "database")
		WriteError(w, http.StatusUnprocessableEntity, msgDBText)
		return
	}
	status, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded):
		status, msg = http.StatusServiceUnavailable, "request timed out"
		w.Header().Set("Retry-After", "5")
	case errors.Is(r.Context().Err(), context.Canceled):
		slog.Default().InfoContext(r.Context(), "client closed the request", "err", err)
		WriteError(w, StatusClientClosed, "client closed request")
		return
	}
	slog.Default().ErrorContext(r.Context(), "request failed", "status", status, "err", err)
	WriteError(w, status, msg)
}

// ReadJSON decodes the request body into v, rejecting unknown fields, bodies
// over limit and, once the JSON decoded, a "\u0000" in any of its strings
// (ErrNUL: Postgres cannot store it); json.RawMessage fields come out
// re-encoded (raw.go). Answer its error with WriteDecodeError.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return readJSON(w, r, v, limit, false)
}

// ReadSingleJSON enforces a single JSON value and checks the full body size.
func ReadSingleJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return readJSON(w, r, v, limit, true)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64, single bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	scan := newNULScanner(r.Body)
	dec := json.NewDecoder(scan)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if single {
		var extra json.RawMessage
		if err := dec.Decode(&extra); err == nil {
			return errors.New("expected a single JSON value")
		} else if !errors.Is(err, io.EOF) {
			return err
		}
	}
	// Within the value only: the decoder may have read past its end, and
	// whatever follows it is ignored.
	if scan.nul >= 0 && scan.nul < dec.InputOffset() {
		countRefused(r, "body")
		return ErrNUL
	}
	return normalizeRaw(v)
}

// ReadJSONStripNUL is ReadJSON for a body whose strings the client cannot
// correct (POST /v1/sync: HealthKit's source names, the device's name): it
// removes every "\u0000" from them, keys and values, and decodes the rest
// instead of refusing it, so one NUL cannot stall an upload stream.
func ReadJSONStripNUL(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	strip := &nulStripper{r: r.Body}
	dec := json.NewDecoder(strip)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if strip.removed > 0 {
		metrics.NULStripped.WithLabelValues(routeOf(r)).Inc()
	}
	return normalizeRaw(v)
}

// WriteDecodeError answers a ReadJSON failure: 413 for an oversized body,
// 422 for a NUL in a string or a raw JSON field that cannot be stored, 400
// otherwise (raw 0x00 bytes included: not JSON).
func WriteDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	var raw *RawJSONError
	switch {
	case errors.As(err, &tooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body over %d bytes", tooLarge.Limit))
	case errors.Is(err, ErrNUL):
		WriteError(w, http.StatusUnprocessableEntity, ErrNUL.Error())
	case errors.As(err, &raw):
		WriteError(w, http.StatusUnprocessableEntity, raw.Error())
	default:
		WriteError(w, http.StatusBadRequest, "bad json: "+err.Error())
	}
}

// NotFound answers unknown routes (and unknown methods on known paths) with
// the API's JSON error shape instead of ServeMux's plain-text 404/405. Mount it
// on "/" and on the authenticated "/v1/" mux.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, "not found")
}

// RequestInfo is what inner layers learn about a request (route, user), for
// logs and metrics; see logx.Fields.
type RequestInfo = logx.Fields

// Info returns the request's RequestInfo, or nil outside Instrument.
func Info(ctx context.Context) *RequestInfo { return logx.FieldsFrom(ctx) }

// WithRoutes serves mux and records the pattern that matched in RequestInfo
// before the handler runs (ServeMux sets r.Pattern only on the request copy it
// was given, which outer middleware never sees). A path or query that is not
// text Postgres can store (a NUL, %00, or bytes that are not UTF-8) is a 400
// before any handler sees it; behind auth, so a missing token stays a 401.
func WithRoutes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ri := Info(r.Context()); ri != nil {
			if _, pattern := mux.Handler(r); pattern != "" {
				ri.Route = pattern
			}
		}
		if !urlIsText(r.URL) {
			countRefused(r, "url")
			WriteError(w, http.StatusBadRequest, msgURLText)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (s *statusWriter) WriteHeader(c int) {
	if !s.wrote {
		s.status, s.wrote = c, true
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// clientRequestID accepts a client-sent X-Request-ID only if it looks like an
// ID; it is logged for correlation but never used as ours.
var clientRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Instrument is the outermost middleware: it gives every request an ID
// (X-Request-ID on the response, request_id in every log line and error
// body), logs one access line and records the HTTP metrics. The route label is
// the ServeMux pattern ("GET /v1/days/{day}", never the raw path), so
// cardinality stays bounded. A valid trace header becomes the lines' trace
// (Cloud Logging's format only). With clients (the process's, the same the
// limits key by), a request with X-Forwarded-For is sampled for the "client
// address" line (clientAddressLog: which entry was picked, no address); nil
// clients write no such line (a test's chain).
func Instrument(log *slog.Logger, clients *ClientKeys, next http.Handler) http.Handler {
	var sample *clientAddressLog
	if clients != nil {
		sample = &clientAddressLog{keys: clients, log: log, now: time.Now}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, f := logx.NewContext(r.Context())
		f.RequestID = newRequestID()
		if c := r.Header.Get(RequestIDHeader); clientRequestID.MatchString(c) {
			f.ClientRequestID = c
		}
		f.Trace = traceID(r.Header)
		w.Header().Set(RequestIDHeader, f.RequestID)
		if sample != nil {
			sample.maybe(r)
		}
		r = r.WithContext(ctx)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start)
		if f.Route == "" {
			f.Route = r.Pattern
		}
		route := f.Route
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(dur.Seconds())
		path := r.URL.Path
		if f.LogPath != "" {
			path = f.LogPath
		}
		attrs := []any{"method", r.Method, "path", path, "status", sw.status, "bytes", sw.bytes, "dur_ms", dur.Milliseconds()}
		if f.Code != "" {
			attrs = append(attrs, "code", f.Code)
		}
		if g := w.Header().Values(GateHeader); len(g) > 0 {
			attrs = append(attrs, "gate", strings.Join(g, ","))
		}
		log.InfoContext(ctx, "http", attrs...)
	})
}

func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// Recover turns a panicking handler into a JSON 500 (if nothing was written
// yet), logs the stack and counts it. http.ErrAbortHandler is re-raised: it is
// net/http's way to abort a response on purpose.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				panic(v)
			}
			route := "unmatched"
			if f := Info(r.Context()); f != nil && f.Route != "" {
				route = f.Route
			}
			metrics.Panics.WithLabelValues(route).Inc()
			slog.Default().ErrorContext(r.Context(), "panic", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if !sw.wrote {
				WriteError(sw, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// SecurityHeaders sets the response headers an API carrying health data
// should always send: no caching anywhere, no content sniffing, no framing,
// no referrers, HTTPS only.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// Timeout gives the request's work a deadline; database calls fail with a
// deadline error once it passes, which Fail turns into a 503.
func Timeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MetricsServer is the Prometheus /metrics server for its own port (METRICS_PORT), off
// Cloud Run only: there the metrics are pushed (OTLP, design §6) and nothing listens
// beside $PORT.
func MetricsServer(log *slog.Logger, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
}

// Server is the service's HTTP server with its timeouts: read header 10 s, read
// and write 60 s, idle 120 s, 64 KiB of headers. It serves HTTP/1.1 and h2c with
// prior knowledge on the same port (D220): Cloud Run's front end speaks HTTP/2 to
// the container (--use-http2), so a 32 MiB sync is not cut by its 32 MiB limit on
// HTTP/1 requests (design §3). net/http's HTTP/2 defaults carry such a body: flow
// control opens the window as the handler reads, the frame size bounds frames, not
// bodies, and 250 streams per connection is more than Cloud Run's concurrency. On
// HTTP/2, ReadTimeout and WriteTimeout apply per stream.
func Server(log *slog.Logger, addr string, h http.Handler) *http.Server {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		Protocols:         &p,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// CloudRunService reports whether the process is a Cloud Run service (K_SERVICE is
// set): its ready paths answer generically (Health, CloudReady) and app starts no
// /metrics listener.
func CloudRunService() bool { return os.Getenv("K_SERVICE") != "" }

// onCloudRun reports whether the process is a Cloud Run service or job (K_SERVICE or
// CLOUD_RUN_JOB set): its logs are in Cloud Logging's format.
func onCloudRun() bool { return CloudRunService() || os.Getenv("CLOUD_RUN_JOB") != "" }

// Logger returns the service's JSON logger at LOG_LEVEL and makes it the
// default, so library logs (e.g. JWKS refresh errors) are structured too. On Cloud
// Run it writes Cloud Logging's format, with GCP_PROJECT's traces (logx.NewCloud).
func Logger(service string) *slog.Logger {
	level := logx.ParseLevel(os.Getenv("LOG_LEVEL"))
	log := logx.New(service, level)
	if onCloudRun() {
		log = logx.NewCloud(os.Stdout, service, level, strings.TrimSpace(os.Getenv("GCP_PROJECT")))
	}
	slog.SetDefault(log)
	return log
}
