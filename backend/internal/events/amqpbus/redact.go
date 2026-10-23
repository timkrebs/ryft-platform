package amqpbus

import "net/url"

// Redacted is an AMQP URL as a log line or an error may hold it: the password
// of a user:pass@ part replaced by xxxxx (url.URL.Redacted). A URL that does
// not parse is not echoed at all, since where it breaks is not known.
func Redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable url)"
	}
	return u.Redacted()
}
