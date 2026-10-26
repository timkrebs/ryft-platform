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
// (22P05), and refuses bytes that are not UTF-8 in text (22021). These checks
// keep it from becoming a 500 (docs/API.md → Errors → NUL):
//   - ReadJSON: a "\u0000" in any string of the body, key or value → ErrNUL, 422;
//   - ReadJSONStripNUL (POST /v1/sync): the same "\u0000" removed, page accepted;
//   - both: json.RawMessage fields re-encoded, bad UTF-8 → U+FFFD (raw.go);
//   - WithRoutes: a NUL or non-UTF-8 byte in the path or query → 400;
//   - Fail: the database's refusal, for a path that bypasses them → 422.
//
// None logs the text; each counts ryft_http_text_refused_total or, for a
// stripped sync, ryft_http_nul_stripped_total.

// ErrNUL is ReadJSON's error for a body with "\u0000" in a string (a value or
// an object key, at any depth). WriteDecodeError answers it 422.
var ErrNUL = errors.New(`strings must not contain the NUL character (\u0000)`)

const (
	msgURLText = "the path and query must be UTF-8 text without NUL characters"
	msgDBText  = "the request holds text that cannot be stored (a NUL character or bytes that are not UTF-8)"
)

// lexer follows JSON's lexical rules just far enough to find "\u0000" in a
// string: a quote opens and closes a string; inside one a backslash escapes the
// next byte and \u takes four hex digits. That is exact for every text the
// decoder accepts: "\\u0000" (an escaped backslash, then "u0000") is six
// ordinary characters, an escaped quote does not end the string, and object
// keys count like values. U+0000's digits are all '0', so the case of hex
// digits does not matter. Raw 0x00 bytes are not JSON; the decoder refuses
// them (400). What the lexer makes of text the decoder refuses is never used.
type lexer struct {
	state lexState
	n     int  // \u digits read
	zero  bool // all of them '0'
}

type lexState uint8

const (
	lexOutside lexState = iota // between strings
	lexString                  // inside a string
	lexEscape                  // after a backslash inside a string
	lexUnicode                 // reading the four digits of \u
)

// lexStep is what a byte was to the lexer.
type lexStep uint8

const (
	stepByte    lexStep = iota // outside an escape
	stepEscape                 // part of an escape still open (its backslash: state lexEscape after it)
	stepEscaped                // the last byte of an escape other than \u0000
	stepNUL                    // the last byte of \u0000
)

func (l *lexer) step(c byte) lexStep {
	switch l.state {
	case lexOutside:
		if c == '"' {
			l.state = lexString
		}
	case lexString:
		switch c {
		case '\\':
			l.state = lexEscape
			return stepEscape
		case '"':
			l.state = lexOutside
		}
	case lexEscape:
		if c == 'u' {
			l.state, l.n, l.zero = lexUnicode, 0, true
			return stepEscape
		}
		l.state = lexString
		return stepEscaped
	case lexUnicode:
		l.zero = l.zero && c == '0'
		if l.n++; l.n < 4 {
			return stepEscape
		}
		l.state = lexString
		if l.zero {
			return stepNUL
		}
		return stepEscaped
	}
	return stepByte
}

func (l *lexer) inEscape() bool { return l.state == lexEscape || l.state == lexUnicode }

// nulScanner passes a JSON text through unchanged and notes where the first
// "\u0000" inside a string starts.
type nulScanner struct {
	r   io.Reader
	lex lexer
	off int64 // bytes passed through so far
	esc int64 // offset of the backslash of the escape being read
	nul int64 // offset of the first NUL escape's backslash, -1 for none
}

func newNULScanner(r io.Reader) *nulScanner { return &nulScanner{r: r, nul: -1} }

func (s *nulScanner) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.scan(p[:n])
	return n, err
}

func (s *nulScanner) scan(b []byte) {
	for i := 0; i < len(b) && s.nul < 0; i++ { // the first one is all ReadJSON needs
		switch s.lex.step(b[i]) {
		case stepEscape:
			if s.lex.state == lexEscape {
				s.esc = s.off + int64(i)
			}
		case stepNUL:
			s.nul = s.esc
		}
	}
	s.off += int64(len(b))
}

// nulStripper passes a JSON text through without its "\u0000" escapes and
// counts them; every other byte, "\\u0000" included, stays as written. It works
// in the caller's buffer: bytes move only behind a removed escape, so a body
// without one is never copied. An escape a read ends in is held back (at most
// five bytes) until the next read decides it, so the caller's buffer must be
// larger than that (json.Decoder reads at least 512 bytes). Removing a whole
// escape from inside a string keeps valid JSON valid and invalid JSON invalid.
type nulStripper struct {
	r       io.Reader
	lex     lexer
	held    []byte // the start of an escape the last read ended in
	err     error  // the source's error, returned once held is passed on
	removed int    // \u0000 escapes removed
}

func (s *nulStripper) Read(p []byte) (int, error) {
	for {
		if s.err != nil {
			// The source ended inside an escape: not JSON; the decoder refuses it.
			n := copy(p, s.held)
			s.held = s.held[n:]
			if n > 0 {
				return n, nil
			}
			return 0, s.err
		}
		k := len(s.held)
		if len(p) <= k {
			return 0, io.ErrShortBuffer
		}
		copy(p, s.held)
		s.held = s.held[:0]
		n, err := s.r.Read(p[k:])
		s.err = err
		w, esc := k, 0 // a held escape starts at p[0]
		for _, c := range p[k : k+n] {
			switch s.lex.step(c) {
			case stepNUL:
				w = esc
				s.removed++
				continue
			case stepEscape:
				if s.lex.state == lexEscape {
					esc = w
				}
			}
			p[w] = c
			w++
		}
		if s.lex.inEscape() {
			s.held = append(s.held, p[esc:w]...)
			w = esc
		}
		if w > 0 {
			return w, nil
		}
	}
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
	metrics.TextRefused.WithLabelValues(routeOf(r), at).Inc()
}

// routeOf is the request's route pattern for a metric label, "unmatched" before
// WithRoutes recorded one.
func routeOf(r *http.Request) string {
	if f := Info(r.Context()); f != nil && f.Route != "" {
		return f.Route
	}
	return "unmatched"
}
