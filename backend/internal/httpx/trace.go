package httpx

import (
	"net/http"
	"regexp"
	"strings"
)

var (
	// traceparent is W3C Trace Context: version-traceid-parentid-flags, lower-case hex.
	traceparent = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)
	// cloudTrace is Google's X-Cloud-Trace-Context: TRACE_ID[/SPAN_ID][;o=OPTIONS], the span decimal.
	cloudTrace = regexp.MustCompile(`^([0-9a-fA-F]{32})(/[0-9]{1,20})?(;o=[0-9]{1,2})?$`)
)

// zeroTrace is the trace id both formats reserve as invalid.
const zeroTrace = "00000000000000000000000000000000"

// traceID is the request's trace id for Cloud Logging's trace field (design §6): the one in
// traceparent, else the one in X-Cloud-Trace-Context, as 32 lower-case hex digits; "" when
// neither header holds a valid id (wrong shape, all zeros, traceparent version ff). The
// shape is checked in full, so a header cannot put anything else into a log line.
func traceID(h http.Header) string {
	if m := traceparent.FindStringSubmatch(strings.TrimSpace(h.Get("traceparent"))); m != nil && m[1] != "ff" && m[2] != zeroTrace {
		return m[2]
	}
	if m := cloudTrace.FindStringSubmatch(strings.TrimSpace(h.Get("X-Cloud-Trace-Context"))); m != nil {
		if id := strings.ToLower(m[1]); id != zeroTrace {
			return id
		}
	}
	return ""
}
