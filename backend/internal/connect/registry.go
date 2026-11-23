package connect

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// Registry is the providers a service serves, by name. The api holds the whole Provider (its OAuth half);
// ingest holds a PushRegistry (Registry.PushSide) and so cannot reach a token on any path.
type Registry struct {
	m     map[string]Provider
	names []string // sorted
}

// NewRegistry is the registry of ps. It refuses a malformed or repeated name.
func NewRegistry(ps ...Provider) (*Registry, error) {
	r := &Registry{m: map[string]Provider{}}
	for _, p := range ps {
		n := p.Name()
		switch {
		case !ValidProvider(n) || n == ReservedProvider:
			return nil, fmt.Errorf("connect: %q is not a provider id", n)
		case r.m[n] != nil:
			return nil, fmt.Errorf("connect: provider %q twice", n)
		}
		r.m[n] = p
		r.names = append(r.names, n)
	}
	slices.Sort(r.names)
	return r, nil
}

// Get is the served provider of that name.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.m[name]
	return p, ok
}

// Names are the served providers, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	return slices.Clone(r.names)
}

// DisplayName is the name the app shows for a provider, also one that is not served now (a connection outlives
// its provider's flag): the id with its first letter upper-cased.
func (r *Registry) DisplayName(name string) string {
	if p, ok := r.Get(name); ok {
		return p.DisplayName()
	}
	if !ValidProvider(name) {
		return name
	}
	return string(name[0]-'a'+'A') + name[1:]
}

// PushSide is the registry ingest holds: the push halves only. Each value is wrapped (pushOnly), so a type
// assertion on what Get returns reaches neither OAuth nor Provider: the tokens' half is not on any path of ingest
// (P2-7).
func (r *Registry) PushSide() *PushRegistry {
	out := &PushRegistry{m: map[string]PushSide{}}
	if r != nil {
		for n, p := range r.m {
			out.m[n] = pushOnly{p: p}
		}
		out.names = slices.Clone(r.names)
	}
	return out
}

// pushOnly exposes the push methods of a provider and nothing else: its dynamic type has no OAuth method and
// holds the provider in an unexported field.
type pushOnly struct{ p PushSide }

func (w pushOnly) MaxPushBytes() int64 { return w.p.MaxPushBytes() }
func (w pushOnly) VerifyPush(h http.Header, body []byte, now time.Time) error {
	return w.p.VerifyPush(h, body, now)
}
func (w pushOnly) SplitPush(body []byte) ([]PushItem, error) { return w.p.SplitPush(body) }
func (w pushOnly) ParseRecord(kind string, payload json.RawMessage) (Records, error) {
	return w.p.ParseRecord(kind, payload)
}
func (w pushOnly) Required() []string { return w.p.Required() }

// PushRegistry is a registry of PushSide values: nothing in it can refresh, exchange or deregister.
type PushRegistry struct {
	m     map[string]PushSide
	names []string
}

// Get is the served provider's push half.
func (r *PushRegistry) Get(name string) (PushSide, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.m[name]
	return p, ok
}

// Names are the served providers, sorted.
func (r *PushRegistry) Names() []string {
	if r == nil {
		return nil
	}
	return slices.Clone(r.names)
}
