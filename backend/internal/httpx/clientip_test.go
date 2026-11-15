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

// The address a per-address limit keys by (design 2026-11-05-admin-web §4.6, design §5.1): the rightmost
// X-Forwarded-For entry, the one our own proxy (Traefik, Google's front end) appended, as it is. A
// client's own claim sits to its left and is ignored; only a hop of Google's front ends after it is
// skipped. A private, shared or link-local entry is a client too (one inside a VPC), never a hop.
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
		{"only Google hops: the peer", []string{"35.191.0.1, 130.211.1.2"}, "", "198.51.100.9:1", "198.51.100.9"},
		{"a private forged entry behind the client is not reached", []string{"10.9.9.9, 198.51.100.20"}, "", "169.254.169.126:1", "198.51.100.20"},

		// A client inside a VPC (Private Google Access, Direct VPC egress) arrives with its private
		// address as the observed entry: that is the key, never the forged entry to its left.
		{"a VPC client with a forged entry", []string{"203.0.113.7, 10.128.0.5"}, "", "169.254.169.126:1", "10.128.0.5"},
		{"a VPC client, forged over two lines", []string{"203.0.113.7", "198.51.100.20, 10.128.0.5"}, "", "169.254.169.126:1", "10.128.0.5"},
		{"a VPC client behind a Google hop", []string{"203.0.113.7, 10.128.0.5, 35.191.3.4"}, "", "169.254.169.126:1", "10.128.0.5"},
		{"an IPv6 VPC client (ULA) with a forged entry", []string{"2001:db8:9::1, fd20:a:b:c::7"}, "", "169.254.169.126:1", "fd20:a:b:c::"},
		{"a shared (CGNAT) entry is a client", []string{"198.51.100.20, 100.64.0.1"}, "", "169.254.169.126:1", "100.64.0.1"},
		{"a link-local entry with a zone is a client, the zone dropped", []string{"fe80::1%eth0"}, "", "10.0.0.5:1", "fe80::"},
		{"a loopback entry is a client", []string{"203.0.113.7, 127.0.0.1"}, "", "198.51.100.9:1", "127.0.0.1"},
		{"garbage at the right: the peer", []string{"10.0.0.9, evil"}, "", "198.51.100.9:1", "198.51.100.9"},
		{"garbage behind a Google hop: the peer", []string{"203.0.113.7, evil, 35.191.0.1"}, "", "198.51.100.9:1", "198.51.100.9"},
		{"garbage left of the rightmost is not reached", []string{"evil, 10.0.0.9"}, "", "198.51.100.9:1", "10.0.0.9"},
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

// clientAddressKeys is the "client address" line's allowlist: which entry ClientIP took and how many
// there were (runbook step 9), non-personal counts and a closed set, never an address (D222).
var clientAddressKeys = logcheck.Keys{
	"picked":      logcheck.Enum("xff", "remote"),
	"xff_index":   logcheck.CountMax(maxForwardedEntries),
	"xff_entries": logcheck.CountMax(maxForwardedEntries),
}

// The proof behind runbook step 9 (design §5.1): what reaches the service when a caller sends three
// forged X-Forwarded-For entries through Google's front end, which appends the address it observed (and
// maybe a hop of its own). ClientIP must take the observed address, and the line's xff_index must point
// at exactly the entry ClientIP took, so the runbook can read the choice off the line without an address
// in it. The two shapes the runbook stops on are here too, with the index it tells them by.
func TestStepNineUnderGoogleFrontEnd(t *testing.T) {
	const forged = "203.0.113.7, 203.0.113.8, 203.0.113.9" // the runbook's curl -H
	for _, tc := range []struct {
		name    string
		xff     []string
		want    string // ClientIP
		picked  string
		index   float64 // xff_index; -1: none on the line
		entries float64
	}{
		{"Google appends the observed address", []string{forged + ", 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"as two header lines", []string{forged, "198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"and a load-balancer hop of its own (35.191.0.0/16)", []string{forged + ", 198.51.100.20, 35.191.3.4"}, "198.51.100.20", "xff", 3, 5},
		{"and two hops (35.191/16, 130.211/22)", []string{forged + ", 198.51.100.20, 35.191.3.4, 130.211.0.9"}, "198.51.100.20", "xff", 3, 6},
		{"an IPv6 caller and an IPv6 hop", []string{forged + ", 2001:db8:5:6::1, 2600:2d00:1:b029::7"}, "2001:db8:5:6::", "xff", 3, 5},
		{"a caller that forges a Google range too", []string{"35.191.0.1, 130.211.0.9, 203.0.113.9, 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"a caller that forges garbage", []string{"evil, <script>, 203.0.113.9, 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},

		// Stop: a hop of Google's outside the known ranges is picked (index past the forged three, one
		// bucket for every client).
		{"stop: an unknown Google hop", []string{forged + ", 198.51.100.20, 34.96.0.1"}, "34.96.0.1", "xff", 4, 5},
		// Stop: Google appended nothing, the last forged entry wins (index 2 of 3).
		{"stop: nothing appended", []string{forged}, "203.0.113.9", "xff", 2, 3},
		// No entry would do: RemoteAddr, and no index.
		{"garbage at the right: the peer", []string{forged + ", unknown"}, "169.254.169.126", "remote", -1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			r.RemoteAddr = "169.254.169.126:41000" // Cloud Run's peer
			for _, l := range tc.xff {
				r.Header.Add("X-Forwarded-For", l)
			}
			if got := ClientIP(r); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
			var buf bytes.Buffer
			c := &clientAddressLog{log: logx.NewWriter(&buf, "api", slog.LevelInfo), now: time.Now}
			c.maybe(r)
			lines, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), logcheck.Lines{"client address": clientAddressKeys},
				logcheck.Forbidden{Texts: []string{"203.0.113.", "198.51.100.", "2001:db8:", "169.254.", "35.191.", "130.211.", "34.96.", "2600:2d00:"}})
			for _, p := range problems {
				t.Error(p)
			}
			got := lines["client address"]
			if len(got) != 1 {
				t.Fatalf("want one line, got %s", buf.String())
			}
			l := got[0]
			if l["picked"] != tc.picked || l["xff_entries"] != tc.entries {
				t.Errorf("line %v, want picked %s, xff_entries %v", l, tc.picked, tc.entries)
			}
			idx, has := l["xff_index"]
			switch {
			case tc.index < 0 && has:
				t.Errorf("a line that picked the peer carries xff_index %v", idx)
			case tc.index >= 0 && idx != tc.index:
				t.Errorf("xff_index = %v, want %v", idx, tc.index)
			case tc.index >= 0:
				// The index points at the entry ClientIP took, counted as the line counts.
				var all []string
				for _, h := range tc.xff {
					for _, p := range strings.Split(h, ",") {
						if p = strings.TrimSpace(p); p != "" {
							all = append(all, p)
						}
					}
				}
				r2 := httptest.NewRequest(http.MethodGet, "/", nil)
				r2.Header.Set("X-Forwarded-For", all[int(tc.index)])
				if at := ClientIP(r2); at != tc.want {
					t.Errorf("entry %v is %q, ClientIP took %q", tc.index, at, tc.want)
				}
			}
		})
	}
}

// The line is sampled once a minute per instance and holds the choice only: no address, whole or in
// part, no header, no request id, user or route.
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

	// Every address the requests carried, and their networks: the forged ones, the picked ones, the peer.
	forbid := logcheck.Forbidden{Texts: []string{"203.0.113.", "192.0.2.", "198.51.100.", "2001:db8:", "169.254.", "/24", "/48"}}
	lines, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), logcheck.Lines{"client address": clientAddressKeys}, forbid)
	for _, p := range problems {
		t.Error(p)
	}
	got := lines["client address"]
	if len(got) != 2 {
		t.Fatalf("want 2 lines (one a minute), got %d: %s", len(got), buf.String())
	}
	for i, want := range []map[string]any{
		{"xff_entries": float64(2), "picked": "xff", "xff_index": float64(1), "level": "INFO"},
		{"xff_entries": float64(3), "picked": "xff", "xff_index": float64(2), "level": "INFO"},
	} {
		for k, v := range want {
			if got[i][k] != v {
				t.Errorf("line %d: %s = %v, want %v", i, k, got[i][k], v)
			}
		}
		for _, k := range []string{"request_id", "user_id", "route", "client_ip", "remote_host", "client_prefix", "remote_prefix"} {
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
			if m["picked"] != "xff" || m["xff_index"] != float64(1) || m["xff_entries"] != float64(2) {
				t.Errorf("line %v, want the second of two entries", m)
			}
			if strings.Contains(l, "198.51.100") || strings.Contains(l, "203.0.113") {
				t.Errorf("the line holds an address: %s", l)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d client address lines for three requests in a minute, want 1", n)
	}
}
