// Package ratelimit is a small in-memory token-bucket limiter keyed by user.
// Limits are per pod: with N replicas behind the load balancer a user gets up
// to N times the configured rate, which is fine for damping runaway clients;
// anything that needs a global limit (recompute) is gated in the database.
package ratelimit

import (
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

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

// New allows perSecond requests per key on average, with bursts of burst.
func New(perSecond float64, burst int) *Limiter {
	return &Limiter{perSecond: perSecond, burst: float64(burst), idle: 10 * time.Minute,
		now: time.Now, buckets: map[string]*bucket{}}
}

// Allow takes a token for key. When none is left it returns false and how
// long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.calls++
	if l.calls%1024 == 0 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.idle {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perSecond * float64(time.Second))
	return false, wait
}

// UserLimits applies a per-user limit per request class.
type UserLimits struct {
	sync, session, invite, other *Limiter
}

// NewUserLimits returns the platform's limits: uploads may burst through a
// whole 90-day backfill (tens of pages back to back); session saves (the app
// saves after every set and replays its offline queue) get the same budget of
// their own; everything else allows a screen's worth of parallel reads plus
// pull-to-refresh.
func NewUserLimits() *UserLimits {
	// Invite codes: 5 attempts an hour per user (per pod), against guessing.
	return &UserLimits{sync: New(2, 60), session: New(2, 60), invite: New(5.0/3600, 5), other: New(10, 60)}
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
	}
	ok, retryAfter = l.Allow(user)
	return class, ok, retryAfter
}
