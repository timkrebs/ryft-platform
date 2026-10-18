package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func chain(h http.Handler) http.Handler {
	return Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), Recover(SecurityHeaders(h)))
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestRequestIDOnResponseAndErrors(t *testing.T) {
	h := chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusUnprocessableEntity, "nope")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set(RequestIDHeader, "client-abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	id := rec.Header().Get(RequestIDHeader)
	if id == "" || id == "client-abc" {
		t.Fatalf("the server must generate its own request ID, got %q", id)
	}
	if body := decodeBody(t, rec); body["request_id"] != id || body["error"] != "nope" {
		t.Fatalf("error body %v must carry the request ID %s", body, id)
	}
}

func TestRecoverAnswersJSON500(t *testing.T) {
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if body := decodeBody(t, rec); rec.Code != 500 || body["error"] != "internal error" || body["request_id"] == "" {
		t.Fatalf("status %d body %v", rec.Code, body)
	}
	// A panic after the handler started writing cannot change the status.
	h = chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("late panic: status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestRecoverReraisesAbortHandler(t *testing.T) {
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // identity, as net/http compares it
			t.Fatalf("ErrAbortHandler must propagate, got %v", v)
		}
	}()
	Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, 200, map[string]int{}) })).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	for k, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
		"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") == "" || rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("HSTS and CSP must be set")
	}
}

func TestTimeoutAnswers503AndFailHidesDetails(t *testing.T) {
	slow := Timeout(10*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // a database call honouring the deadline
		Fail(w, r, fmt.Errorf("query samples: %w", r.Context().Err()))
	}))
	rec := httptest.NewRecorder()
	chain(slow).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if body := decodeBody(t, rec); rec.Code != 503 || body["error"] != "request timed out" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d body %v", rec.Code, body)
	}

	rec = httptest.NewRecorder()
	chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Fail(w, r, errors.New(`ERROR: relation "samples" does not exist (SQLSTATE 42P01)`))
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if body := decodeBody(t, rec); rec.Code != 500 || strings.Contains(rec.Body.String(), "relation") || body["error"] != "internal error" {
		t.Fatalf("500 must not leak internals: %s", rec.Body.String())
	}
}

func TestReadJSONLimitIs413(t *testing.T) {
	h := chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if err := ReadJSON(w, r, &v, 16); err != nil {
			WriteDecodeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for body, want := range map[string]int{`{"a":"b"}`: 204, `{"a":"` + strings.Repeat("x", 64) + `"}`: 413, `{"a":`: 400, `{"a":"b","x":1}`: 400} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("%.20s…: status %d, want %d", body, rec.Code, want)
		}
	}
}
