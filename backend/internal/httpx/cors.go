package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CORS lets the browser pages of the listed origins read the answers of the routes it wraps. It is
// not an access control (the bearer is: there are no cookies, so another origin cannot obtain a
// token); it only keeps other web origins from reading answers.
//
//   - Origins are matched by exact string equality. There are no wildcards, no suffix matches, and
//     "null" and "*" never match. Empty Origins means nothing is allowed.
//   - A preflight (OPTIONS with an Origin and an Access-Control-Request-Method) is answered here with
//     204 and never reaches next. An allowed one carries the Access-Control-* headers, a refused one
//     none, which makes the browser refuse the request.
//   - Any other request passes on; with an allowed Origin its answer gains Allow-Origin and
//     Expose-Headers. Every answer carries Vary: Origin.
//   - Credentials are never allowed.
type CORS struct {
	Origins []string
	Methods []string      // allowed request methods, e.g. GET, POST, DELETE
	Headers []string      // allowed request headers, e.g. Authorization, Content-Type
	Expose  []string      // response headers the page may read
	MaxAge  time.Duration // how long the browser may cache a preflight; whole seconds
}

// Wrap puts the CORS rules around next.
func (c CORS) Wrap(next http.Handler) http.Handler {
	methods, headers := strings.Join(c.Methods, ", "), strings.Join(c.Headers, ", ")
	expose, maxAge := strings.Join(c.Expose, ", "), strconv.Itoa(int(c.MaxAge/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		origin := r.Header.Get("Origin")
		allowed := c.allows(origin)
		if r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Add("Vary", "Origin")
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			if allowed && c.method(r.Header.Get("Access-Control-Request-Method")) &&
				c.headers(r.Header.Values("Access-Control-Request-Headers")) {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Add("Vary", "Origin")
		if allowed {
			h.Set("Access-Control-Allow-Origin", origin)
			if expose != "" {
				h.Set("Access-Control-Expose-Headers", expose)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c CORS) allows(origin string) bool {
	if origin == "" || origin == "null" || origin == "*" {
		return false
	}
	for _, o := range c.Origins {
		if o == origin {
			return true
		}
	}
	return false
}

func (c CORS) method(m string) bool {
	for _, a := range c.Methods {
		if a == m {
			return true
		}
	}
	return false
}

// headers reports whether every requested header is allowed (case-insensitive; empty entries of the
// comma-separated list are ignored).
func (c CORS) headers(requested []string) bool {
	for _, line := range requested {
		for _, name := range strings.Split(line, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			ok := false
			for _, a := range c.Headers {
				ok = ok || strings.EqualFold(a, name)
			}
			if !ok {
				return false
			}
		}
	}
	return true
}
