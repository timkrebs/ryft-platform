// Package httpx has the small HTTP plumbing every service shares: JSON helpers,
// request logging and metrics, health/metrics endpoints and graceful shutdown.
// Authentication lives in internal/auth.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/metrics"
)

const maxBody = 32 << 20 // 32 MiB: a first HealthKit backfill can be large

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

func ReadJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// NotFound answers unknown routes (and unknown methods on known paths) with
// the API's JSON error shape instead of ServeMux's plain-text 404/405. Mount it
// on "/" and on the authenticated "/v1/" mux.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, "not found")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

// RequestInfo collects what inner layers learn about a request — the route
// pattern that matched and the authenticated user — for Instrument to log and
// count once the request is done. It travels as a pointer in the context
// because middleware like auth hands copies of the request (r.WithContext)
// inward, and ServeMux records the pattern only on the copy it was given.
type RequestInfo struct {
	Route  string
	UserID string
}

type infoKey struct{}

// Info returns the request's RequestInfo, or nil outside Instrument.
func Info(ctx context.Context) *RequestInfo {
	ri, _ := ctx.Value(infoKey{}).(*RequestInfo)
	return ri
}

// WithRoutes serves mux and records the pattern that matched in RequestInfo
// before the handler runs.
func WithRoutes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ri := Info(r.Context()); ri != nil {
			if _, pattern := mux.Handler(r); pattern != "" {
				ri.Route = pattern
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// Instrument logs every request and records it in the HTTP metrics. The route
// label is the ServeMux pattern ("GET /v1/days/{day}", never the raw path), so
// cardinality stays bounded.
func Instrument(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &RequestInfo{}
		r = r.WithContext(context.WithValue(r.Context(), infoKey{}, ri))
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start)
		route := ri.Route
		if route == "" {
			route = r.Pattern
		}
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(dur.Seconds())
		attrs := []any{"method", r.Method, "path", r.URL.Path, "route", route, "status", sw.status,
			"dur_ms", dur.Milliseconds()}
		if ri.UserID != "" {
			attrs = append(attrs, "user_id", ri.UserID)
		}
		log.InfoContext(r.Context(), "http", attrs...)
	})
}

// Health mounts /healthz (process up), /readyz (dependencies reachable) and
// /metrics (Prometheus). None of them require auth; the Ingress only routes /v1.
func Health(mux *http.ServeMux, ready func(context.Context) error) {
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			WriteError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		WriteJSON(w, 200, map[string]string{"status": "ready"})
	})
}

// Serve runs the server until SIGINT/SIGTERM, then drains for up to 10s.
func Serve(log *slog.Logger, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// Logger returns a JSON slog logger tagged with the service name.
func Logger(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
}
