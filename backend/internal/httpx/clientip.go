package httpx

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ClientKeys is who a request's client is, for the limits keyed by address: the edge limit of api and
// ingest (internal/app, D53) and /v1/admin's (cmd/api, D155). A process makes one from XFF_TRUSTED_HOPS
// and hands the same one to every limit and to Instrument's "client address" line, so they agree.
//
// The client is read from X-Forwarded-For, walked from the right over all its lines (empty entries are not
// counted). An entry in Google's front-end ranges (googleFrontEnds) is a hop of Google's: skipped and not
// counted. The client is the hops-th of the remaining entries, and nothing left of it is ever read. Why
// from the right: a client can write any X-Forwarded-For it likes, and each proxy in front of us appends
// the address it saw, so only the entries our own proxies appended can be trusted, and they are the
// rightmost ones. hops is 1 on Cloud Run (its front end appends the caller's address, D222) and 2 behind
// the external Application Load Balancer, which appends <client>,<lb-ip> (D240). A private, shared (CGNAT),
// link-local or loopback entry is a client as it is: one that reaches Cloud Run from a VPC (Private Google
// Access, Direct VPC egress) arrives as 10.x or a ULA (fc00::/7), and skipping it would make the forged
// entry to its left the key. Only Google's own load-balancer and health-check ranges are a hop to skip.
// Never the leftmost entry or X-Real-Ip. IPv6 is cut to its /64 (one subscriber's network), an
// IPv4-mapped address is read as IPv4, a zone is dropped.
//
// No header, fewer countable entries than hops, or an entry that does not parse before the client's is
// reached (or at its place): the one fallback key, shared by every such request. RemoteAddr is not used.
// That is safe: on Cloud Run every request through Google's front end carries the entry it appended, so
// only a misconfigured hop count or a path to the container that is not Google's front end lands there.
// Those requests share one bucket, so the limit still holds (it fails closed); nothing a client controls
// becomes a key of its own; and RemoteAddr on Cloud Run is Google's front end, a key every client would
// share anyway.
//
// Key is not the address: it is an HMAC-SHA256 of the normalized address under a random key made with the
// ClientKeys, cut to 16 bytes, so no limiter's map holds an address and a key means nothing outside this
// process. No address is logged or a metric label, not even in part (D222: the privacy text promises logs
// without them); the sampled "client address" line says only which entry was picked.
type ClientKeys struct {
	hops   int
	secret [32]byte
}

// clientKeySize is how many bytes of the HMAC a key keeps: 128 bits, no collision in any number of
// addresses a process sees.
const clientKeySize = 16

// fallbackKey is the key of every request whose client cannot be read (ClientKeys): one shared bucket. It
// is shorter than a client's key, so the two never meet.
const fallbackKey = "fallback"

// NewClientKeys makes the process's ClientKeys for hops trusted X-Forwarded-For entries (XFF_TRUSTED_HOPS,
// which config.Load allows from 1 to 8), with a key of its own from crypto/rand. hops below 1 is a
// programming error and panics.
func NewClientKeys(hops int) *ClientKeys {
	if hops < 1 {
		panic(fmt.Sprintf("httpx: NewClientKeys(%d): the client is at least the first entry from the right", hops))
	}
	k := &ClientKeys{hops: hops}
	_, _ = rand.Read(k.secret[:]) // it never returns an error (since Go 1.24 a failure ends the program)
	return k
}

// Key is r's client as a limiter's key (ClientKeys): 16 bytes of an HMAC of its address, or the shared
// fallback key.
func (k *ClientKeys) Key(r *http.Request) string {
	a, _, ok := k.pick(r.Header.Values("X-Forwarded-For"))
	if !ok {
		return fallbackKey
	}
	return k.of(a)
}

// of is a's key: the HMAC of its bytes (4 for IPv4, 16 for IPv6, so the two never meet), cut.
func (k *ClientKeys) of(a netip.Addr) string {
	m := hmac.New(sha256.New, k.secret[:])
	m.Write(a.AsSlice())
	return string(m.Sum(nil)[:clientKeySize])
}

// pick is the client's entry of the X-Forwarded-For lines, normalized, with its position counted from the
// right over every non-empty entry, Google's included (1: the rightmost); false for the fallback. An entry
// that does not parse ends the walk: right of the client's entry only our own proxies write, so garbage
// there means the header is not what we think it is.
func (k *ClientKeys) pick(lines []string) (netip.Addr, int, bool) {
	pos, counted := 0, 0
	for e := range forwardedFromRight(lines) {
		pos++
		a, err := netip.ParseAddr(e)
		if err != nil {
			return netip.Addr{}, 0, false
		}
		if a = a.Unmap(); googleFrontEnd(a) {
			continue
		}
		if counted++; counted == k.hops {
			return normalizeAddr(a), pos, true
		}
	}
	return netip.Addr{}, 0, false
}

// forwardedFromRight yields the non-empty X-Forwarded-For entries, trimmed, the rightmost first, across
// all the header's lines (the last line first). It allocates nothing, so a long forged header costs only
// the entries the walk reaches.
func forwardedFromRight(lines []string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for i := len(lines) - 1; i >= 0; i-- {
			rest := lines[i]
			for {
				j := strings.LastIndexByte(rest, ',')
				if e := strings.TrimSpace(rest[j+1:]); e != "" && !yield(e) {
					return
				}
				if j < 0 {
					break
				}
				rest = rest[:j]
			}
		}
	}
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

// normalizeAddr is a as a key counts it: IPv4-mapped read as IPv4, no zone, IPv6 cut to its /64.
func normalizeAddr(a netip.Addr) netip.Addr {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().Addr()
	}
	return a
}

// clientAddressEvery is how often an instance logs the "client address" line at most.
const clientAddressEvery = time.Minute

// maxForwardedEntries caps the entry count and the index the line reports.
const maxForwardedEntries = 64

// clientAddressLog writes the "client address" line for a request that carries
// X-Forwarded-For, at most once per clientAddressEvery per Instrument (one per instance),
// at info. It holds no address, whole or in part (D222), only which entry ClientKeys took:
//
//   - picked: "xff" (an X-Forwarded-For entry) or "fallback" (none would do: the shared key);
//   - xff_index: with "xff", the picked entry's position from the left, counting from 0,
//     which is how many entries left of it ClientKeys ignored (the client's own claim);
//   - xff_entries: how many entries the header had.
//
// It is how runbook step 9 shows that a forged header does not move the limit's key: with
// three forged entries, the entry Google appended must be picked, at index 3 (behind the
// load balancer, hops 2, too: the LB's own entry is right of it). An index past the forged
// ones means Google appends a hop of its own that ClientKeys doesn't skip; three entries and
// index 2 means nothing was appended and the forged entry won; fallback means fewer entries
// arrived than the hop count expects. It is logged without the request's context, so it
// carries no request id, user or route, and never the header itself.
type clientAddressLog struct {
	keys *ClientKeys
	log  *slog.Logger
	now  func() time.Time

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
	for range forwardedFromRight(lines) {
		n++
	}
	if _, pos, ok := c.keys.pick(lines); ok {
		c.log.Info("client address", "picked", "xff", "xff_index", min(n-pos, maxForwardedEntries),
			"xff_entries", min(n, maxForwardedEntries))
		return
	}
	c.log.Info("client address", "picked", "fallback", "xff_entries", min(n, maxForwardedEntries))
}
