package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// picked is the address ClientKeys keys r by, as text, or "fallback".
func picked(k *ClientKeys, r *http.Request) string {
	a, _, ok := k.pick(r.Header.Values("X-Forwarded-For"))
	if !ok {
		return "fallback"
	}
	return a.String()
}

// The address a per-address limit keys by (design 2026-11-05-admin-web §4.6, design §5.1, D240): walked
// from the right, Google's front-end hops skipped and not counted, the hops-th remaining entry as it is. A
// client's own claim sits to its left and is never read. A private, shared or link-local entry is a client
// too (one inside a VPC), never a hop. Whatever does not give a client is the shared fallback, RemoteAddr
// never: every request here has a peer of its own, and none is ever the answer.
func TestClientKeysPick(t *testing.T) {
	for _, tc := range []struct {
		name string
		hops int // 0: 1
		xff  []string
		xri  string
		want string
	}{
		{name: "one entry: the client", xff: []string{"203.0.113.7"}, want: "203.0.113.7"},
		{name: "a forged leftmost entry is ignored", xff: []string{"1.2.3.4, 203.0.113.7"}, want: "203.0.113.7"},
		{name: "forged entries over several header lines", xff: []string{"1.2.3.4", "5.6.7.8, 203.0.113.7"}, want: "203.0.113.7"},
		{name: "spaces and a trailing comma", xff: []string{" 1.2.3.4 ,  203.0.113.7 , "}, want: "203.0.113.7"},
		{name: "empty lines and entries are not counted", xff: []string{"1.2.3.4, 203.0.113.7", ",", " , "}, want: "203.0.113.7"},
		{name: "X-Real-Ip is never used", xff: []string{"203.0.113.7"}, xri: "9.9.9.9", want: "203.0.113.7"},
		{name: "no header: the fallback, not the peer", xri: "9.9.9.9", want: "fallback"},
		{name: "an empty header: the fallback", xff: []string{" , "}, want: "fallback"},
		{name: "rightmost entry is not an IP: the fallback", xff: []string{"203.0.113.7, unknown"}, want: "fallback"},
		{name: "garbage only: the fallback", xff: []string{"<script>"}, want: "fallback"},
		{name: "IPv6 is masked to its /64", xff: []string{"2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, want: "2001:db8:1:2::"},
		{name: "another address of the same /64 shares the key", xff: []string{"2001:db8:1:2:1::1"}, want: "2001:db8:1:2::"},
		{name: "IPv4-mapped IPv6 reads as IPv4", xff: []string{"::ffff:203.0.113.7"}, want: "203.0.113.7"},

		// Google's front end appends the address it saw; a hop of its own after it is skipped.
		{name: "Cloud Run: forged, then the observed address", xff: []string{"203.0.113.7, 198.51.100.20"}, want: "198.51.100.20"},
		{name: "a Google load-balancer hop after the client", xff: []string{"203.0.113.7, 198.51.100.20, 35.191.3.4"}, want: "198.51.100.20"},
		{name: "a Google health-check range hop", xff: []string{"198.51.100.20, 130.211.0.9"}, want: "198.51.100.20"},
		{name: "a Google IPv6 hop", xff: []string{"2001:db8:5:6::1, 2600:2d00:1:b029::7"}, want: "2001:db8:5:6::"},
		{name: "a Google IPv4-mapped hop", xff: []string{"198.51.100.20, ::ffff:35.191.3.4"}, want: "198.51.100.20"},
		{name: "only Google hops: the fallback", xff: []string{"35.191.0.1, 130.211.1.2"}, want: "fallback"},
		{name: "a private forged entry behind the client is not reached", xff: []string{"10.9.9.9, 198.51.100.20"}, want: "198.51.100.20"},

		// A client inside a VPC (Private Google Access, Direct VPC egress) arrives with its private
		// address as the observed entry: that is the key, never the forged entry to its left.
		{name: "a VPC client with a forged entry", xff: []string{"203.0.113.7, 10.128.0.5"}, want: "10.128.0.5"},
		{name: "a VPC client, forged over two lines", xff: []string{"203.0.113.7", "198.51.100.20, 10.128.0.5"}, want: "10.128.0.5"},
		{name: "a VPC client behind a Google hop", xff: []string{"203.0.113.7, 10.128.0.5, 35.191.3.4"}, want: "10.128.0.5"},
		{name: "an IPv6 VPC client (ULA) with a forged entry", xff: []string{"2001:db8:9::1, fd20:a:b:c::7"}, want: "fd20:a:b:c::"},
		{name: "a shared (CGNAT) entry is a client", xff: []string{"198.51.100.20, 100.64.0.1"}, want: "100.64.0.1"},
		{name: "a link-local entry with a zone is a client, the zone dropped", xff: []string{"fe80::1%eth0"}, want: "fe80::"},
		{name: "a loopback entry is a client", xff: []string{"203.0.113.7, 127.0.0.1"}, want: "127.0.0.1"},
		{name: "garbage at the right: the fallback", xff: []string{"10.0.0.9, evil"}, want: "fallback"},
		{name: "garbage behind a Google hop: the fallback", xff: []string{"203.0.113.7, evil, 35.191.0.1"}, want: "fallback"},
		{name: "garbage left of the rightmost is not reached", xff: []string{"evil, 10.0.0.9"}, want: "10.0.0.9"},

		// Behind the external Application Load Balancer (hops 2, D240): it appends <client>,<lb-ip>, and
		// Google's front end may add a hop of its own after that.
		{name: "hops 2: forged entries, the client, the LB's entry", hops: 2,
			xff: []string{"1.2.3.4, 5.6.7.8, 198.51.100.20, 34.120.0.5"}, want: "198.51.100.20"},
		{name: "hops 2: forged over two lines", hops: 2, xff: []string{"1.2.3.4", "198.51.100.20, 34.120.0.5"}, want: "198.51.100.20"},
		{name: "hops 2: a Google hop after the LB's entry", hops: 2,
			xff: []string{"203.0.113.7, 198.51.100.20, 34.120.0.5, 35.191.3.4"}, want: "198.51.100.20"},
		{name: "hops 2: an IPv6 client", hops: 2, xff: []string{"2001:db8:9::1, 2001:db8:5:6::1, 34.120.0.5"}, want: "2001:db8:5:6::"},
		{name: "hops 2: one entry is too few: the fallback", hops: 2, xff: []string{"198.51.100.20"}, want: "fallback"},
		{name: "hops 2: one entry and a Google hop is too few", hops: 2, xff: []string{"198.51.100.20, 35.191.3.4"}, want: "fallback"},
		{name: "hops 2: no header: the fallback", hops: 2, want: "fallback"},
		{name: "hops 2: garbage at position 2: the fallback", hops: 2, xff: []string{"203.0.113.7, evil, 34.120.0.5"}, want: "fallback"},
		{name: "hops 2: garbage at position 1: the fallback", hops: 2, xff: []string{"198.51.100.20, evil"}, want: "fallback"},
		{name: "hops 2: garbage left of the client is not reached", hops: 2,
			xff: []string{"evil, <script>, 198.51.100.20, 34.120.0.5"}, want: "198.51.100.20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
			r.RemoteAddr = "198.51.100.9:55555" // never the answer
			for _, l := range tc.xff {
				r.Header.Add("X-Forwarded-For", l)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-Ip", tc.xri)
			}
			if got := picked(NewClientKeys(max(tc.hops, 1)), r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The limiters get a key, never the address: 16 bytes of an HMAC under the process's own key, the same for
// one client within the process, the same for one /64, another for another network and in another process.
// The fallback is one constant every unreadable request shares, and it cannot be a client's key.
func TestClientKeysKey(t *testing.T) {
	k := NewClientKeys(1)
	key := func(k *ClientKeys, xff ...string) string {
		r := httptest.NewRequest(http.MethodGet, "/v1/sync", nil)
		for _, l := range xff {
			r.Header.Add("X-Forwarded-For", l)
		}
		return k.Key(r)
	}
	const addr = "198.51.100.20"
	got := key(k, addr)
	raw := netip.MustParseAddr(addr).AsSlice()
	if len(got) != clientKeySize || strings.Contains(got, addr) || strings.Contains(got, "198.51.100") || bytes.Contains([]byte(got), raw) {
		t.Fatalf("the key %q holds the address or has %d bytes", got, len(got))
	}
	if key(k, addr) != got {
		t.Error("the key is not stable within the process")
	}
	if key(k, "203.0.113.7, 203.0.113.8, "+addr) != got || key(k, "203.0.113.7", addr) != got {
		t.Error("a spoofed left entry changed the key")
	}
	if key(k, "::ffff:"+addr) != got {
		t.Error("an IPv4-mapped address is another key")
	}
	if key(k, "198.51.100.21") == got {
		t.Error("two addresses share a key")
	}
	if a, b := key(k, "2001:db8:1:2::1"), key(k, "2001:db8:1:2:ffff:eeee:dddd:9"); a != b || len(a) != clientKeySize {
		t.Error("two addresses of one /64 do not share the key")
	}
	if key(k, "2001:db8:1:2::1") == key(k, "2001:db8:1:3::1") {
		t.Error("two /64s share a key")
	}
	if key(NewClientKeys(1), addr) == got {
		t.Error("another process's key for the same address is the same: the HMAC key is not random")
	}
	for _, xff := range [][]string{nil, {"evil"}, {"35.191.0.1"}, {" , "}} {
		if f := key(k, xff...); f != fallbackKey {
			t.Errorf("%v: %q, want the fallback key", xff, f)
		}
	}
	if len(fallbackKey) == clientKeySize {
		t.Error("the fallback key has a client key's length: they could meet")
	}
	// Behind the load balancer (hops 2) the client is the entry left of the LB's.
	k2 := NewClientKeys(2)
	if key(k2, "203.0.113.7, "+addr+", 34.120.0.5") != key(k2, addr+", 34.120.0.5") || key(k2, addr) != fallbackKey {
		t.Error("hops 2: a spoofed entry moved the key, or one entry was enough")
	}
	defer func() {
		if recover() == nil {
			t.Error("NewClientKeys(0) did not panic")
		}
	}()
	NewClientKeys(0)
}

// clientAddressKeys is the "client address" line's allowlist: which entry ClientKeys took and how many
// there were (runbook step 9), non-personal counts and a closed set, never an address (D222).
var clientAddressKeys = logcheck.Keys{
	"picked":      logcheck.Enum("xff", "fallback"),
	"xff_index":   logcheck.CountMax(maxForwardedEntries),
	"xff_entries": logcheck.CountMax(maxForwardedEntries),
}

// The proof behind runbook step 9 (design §5.1): what reaches the service when a caller sends three
// forged X-Forwarded-For entries through Google's front end, which appends the address it observed (and
// maybe a hop of its own), or through the load balancer (hops 2), which appends the client and its own
// address. ClientKeys must take the observed address, and the line's xff_index must point at exactly the
// entry it took, so the runbook can read the choice off the line without an address in it. The shapes
// the runbook stops on are here too, with the index it tells them by.
func TestStepNineUnderGoogleFrontEnd(t *testing.T) {
	const forged = "203.0.113.7, 203.0.113.8, 203.0.113.9" // the runbook's curl -H
	for _, tc := range []struct {
		name    string
		hops    int // 0: 1
		xff     []string
		want    string // the picked address, or "fallback"
		picked  string
		index   float64 // xff_index; -1: none on the line
		entries float64
	}{
		{"Google appends the observed address", 0, []string{forged + ", 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"as two header lines", 0, []string{forged, "198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"and a load-balancer hop of its own (35.191.0.0/16)", 0, []string{forged + ", 198.51.100.20, 35.191.3.4"}, "198.51.100.20", "xff", 3, 5},
		{"and two hops (35.191/16, 130.211/22)", 0, []string{forged + ", 198.51.100.20, 35.191.3.4, 130.211.0.9"}, "198.51.100.20", "xff", 3, 6},
		{"an IPv6 caller and an IPv6 hop", 0, []string{forged + ", 2001:db8:5:6::1, 2600:2d00:1:b029::7"}, "2001:db8:5:6::", "xff", 3, 5},
		{"a caller that forges a Google range too", 0, []string{"35.191.0.1, 130.211.0.9, 203.0.113.9, 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},
		{"a caller that forges garbage", 0, []string{"evil, <script>, 203.0.113.9, 198.51.100.20"}, "198.51.100.20", "xff", 3, 4},

		// Behind the external Application Load Balancer (hops 2, D240): <client>,<lb-ip>, maybe a Google hop.
		{"hops 2: the LB appends the client and its own address", 2, []string{forged + ", 198.51.100.20, 34.120.0.5"}, "198.51.100.20", "xff", 3, 5},
		{"hops 2: and a Google hop after the LB's entry", 2, []string{forged + ", 198.51.100.20, 34.120.0.5, 35.191.3.4"}, "198.51.100.20", "xff", 3, 6},
		{"hops 2: an IPv6 caller", 2, []string{forged + ", 2001:db8:5:6::1, 34.120.0.5"}, "2001:db8:5:6::", "xff", 3, 5},
		// Through the load balancer at hops 1 (edge built, and live until the deploy PR; runbook GCP 16.4):
		// the LB's own address is the key (index 4 of 5), one bucket for every client. Expected at built;
		// it is why the deploy PR follows live within the hour, and why check_cloudrun refuses the LB's
		// ingress at hops 1 (D242).
		{"hops 1 behind the load balancer: its own address", 0, []string{forged + ", 198.51.100.20, 34.120.0.5"}, "34.120.0.5", "xff", 4, 5},

		// Stop: a hop of Google's outside the known ranges is picked (index past the forged three, one
		// bucket for every client).
		{"stop: an unknown Google hop", 0, []string{forged + ", 198.51.100.20, 34.96.0.1"}, "34.96.0.1", "xff", 4, 5},
		// Stop: Google appended nothing, the last forged entry wins (index 2 of 3).
		{"stop: nothing appended", 0, []string{forged}, "203.0.113.9", "xff", 2, 3},
		// Stop: hops 2 while the caller reaches Cloud Run's front end directly (ingress all): its forged entry
		// is the key (index 2 of 4). D240: 2 only with ingress internal-and-cloud-load-balancing.
		{"stop: hops 2 without the load balancer", 2, []string{forged + ", 198.51.100.20"}, "203.0.113.9", "xff", 2, 4},
		// No entry would do: the fallback, and no index.
		{"garbage at the right: the fallback", 0, []string{forged + ", unknown"}, "fallback", "fallback", -1, 4},
		{"hops 2: one entry is too few: the fallback", 2, []string{"198.51.100.20"}, "fallback", "fallback", -1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := NewClientKeys(max(tc.hops, 1))
			r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			r.RemoteAddr = "169.254.169.126:41000" // Cloud Run's peer
			for _, l := range tc.xff {
				r.Header.Add("X-Forwarded-For", l)
			}
			if got := picked(k, r); got != tc.want {
				t.Errorf("picked %q, want %q", got, tc.want)
			}
			var buf bytes.Buffer
			c := &clientAddressLog{keys: k, log: logx.NewWriter(&buf, "api", slog.LevelInfo), now: time.Now}
			c.maybe(r)
			lines, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), logcheck.Lines{"client address": clientAddressKeys},
				logcheck.Forbidden{Texts: []string{"203.0.113.", "198.51.100.", "2001:db8:", "169.254.", "35.191.", "130.211.", "34.96.", "34.120.", "2600:2d00:"}})
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
				t.Errorf("a line that picked the fallback carries xff_index %v", idx)
			case tc.index >= 0 && idx != tc.index:
				t.Errorf("xff_index = %v, want %v", idx, tc.index)
			case tc.index >= 0:
				// The index points at the entry ClientKeys took, counted as the line counts.
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
				if at := picked(NewClientKeys(1), r2); at != tc.want {
					t.Errorf("entry %v is %q, ClientKeys took %q", tc.index, at, tc.want)
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
	c := &clientAddressLog{keys: NewClientKeys(1), log: log, now: func() time.Time { return now }}
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
	now = now.Add(time.Minute)
	c.maybe(req("192.0.2.201, garbage"))

	// Every address the requests carried, and their networks: the forged ones, the picked ones, the peer.
	forbid := logcheck.Forbidden{Texts: []string{"203.0.113.", "192.0.2.", "198.51.100.", "2001:db8:", "169.254.", "/24", "/48", "garbage"}}
	lines, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), logcheck.Lines{"client address": clientAddressKeys}, forbid)
	for _, p := range problems {
		t.Error(p)
	}
	got := lines["client address"]
	if len(got) != 3 {
		t.Fatalf("want 3 lines (one a minute), got %d: %s", len(got), buf.String())
	}
	for i, want := range []map[string]any{
		{"xff_entries": float64(2), "picked": "xff", "xff_index": float64(1), "level": "INFO"},
		{"xff_entries": float64(3), "picked": "xff", "xff_index": float64(2), "level": "INFO"},
		{"xff_entries": float64(2), "picked": "fallback", "level": "INFO"},
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

// Instrument samples the line for every service that has it in front (api, ingest), with the process's
// ClientKeys; without them it writes no such line.
func TestInstrumentLogsTheClientAddress(t *testing.T) {
	for _, clients := range []*ClientKeys{NewClientKeys(1), nil} {
		var buf bytes.Buffer
		h := Instrument(logx.NewWriter(&buf, "ingest", slog.LevelInfo), clients, http.HandlerFunc(Live))
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
		if want := map[bool]int{true: 1, false: 0}[clients != nil]; n != want {
			t.Errorf("clients %v: %d client address lines for three requests in a minute, want %d", clients != nil, n, want)
		}
	}
}
