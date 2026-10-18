package httpx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
)

type ctxKey struct{}

// The route label must be the inner pattern even though the request passes an
// outer mux and a middleware that hands a *copy* of the request inward (as
// auth does with r.WithContext) — the copy's Pattern never reaches Instrument,
// so WithRoutes records it in RequestInfo. That keeps metric cardinality bounded.
func TestInstrumentLabelsByInnerRouteAcrossRequestCopies(t *testing.T) {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/days/{day}", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]string{"day": r.PathValue("day")})
	})
	copying := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ri := Info(r.Context()); ri != nil {
				ri.UserID = "user-1"
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, "x")))
		})
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", copying(WithRoutes(api)))
	h := Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)

	for _, day := range []string{"2026-10-17", "2026-10-18"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/days/"+day, nil))
		if rec.Code != 200 || rec.Body.String() != `{"day":"`+day+`"}`+"\n" {
			t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
		}
	}
	if n := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("GET /v1/days/{day}", "200")); n != 2 {
		t.Fatalf("want 2 requests under the pattern label, got %v", n)
	}
	if n := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("/v1/", "200")); n != 0 {
		t.Fatalf("requests must not be counted under the outer pattern, got %v", n)
	}
}
