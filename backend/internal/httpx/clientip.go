package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ClientIP is the address to key a per-address limit by: walking X-Forwarded-For from the
// right, the first entry that is a public address and not one of Google's front ends';
// otherwise the host of RemoteAddr. IPv6 addresses are masked to their /64, an IPv4-mapped
// one is read as IPv4.
//
// Why from the right: a client can write any X-Forwarded-For it likes, and each proxy in
// front of us appends the address it saw. So the entries a proxy of ours appended are the
// rightmost ones, and everything left of them is the client's claim. On Cloud Run, Google's
// front end appends the address it observed (design §5.1; runbook step 9 checks it with a
// forged header and the "client address" line). Behind Traefik (EKS) the rightmost entry is
// the one Traefik appended, as before. What the walk skips can only be a hop of the
// infrastructure (Google's load-balancer and health-check ranges, private, shared,
// link-local, loopback), never a client on the internet; an entry that does not parse ends
// the walk at RemoteAddr. Never use the leftmost entry or X-Real-Ip.
//
// The value is a map key in memory and is never a metric label. The only line that logs it
// is the sampled "client address" line (Instrument; at most one a minute per instance). The
// admin limiter that keys by it caps its map (cmd/api, D155).
func ClientIP(r *http.Request) string {
	if a, ok := forwardedClient(r.Header.Values("X-Forwarded-For")); ok {
		return normalizeIP(a)
	}
	return remoteHost(r.RemoteAddr)
}

// forwardedClient walks the entries from the right and returns the first client address.
func forwardedClient(lines []string) (netip.Addr, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		parts := strings.Split(lines[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			p := strings.TrimSpace(parts[j])
			if p == "" {
				continue
			}
			a, err := netip.ParseAddr(p)
			if err != nil {
				return netip.Addr{}, false
			}
			if a = a.Unmap(); infrastructure(a) {
				continue
			}
			return a, true
		}
	}
	return netip.Addr{}, false
}

// googleFrontEnds are the ranges Google's load balancers and health checks connect from
// (Cloud Load Balancing's documented source ranges): a hop of Google's, never a client.
var googleFrontEnds = []netip.Prefix{
	netip.MustParsePrefix("35.191.0.0/16"),
	netip.MustParsePrefix("130.211.0.0/22"),
	netip.MustParsePrefix("2600:2d00:1:b029::/64"),
	netip.MustParsePrefix("2600:2d00:1:1::/64"),
}

// sharedAddressSpace is 100.64.0.0/10 (RFC 6598, carrier-grade NAT): not routable on the internet.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// infrastructure reports whether a is an address no client on the internet has: not global
// unicast (loopback, link-local, multicast, unspecified), private, shared, or Google's front
// end.
func infrastructure(a netip.Addr) bool {
	if !a.IsGlobalUnicast() || a.IsPrivate() || sharedAddressSpace.Contains(a) {
		return true
	}
	for _, p := range googleFrontEnds {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// remoteHost is RemoteAddr's host, normalized when it is an address.
func remoteHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
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

// clientAddressEvery is how often an instance logs the "client address" line at most.
const clientAddressEvery = time.Minute

// maxForwardedEntries caps the entry count the line reports.
const maxForwardedEntries = 64

// clientAddressLog writes the "client address" line for a request that carries
// X-Forwarded-For, at most once per clientAddressEvery per Instrument (one per instance):
// how many entries the header had, the address ClientIP picked and RemoteAddr's host. It is
// how runbook step 9 shows that a forged header does not move the limit's key (the picked
// address must be the caller's, not the forged one and not Google's). It is logged without
// the request's context, so it carries no request id, user or route, and never the header
// itself: the forged entries are the client's claim.
type clientAddressLog struct {
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	last time.Time
}

func (c *clientAddressLog) maybe(r *http.Request) {
	lines := r.Header.Values("X-Forwarded-For")
	if len(lines) == 0 {
		return
	}
	c.mu.Lock()
	now := c.now()
	due := c.last.IsZero() || now.Sub(c.last) >= clientAddressEvery
	if due {
		c.last = now
	}
	c.mu.Unlock()
	if !due {
		return
	}
	n := 0
	for _, l := range lines {
		for _, p := range strings.Split(l, ",") {
			if strings.TrimSpace(p) != "" {
				n++
			}
		}
	}
	c.log.Info("client address", "xff_entries", min(n, maxForwardedEntries), "client_ip", ClientIP(r), "remote_host", remoteHost(r.RemoteAddr))
}
