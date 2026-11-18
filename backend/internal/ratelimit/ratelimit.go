package ratelimit

import (
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultIdle is how long a bucket may go unused before it is dropped, unless Idle says otherwise.
const defaultIdle = 10 * time.Minute

// sweepEvery is how many calls of a Limiter pass between two sweeps of its idle buckets.
const sweepEvery = 1024

// Limiter holds one token bucket per key.
type Limiter struct {
	perSecond float64
	burst     float64
	idle      time.Duration // buckets unused this long are dropped
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Option sets one of a Limiter's defaults (New, NewCapped).
type Option func(*Limiter)

// Idle drops a bucket unused for d instead of 10 minutes. A bucket is full again after burst/perSecond
// seconds, so any d beyond that loses nothing; a shorter one keeps fewer keys in memory.
func Idle(d time.Duration) Option { return func(l *Limiter) { l.idle = d } }

// Clock reads the time from now instead of time.Now (tests).
func Clock(now func() time.Time) Option { return func(l *Limiter) { l.now = now } }

// New allows perSecond requests per key on average, with bursts of burst.
func New(perSecond float64, burst int, opts ...Option) *Limiter {
	l := &Limiter{perSecond: perSecond, burst: float64(burst), idle: defaultIdle, now: time.Now, buckets: map[string]*bucket{}}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Allow takes a token for key. When none is left it returns false and how
// long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.count(now)
	b, ok := l.buckets[key]
	if !ok {
		b = l.add(key, now)
	}
	return l.take(b, now)
}

// count counts a call and sweeps the idle buckets every sweepEvery calls. l.mu is held.
func (l *Limiter) count(now time.Time) {
	if l.calls++; l.calls%sweepEvery == 0 {
		l.sweep(now)
	}
}

// sweep drops every bucket unused for longer than the idle time. l.mu is held.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > l.idle {
			delete(l.buckets, k)
		}
	}
}

// add makes key's bucket, full. l.mu is held.
func (l *Limiter) add(key string, now time.Time) *bucket {
	b := &bucket{tokens: l.burst, last: now}
	l.buckets[key] = b
	return b
}

// take refills b for the time since its last request and takes a token from it. l.mu is held.
func (l *Limiter) take(b *bucket, now time.Time) (bool, time.Duration) {
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perSecond * float64(time.Second))
	return false, wait
}

// Capped is a Limiter that keeps a bucket for at most a fixed number of keys: a key it knows keeps its own
// bucket; a new one gets one while fewer than the cap are known (a key unused for the idle time is
// forgotten, swept at most once a second once the cap is reached); beyond that every new key shares one
// overflow bucket. So a flood from many keys gets one key's rate together (it fails closed for the
// newcomers) while the known keys are not affected, and memory stays bounded however many keys arrive.
// It is the per-address limit's (the edge's in internal/app, D53; /v1/admin's in cmd/api, D155), where a
// client chooses its key: one IPv6 /48 holds 65,536 /64s.
//
// The overflow bucket lives outside the map, so the map holds at most the cap and no key of a caller can
// be the overflow bucket. There is one map and one idle time: nothing outlives it.
type Capped struct {
	lim *Limiter
	max int

	// Under lim.mu.
	overflow bucket
	swept    time.Time // the last sweep a full map triggered
}

// NewCapped is New for at most capacity keys at a time.
func NewCapped(perSecond float64, burst, capacity int, opts ...Option) *Capped {
	l := New(perSecond, burst, opts...)
	return &Capped{lim: l, max: capacity, overflow: bucket{tokens: l.burst, last: l.now()}}
}

// Allow takes a token for key's bucket, or the overflow bucket's when key is new and the cap is reached.
// When none is left it returns false and how long until one is.
func (c *Capped) Allow(key string) (bool, time.Duration) {
	l := c.lim
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.count(now)
	if b, ok := l.buckets[key]; ok {
		return l.take(b, now)
	}
	if len(l.buckets) >= c.max && now.Sub(c.swept) >= time.Second {
		c.swept = now
		l.sweep(now)
	}
	if len(l.buckets) >= c.max {
		return l.take(&c.overflow, now)
	}
	return l.take(l.add(key, now), now)
}

// UserLimits applies a per-user limit per request class.
type UserLimits struct {
	sync, session, invite, imports, other *Limiter
}

// NewUserLimits returns the platform's limits: uploads may burst through a
// whole 90-day backfill (tens of pages back to back); session saves (the app
// saves after every set and replays its offline queue) get the same budget of
// their own; everything else allows a screen's worth of parallel reads plus
// pull-to-refresh.
func NewUserLimits() *UserLimits {
	// Invite codes: 5 attempts an hour per user (per pod), against guessing. The training
	// import (each request up to 50 sessions and a few seconds of database work): bursts of 5,
	// then one every 5 seconds (an app sends its batches one after another, S2).
	return &UserLimits{sync: New(2, 60), session: New(2, 60), invite: New(5.0/3600, 5), imports: New(0.2, 5), other: New(10, 60)}
}

// Allow classifies r and takes a token from user's bucket for that class.
func (u *UserLimits) Allow(r *http.Request, user string) (class string, ok bool, retryAfter time.Duration) {
	class, l := "default", u.other
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/sync":
		class, l = "sync", u.sync
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		class, l = "session", u.session
	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/invite":
		class, l = "invite", u.invite
	case r.Method == http.MethodPost && r.URL.Path == "/v1/train/import":
		class, l = "import", u.imports
	}
	ok, retryAfter = l.Allow(user)
	return class, ok, retryAfter
}
