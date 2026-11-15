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

// ClientIP is the address to key a per-address limit by: the rightmost X-Forwarded-For
// entry as it is, skipping only Google's front ends (googleFrontEnds); an entry that does
// not parse, or no entry left, gives the host of RemoteAddr. IPv6 addresses are masked to
// their /64, an IPv4-mapped one is read as IPv4.
//
// Why the rightmost: a client can write any X-Forwarded-For it likes, and each proxy in
// front of us appends the address it saw. So the entry our own proxy appended is the
// rightmost one, and everything left of it is the client's claim. On Cloud Run, Google's
// front end appends the address it observed (design §5.1; runbook step 9 checks it with a
// forged header and the "client address" line); behind Traefik (EKS) it is the one Traefik
// appended, as before. A private, shared (CGNAT), link-local or loopback entry is taken as
// it is: a client that reaches Cloud Run from a VPC (Private Google Access, Direct VPC
// egress) arrives as 10.x or a ULA (fc00::/7), and skipping it would make the forged entry
// to its left the key. Only Google's own load-balancer and health-check ranges are a hop to skip.
// Never use the leftmost entry or X-Real-Ip.
//
// The value is a map key in memory and is never a metric label or logged, not even in
// part (D222: no IP address in any log line; the privacy text promises logs without
// them). The sampled "client address" line says only which entry was picked. The admin
// limiter that keys by it caps its map (cmd/api, D155).
func ClientIP(r *http.Request) string {
	if a, _, ok := forwardedClient(r.Header.Values("X-Forwarded-For")); ok {
		return normalizeIP(a)
	}
	return remoteHost(r.RemoteAddr)
}

// forwardedClient walks the entries from the right and returns the first that is not a
// Google front end, with its position counted from the right (1: the rightmost); an
// entry that does not parse ends the walk (false: RemoteAddr). Empty entries are not
// counted, as forwardedEntries does not count them.
func forwardedClient(lines []string) (netip.Addr, int, bool) {
	pos := 0
	for i := len(lines) - 1; i >= 0; i-- {
		parts := strings.Split(lines[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			p := strings.TrimSpace(parts[j])
			if p == "" {
				continue
			}
			pos++
			a, err := netip.ParseAddr(p)
			if err != nil {
				return netip.Addr{}, 0, false
			}
			if a = a.Unmap(); googleFrontEnd(a) {
				continue
			}
			return a, pos, true
		}
	}
	return netip.Addr{}, 0, false
}

// forwardedEntries is how many non-empty entries the X-Forwarded-For lines hold.
func forwardedEntries(lines []string) int {
	n := 0
	for _, l := range lines {
		for _, p := range strings.Split(l, ",") {
			if strings.TrimSpace(p) != "" {
				n++
			}
		}
	}
	return n
}

// googleFrontEnds are the ranges Google's load balancers and health checks connect from
// (Cloud Load Balancing's documented source ranges): a hop of Google's, never a client.
var googleFrontEnds = []netip.Prefix{
	netip.MustParsePrefix("35.191.0.0/16"),
	netip.MustParsePrefix("130.211.0.0/22"),
	netip.MustParsePrefix("2600:2d00:1:b029::/64"),
	netip.MustParsePrefix("2600:2d00:1:1::/64"),
}

// googleFrontEnd reports whether a is in one of Google's front-end ranges.
func googleFrontEnd(a netip.Addr) bool {
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

// maxForwardedEntries caps the entry count and the index the line reports.
const maxForwardedEntries = 64

// clientAddressLog writes the "client address" line for a request that carries
// X-Forwarded-For, at most once per clientAddressEvery per Instrument (one per instance),
// at info. It holds no address, whole or in part (D222), only which entry ClientIP took:
//
//   - picked: "xff" (an X-Forwarded-For entry) or "remote" (none would do: RemoteAddr);
//   - xff_index: with "xff", the picked entry's position from the left, counting from 0,
//     which is how many entries left of it ClientIP ignored (the client's own claim);
//   - xff_entries: how many entries the header had.
//
// It is how runbook step 9 shows that a forged header does not move the limit's key: with
// three forged entries, Google's appended one must be picked, at index 3. An index past
// the forged ones means Google appends a hop of its own that ClientIP doesn't skip; three
// entries and index 2 means Google appended nothing and the forged entry won. It is logged
// without the request's context, so it carries no request id, user or route, and never
// the header itself.
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
	n := forwardedEntries(lines)
	if _, pos, ok := forwardedClient(lines); ok {
		c.log.Info("client address", "picked", "xff", "xff_index", min(n-pos, maxForwardedEntries),
			"xff_entries", min(n, maxForwardedEntries))
		return
	}
	c.log.Info("client address", "picked", "remote", "xff_entries", min(n, maxForwardedEntries))
}
