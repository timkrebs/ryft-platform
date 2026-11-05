package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The address a per-address limit keys by (design 2026-11-05-admin-web §4.6): the rightmost
// X-Forwarded-For entry, the one our own proxy appended. A client's own X-Forwarded-For claim sits
// to its left and is ignored.
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
		{"a zone is dropped", []string{"fe80::1%eth0"}, "", "10.0.0.5:1", "fe80::"},
		{"IPv6 peer", nil, "", "[2001:db8::7]:443", "2001:db8::"},
		{"a peer without a port", nil, "", "198.51.100.9", "198.51.100.9"},
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
