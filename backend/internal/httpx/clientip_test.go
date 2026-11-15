package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// The address a per-address limit keys by (design 2026-11-05-admin-web §4.6, design §5.1): from the right,
// the first X-Forwarded-For entry that is a client on the internet, the one our own proxy (Traefik,
// Google's front end) appended. A client's own claim sits to its left and is ignored; a hop the
// infrastructure appended after it is skipped.
func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		xff    []string
		xri    string
		remote string
		want   string
	}{
		{"one entry: the client", []string{"203.0.113.7"}, "", "10.0.0.5:4242", "203.0.113.7"},
		{"a forged leftmost entry is ignored", []string{"1.2.3.4, 203.0.113.7"}, "", "10.0.0.5:4242", "203.0.113.7"},
		{"forged entries over several header lines", []string{"1.2.3.4", "5.6.7.8, 203.0.113.7"}, "", "10.0.0.5:4242", "203.0.113.7"},
		{"spaces and a trailing comma", []string{" 1.2.3.4 ,  203.0.113.7 , "}, "", "10.0.0.5:4242", "203.0.113.7"},
		{"X-Real-Ip is never used", []string{"203.0.113.7"}, "9.9.9.9", "10.0.0.5:4242", "203.0.113.7"},
		{"no header: the peer", nil, "9.9.9.9", "198.51.100.9:55555", "198.51.100.9"},
		{"rightmost entry is not an IP: the peer", []string{"203.0.113.7, unknown"}, "", "198.51.100.9:55555", "198.51.100.9"},
		{"garbage only: the peer", []string{"<script>"}, "", "198.51.100.9:55555", "198.51.100.9"},
		{"IPv6 is masked to its /64", []string{"2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "", "10.0.0.5:1", "2001:db8:1:2::"},
		{"another address of the same /64 shares the key", []string{"2001:db8:1:2:1::1"}, "", "10.0.0.5:1", "2001:db8:1:2::"},
		{"IPv4-mapped IPv6 reads as IPv4", []string{"::ffff:203.0.113.7"}, "", "10.0.0.5:1", "203.0.113.7"},
		{"IPv6 peer", nil, "", "[2001:db8::7]:443", "2001:db8::"},
		{"a peer without a port", nil, "", "198.51.100.9", "198.51.100.9"},
		{"a peer's zone is dropped", nil, "", "[fe80::1%eth0]:1", "fe80::"},

		// Google's front end appends the address it saw; a hop of its own after it is skipped.
		{"Cloud Run: forged, then the observed address", []string{"203.0.113.7, 198.51.100.20"}, "", "169.254.169.126:41000", "198.51.100.20"},
		{"a Google load-balancer hop after the client", []string{"203.0.113.7, 198.51.100.20, 35.191.3.4"}, "", "169.254.169.126:1", "198.51.100.20"},
		{"a Google health-check range hop", []string{"198.51.100.20, 130.211.0.9"}, "", "169.254.169.126:1", "198.51.100.20"},
		{"a Google IPv6 hop", []string{"2001:db8:5:6::1, 2600:2d00:1:b029::7"}, "", "169.254.169.126:1", "2001:db8:5:6::"},
		{"private and link-local hops after the client", []string{"198.51.100.20, 10.1.2.3", "169.254.1.1, fd00::1"}, "", "169.254.169.126:1", "198.51.100.20"},
		{"a shared (CGNAT) hop", []string{"198.51.100.20, 100.64.0.1"}, "", "169.254.169.126:1", "198.51.100.20"},
		{"a link-local entry with a zone is a hop", []string{"fe80::1%eth0"}, "", "10.0.0.5:1", "10.0.0.5"},
		{"only hops: the peer", []string{"10.0.0.9, 35.191.0.1, 127.0.0.1"}, "", "198.51.100.9:1", "198.51.100.9"},
		{"garbage behind a hop ends the walk", []string{"evil, 10.0.0.9"}, "", "198.51.100.9:1", "198.51.100.9"},
		{"a private forged entry behind the client is not reached", []string{"10.9.9.9, 198.51.100.20"}, "", "169.254.169.126:1", "198.51.100.20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
			r.RemoteAddr = tc.remote
			for _, l := range tc.xff {
				r.Header.Add("X-Forwarded-For", l)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-Ip", tc.xri)
			}
			if got := ClientIP(r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Runbook step 9 (design §5.1): a request with a forged X-Forwarded-For shows, once a minute per
// instance at most, the address ClientIP picked, the entry count and the peer, and nothing else: no
// header, no request id, user or route.
func TestClientAddressLine(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewWriter(&buf, "api", slog.LevelInfo)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	c := &clientAddressLog{log: log, now: func() time.Time { return now }}
	req := func(xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		r.RemoteAddr = "169.254.169.126:41000"
		for _, l := range xff {
			r.Header.Add("X-Forwarded-For", l)
		}
		return r
	}
	c.maybe(req()) // no header: no line, and the minute does not start
	c.maybe(req("203.0.113.7, 198.51.100.20"))
	now = now.Add(30 * time.Second)
	c.maybe(req("198.51.100.21")) // within the minute: none
	now = now.Add(31 * time.Second)
	c.maybe(req("192.0.2.200", "203.0.113.9, 2001:db8:7:8::9"))

	keys := logcheck.Lines{"client address": {
		"xff_entries": logcheck.CountMax(maxForwardedEntries),
		"client_ip":   logcheck.Text(45),
		"remote_host": logcheck.Text(45),
	}}
	lines, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), keys, logcheck.Forbidden{Texts: []string{"203.0.113.7", "192.0.2.200"}})
	for _, p := range problems {
		t.Error(p)
	}
	got := lines["client address"]
	if len(got) != 2 {
		t.Fatalf("want 2 lines (one a minute), got %d: %s", len(got), buf.String())
	}
	for i, want := range []map[string]any{
		{"xff_entries": float64(2), "client_ip": "198.51.100.20", "remote_host": "169.254.169.126", "level": "INFO"},
		{"xff_entries": float64(3), "client_ip": "2001:db8:7:8::", "remote_host": "169.254.169.126", "level": "INFO"},
	} {
		for k, v := range want {
			if got[i][k] != v {
				t.Errorf("line %d: %s = %v, want %v", i, k, got[i][k], v)
			}
		}
		for _, k := range []string{"request_id", "user_id", "route"} {
			if _, ok := got[i][k]; ok {
				t.Errorf("line %d carries %s", i, k)
			}
		}
	}
}

// Instrument samples the line for every service that has it in front (api, ingest).
func TestInstrumentLogsTheClientAddress(t *testing.T) {
	var buf bytes.Buffer
	h := Instrument(logx.NewWriter(&buf, "ingest", slog.LevelInfo), http.HandlerFunc(Live))
	for range 3 {
		r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.7, 198.51.100.20")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == "client address" {
			n++
			if m["client_ip"] != "198.51.100.20" {
				t.Errorf("picked %v", m["client_ip"])
			}
		}
	}
	if n != 1 {
		t.Errorf("%d client address lines for three requests in a minute, want 1", n)
	}
}
