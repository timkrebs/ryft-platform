package httpx

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/metrics"
)

// Text Postgres cannot store. A NUL (U+0000) is a valid Go string and valid
// JSON ("\u0000"), but Postgres refuses it in text (SQLSTATE 22021) and jsonb
// (22P05), and refuses bytes that are not UTF-8 in text (22021). Three checks
// keep it from becoming a 500 (docs/API.md → Errors):
//   - ReadJSON: a "\u0000" in any string of the body, key or value → ErrNUL, 422;
//   - WithRoutes: a NUL or non-UTF-8 byte in the path or query → 400;
//   - Fail: the database's refusal, for a path that bypasses both → 422.
//
// None logs the text; each counts ryft_http_text_refused_total.

// ErrNUL is ReadJSON's error for a body with "\u0000" in a string (a value or
// an object key, at any depth). WriteDecodeError answers it 422.
var ErrNUL = errors.New(`strings must not contain the NUL character (\u0000)`)

const (
	msgURLText = "the path and query must be UTF-8 text without NUL characters"
	msgDBText  = "the request holds text that cannot be stored (a NUL character or bytes that are not UTF-8)"
)

// nulScanner passes a JSON text through and notes where the first "\u0000"
// inside a string starts. It follows JSON's lexical rules only — a quote opens
// and closes a string; inside one a backslash escapes the next byte and \u takes
// four hex digits — which is exact for every text the decoder accepts: "\\u0000"
// (an escaped backslash, then "u0000") is six ordinary characters, an escaped
// quote does not end the string, and object keys count like values. U+0000's
// digits are all '0', so the case of hex digits does not matter. Raw 0x00 bytes
// are not JSON; the decoder refuses them (400). What the scanner notes in text
// the decoder refuses is never read.
type nulScanner struct {
	r     io.Reader
	off   int64 // bytes passed through so far
	state lexState
	esc   int64 // offset of the backslash of the escape being read
	n     int   // \u digits read
	zero  bool  // all of them '0'
	nul   int64 // offset of the first NUL escape's backslash, -1 for none
}

type lexState uint8

const (
	lexOutside lexState = iota // between strings
	lexString                  // inside a string
	lexEscape                  // after a backslash inside a string
	lexUnicode                 // reading the four digits of \u
)

func newNULScanner(r io.Reader) *nulScanner { return &nulScanner{r: r, nul: -1} }

func (s *nulScanner) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.scan(p[:n])
	return n, err
}

func (s *nulScanner) scan(b []byte) {
	if s.nul >= 0 { // the first one is all ReadJSON needs
		s.off += int64(len(b))
		return
	}
	for i, c := range b {
		switch s.state {
		case lexOutside:
			if c == '"' {
				s.state = lexString
			}
		case lexString:
			switch c {
			case '\\':
				s.state, s.esc = lexEscape, s.off+int64(i)
			case '"':
				s.state = lexOutside
			}
		case lexEscape:
			s.state = lexString
			if c == 'u' {
				s.state, s.n, s.zero = lexUnicode, 0, true
			}
		case lexUnicode:
			s.zero = s.zero && c == '0'
			if s.n++; s.n == 4 {
				s.state = lexString
				if s.zero && s.nul < 0 {
					s.nul = s.esc
				}
			}
		}
	}
	s.off += int64(len(b))
}

// isText says whether Postgres takes s as text: UTF-8 without a NUL.
func isText(s string) bool { return utf8.ValidString(s) && strings.IndexByte(s, 0) < 0 }

// urlIsText says whether the path and every query key and value are text;
// handlers read nothing else of the URL (path values are parts of the path).
func urlIsText(u *url.URL) bool {
	if !isText(u.Path) {
		return false
	}
	for k, vs := range u.Query() {
		if !isText(k) {
			return false
		}
		for _, v := range vs {
			if !isText(v) {
				return false
			}
		}
	}
	return true
}

// refusedText says whether err is Postgres refusing the request's text: a NUL
// or bytes that are not UTF-8 in text (22021), "\u0000" in jsonb (22P05).
// *pgconn.PgError has SQLState; the interface keeps pgx out of httpx.
func refusedText(err error) bool {
	var pg interface{ SQLState() string }
	if !errors.As(err, &pg) {
		return false
	}
	switch pg.SQLState() {
	case "22021", "22P05":
		return true
	}
	return false
}

// countRefused counts a request refused for its text, by route and where the
// text was: body, url or database.
func countRefused(r *http.Request, at string) {
	route := "unmatched"
	if f := Info(r.Context()); f != nil && f.Route != "" {
		route = f.Route
	}
	metrics.TextRefused.WithLabelValues(route, at).Inc()
}
