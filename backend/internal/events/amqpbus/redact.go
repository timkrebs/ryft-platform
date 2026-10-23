package amqpbus

import "net/url"

// Redacted is an AMQP URL as a log line or an error may hold it: the password
// of a user:pass@ part replaced by xxxxx (url.URL.Redacted). A URL that does
// not parse is not echoed at all, since where it breaks is not known; nor is an
// opaque one or one without a host ("ryft:pw@rabbit:5672/" parses with the
// scheme "ryft", "amqp:ryft:pw@rabbit" with no user), where Redacted would
// have nothing to mask.
func Redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return "(unparseable url)"
	}
	return u.Redacted()
}
