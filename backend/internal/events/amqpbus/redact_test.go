package amqpbus

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// A URL with credentials in it is never logged or put in an error as it is. The
// password is built at run time.
func TestRedactedHidesThePassword(t *testing.T) {
	pw := strings.Repeat("p", 3) + "-" + strings.Repeat("w", 3)
	for raw, want := range map[string]string{
		"amqp://ryft:" + pw + "@rabbit:5672/":      "amqp://ryft:xxxxx@rabbit:5672/",
		"amqps://rabbit:5671":                      "amqps://rabbit:5671",
		"http://ryft:" + pw + "@rabbit:5672/vhost": "http://ryft:xxxxx@rabbit:5672/vhost",
		"amqp://ryft:" + pw + "@ra bbit/":          "(unparseable url)",
		// opaque: no User for url.Redacted to mask
		"ryft:" + pw + "@rabbit:5672/ryft":  "(unparseable url)",
		"amqp:ryft:" + pw + "@rabbit:5672/": "(unparseable url)",
		"amqps:///ryft":                     "(unparseable url)",
	} {
		got := Redacted(raw)
		if got != want || strings.Contains(got, pw) {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

// The bad-scheme error of Open quotes the URL redacted.
func TestOpenBadSchemeHidesThePassword(t *testing.T) {
	pw := strings.Repeat("p", 3) + "-" + strings.Repeat("w", 3)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := Open(context.Background(), Options{URL: "http://ryft:" + pw + "@rabbit:5672/"}, log)
	if err == nil || strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("%v", err)
	}
	// Opaque URLs have no user to mask: the scheme "ryft" (amqp:// forgotten) is
	// refused without quoting, and amqp:user:pw@host passes the scheme check but
	// not the host check, so nothing opens, logs or echoes it.
	for _, raw := range []string{"ryft:" + pw + "@rabbit:5672/ryft", "amqp:ryft:" + pw + "@rabbit:5672/", "amqps:"} {
		if _, err := Open(context.Background(), Options{URL: raw}, log); err == nil || strings.Contains(err.Error(), pw) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
