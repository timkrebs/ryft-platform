package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCORS(origins ...string) CORS {
	return CORS{Origins: origins, Methods: []string{"GET", "POST", "DELETE"}, Headers: []string{"Authorization", "Content-Type"},
		Expose: []string{"X-Request-ID", "Retry-After"}, MaxAge: 10 * time.Minute}
}

// corsServer answers 401 behind the CORS wrapper, like the admin routes without a bearer, and counts
// how often the inner handler ran.
func corsServer(c CORS) (http.Handler, *int) {
	ran := new(int)
	return c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*ran++
		WriteError(w, http.StatusUnauthorized, "no")
	})), ran
}

func acHeaders(h http.Header) []string {
	var out []string
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			out = append(out, k)
		}
	}
	return out
}

// design 2026-11-05-admin-web §5: the CORS table, row by row.
func TestCORSPreflight(t *testing.T) {
	const page = "https://ryftlab.de"
	srv, ran := corsServer(testCORS(page))
	preflight := func(origin, method, headers string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodOptions, "/v1/admin/invites", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if method != "" {
			r.Header.Set("Access-Control-Request-Method", method)
		}
		if headers != "" {
			r.Header.Set("Access-Control-Request-Headers", headers)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}

	t.Run("allowed", func(t *testing.T) {
		for _, tc := range []struct{ method, headers string }{
			{"POST", "authorization,content-type"}, {"GET", "Authorization"}, {"DELETE", ""}, {"POST", "Content-Type , AUTHORIZATION"},
			{"GET", "authorization,"},
		} {
			rec := preflight(page, tc.method, tc.headers)
			h := rec.Header()
			if rec.Code != 204 || rec.Body.Len() != 0 ||
				h.Get("Access-Control-Allow-Origin") != page ||
				h.Get("Access-Control-Allow-Methods") != "GET, POST, DELETE" ||
				h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
				h.Get("Access-Control-Max-Age") != "600" ||
				h.Get("Access-Control-Allow-Credentials") != "" ||
				strings.Join(h.Values("Vary"), ",") != "Origin,Access-Control-Request-Method,Access-Control-Request-Headers" {
				t.Errorf("%+v: %d %v", tc, rec.Code, h)
			}
		}
	})
	t.Run("refused: 204 and no Access-Control header at all", func(t *testing.T) {
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"other origin":     preflight("https://evil.example", "GET", "authorization"),
			"null origin":      preflight("null", "GET", ""),
			"star origin":      preflight("*", "GET", ""),
			"suffix of origin": preflight("https://ryftlab.de.evil.example", "GET", ""),
			"scheme differs":   preflight("http://ryftlab.de", "GET", ""),
			"port differs":     preflight("https://ryftlab.de:8443", "GET", ""),
			"upper-case host":  preflight("https://RYFTLAB.de", "GET", ""),
			"PUT":              preflight(page, "PUT", "authorization"),
			"PATCH":            preflight(page, "PATCH", ""),
			"lower-case get":   preflight(page, "get", ""),
			"another header":   preflight(page, "GET", "authorization,x-admin-key"),
			"cookie header":    preflight(page, "GET", "cookie"),
		} {
			if rec.Code != 204 || len(acHeaders(rec.Header())) != 0 {
				t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
			}
		}
	})
	if *ran != 0 {
		t.Fatalf("a preflight never reaches the handler behind (auth, audit, metrics): ran %d times", *ran)
	}
	t.Run("OPTIONS without Origin or without a requested method is no preflight", func(t *testing.T) {
		for _, rec := range []*httptest.ResponseRecorder{preflight("", "GET", ""), preflight(page, "", "")} {
			if rec.Code != 401 {
				t.Errorf("passes on to the handler (and its auth): %d", rec.Code)
			}
		}
		if *ran != 2 {
			t.Errorf("ran %d times", *ran)
		}
	})
}

func TestCORSActualRequest(t *testing.T) {
	const page = "https://ryftlab.de"
	srv, _ := corsServer(testCORS(page, "http://localhost:3000"))
	get := func(origin string) http.Header {
		r := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != 401 {
			t.Fatalf("CORS never answers for the handler: %d", rec.Code)
		}
		return rec.Header()
	}
	for _, o := range []string{page, "http://localhost:3000"} {
		h := get(o) // even a refused request (401 here) is readable by the allowed page
		if h.Get("Access-Control-Allow-Origin") != o || h.Get("Access-Control-Expose-Headers") != "X-Request-ID, Retry-After" ||
			h.Get("Vary") != "Origin" || h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: %v", o, h)
		}
	}
	for _, o := range []string{"", "https://evil.example", "null", "*", "https://ryftlab.de/", "HTTPS://ryftlab.de"} {
		h := get(o)
		if len(acHeaders(h)) != 0 || h.Get("Vary") != "Origin" {
			t.Errorf("origin %q: %v", o, h)
		}
	}
}

// Empty Origins (the config default) allows nothing, whatever the page sends.
func TestCORSEmptyOriginsAllowNothing(t *testing.T) {
	srv, _ := corsServer(testCORS())
	for _, origin := range []string{"https://ryftlab.de", "null", "*", ""} {
		r := httptest.NewRequest(http.MethodOptions, "/v1/admin/invites", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "GET")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if len(acHeaders(rec.Header())) != 0 {
			t.Errorf("preflight from %q: %v", origin, rec.Header())
		}
		g := httptest.NewRequest(http.MethodGet, "/v1/admin/invites", nil)
		g.Header.Set("Origin", origin)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, g)
		if len(acHeaders(rec.Header())) != 0 {
			t.Errorf("request from %q: %v", origin, rec.Header())
		}
	}
}
