package apple

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The error of a refused call is logged by the api: the response's status code
// with the standard phrase, and the body's error only as a bare code. A reason
// phrase, a long or free-text error, or a body that is not JSON never gets in.
func TestRefusalLogsTheStatusAndABareCode(t *testing.T) {
	status, reason, body := 0, "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, reason, len(body), body)
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	c, _ := client(t, &fakeApple{Server: srv})
	for _, tc := range []struct {
		name         string
		status       int
		reason, body string
		want         string
	}{
		{"bare code", 400, "Bad Request", `{"error":"invalid_request"}`, "400 Bad Request: invalid_request"},
		{"free text in the reason phrase", 500, "tim@example.com is down", `{"error":"server_error"}`, "500 Internal Server Error: server_error"},
		{"free text as the error", 400, "Bad Request", `{"error":"cannot load tim@example.com"}`, "400 Bad Request: no error code"},
		{"long error", 400, "Bad Request", `{"error":"` + strings.Repeat("a", 65) + `"}`, "400 Bad Request: no error code"},
		{"uppercase error", 400, "Bad Request", `{"error":"Invalid"}`, "400 Bad Request: no error code"},
		{"not JSON", 502, "Bad Gateway", `<html>tim@example.com</html>`, "502 Bad Gateway: no error code"},
		{"empty body", 503, "Service Unavailable", ``, "503 Service Unavailable: no error code"},
		{"status without a phrase", 520, "Web Server Is Down", `{"error":"x"}`, "520 unknown status: x"},
	} {
		status, reason, body = tc.status, tc.reason, tc.body
		err := c.Revoke(context.Background(), "refresh")
		if err == nil || err.Error() != "apple /auth/revoke: "+tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
