// Package httpx has the small HTTP plumbing every service shares: JSON helpers,
// bearer auth, request logging and metrics, health/metrics endpoints and
// graceful shutdown.
package httpx

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

// BearerAuth rejects requests whose Authorization header does not carry the
// configured key. Constant-time compare so timing does not leak the key.
func BearerAuth(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		panic("httpx.BearerAuth: empty API key")
	}
	want := []byte(apiKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(tok), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ryft"`)
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

// Instrument logs every request and records it in the HTTP metrics. The route
// label is the ServeMux pattern that matched (set on r by the mux, including
// nested muxes behind BearerAuth), so /v1/days/2026-10-18 counts as
// "GET /v1/days/{day}".
func Instrument(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(dur.Seconds())
		log.Info("http", "method", r.Method, "path", r.URL.Path, "route", route, "status", sw.status,
			"dur_ms", dur.Milliseconds())
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
