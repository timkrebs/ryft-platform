package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

// The route label must be the inner pattern even though the request passes an
// outer mux and BearerAuth first — that is what keeps metric cardinality bounded.
func TestInstrumentLabelsByInnerRoutePattern(t *testing.T) {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/days/{day}", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]string{"day": r.PathValue("day")})
	})
	mux := http.NewServeMux()
	mux.Handle("/v1/", BearerAuth("k", api))
	h := Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)

	for _, day := range []string{"2026-10-17", "2026-10-18"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/days/"+day, nil)
		req.Header.Set("Authorization", "Bearer k")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	}
	if n := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("GET /v1/days/{day}", "200")); n != 2 {
		t.Fatalf("want 2 requests under the pattern label, got %v", n)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/days/2026-10-18", nil))
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing token: status %d", rec.Code)
	}
	if n := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("/v1/", "401")); n != 1 {
		t.Fatalf("unauthenticated request should be labelled with the outer pattern, got %v", n)
	}
}
