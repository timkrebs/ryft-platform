package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP is the address to key a per-address limit by: the rightmost X-Forwarded-For entry when it
// parses as an IP, otherwise the host of RemoteAddr. IPv6 addresses are masked to their /64, an IPv4-mapped
// one is read as IPv4.
//
// Why the rightmost entry: the api sits behind exactly one proxy, Traefik, which trusts no
// forwarded header from its peer (no forwardedHeaders.trustedIPs): it drops what a client sent and
// appends the peer address it saw, which the NLB keeps as the client's. The rightmost entry is
// therefore the one our own proxy wrote, and anything left of it is the client's claim. The
// api's NetworkPolicy admits only Traefik (and our smoke pod) to the port, so a request cannot
// bypass the proxy. Never use the leftmost entry or X-Real-Ip. The value is a map key in memory:
// do not log it and do not use it as a metric label.
func ClientIP(r *http.Request) string {
	var last string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if part = strings.TrimSpace(part); part != "" {
				last = part
			}
		}
	}
	if a, err := netip.ParseAddr(last); err == nil {
		return normalizeIP(a)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return normalizeIP(a)
	}
	return host
}

func normalizeIP(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().Addr().String()
	}
	return a.String()
}
