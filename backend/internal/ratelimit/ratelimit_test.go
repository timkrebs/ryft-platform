package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fakeClock(l *Limiter) *time.Time {
	t := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t }
	return &t
}

func TestBurstThenRefill(t *testing.T) {
	l := New(2, 3)
	now := fakeClock(l)
	for k := 0; k < 3; k++ {
		if ok, _ := l.Allow("u"); !ok {
			t.Fatalf("request %d within the burst must pass", k)
		}
	}
	ok, wait := l.Allow("u")
	if ok || wait != 500*time.Millisecond {
		t.Fatalf("4th must wait 500ms, got ok=%v wait=%v", ok, wait)
	}
	*now = now.Add(500 * time.Millisecond)
	if ok, _ := l.Allow("u"); !ok {
		t.Fatal("one token refills after 500ms at 2/s")
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("users have separate buckets")
	}
}

func TestIdleBucketsAreDropped(t *testing.T) {
	l := New(1, 1)
	now := fakeClock(l)
	l.Allow("old")
	*now = now.Add(time.Hour)
	for k := 0; k < 1024; k++ {
		l.Allow("new")
	}
	if _, ok := l.buckets["old"]; ok {
		t.Fatal("an idle bucket must be evicted")
	}
}

func TestUserLimitsClassifiesSync(t *testing.T) {
	u := NewUserLimits()
	if class, _, _ := u.Allow(httptest.NewRequest(http.MethodPost, "/v1/sync", nil), "a"); class != "sync" {
		t.Fatalf("sync class expected, got %s", class)
	}
	if class, _, _ := u.Allow(httptest.NewRequest(http.MethodGet, "/v1/days", nil), "a"); class != "default" {
		t.Fatalf("default class expected, got %s", class)
	}
	if class, _, _ := u.Allow(httptest.NewRequest(http.MethodPut, "/v1/sessions/0b1c", nil), "a"); class != "session" {
		t.Fatalf("session saves have their own class, got %s", class)
	}
	if class, _, _ := u.Allow(httptest.NewRequest(http.MethodGet, "/v1/sessions/0b1c", nil), "a"); class != "default" {
		t.Fatalf("reading a session is a default request, got %s", class)
	}
	// The training import: its own budget, bursts of 5, then one every 5 s; other routes unaffected.
	imp := func() (string, bool, time.Duration) {
		return u.Allow(httptest.NewRequest(http.MethodPost, "/v1/train/import", nil), "imp")
	}
	for k := 0; k < 5; k++ {
		if class, ok, _ := imp(); class != "import" || !ok {
			t.Fatalf("import %d: class %s ok %v", k, class, ok)
		}
	}
	if _, ok, wait := imp(); ok || wait <= 4*time.Second || wait > 5*time.Second {
		t.Fatalf("the sixth import waits about 5 s: ok %v wait %v", ok, wait)
	}
	if _, ok, _ := u.Allow(httptest.NewRequest(http.MethodGet, "/v1/days", nil), "imp"); !ok {
		t.Fatal("the import's budget is its own")
	}
	// A backfill: 60 pages back to back must all pass.
	for k := 0; k < 60; k++ {
		if _, ok, _ := u.Allow(httptest.NewRequest(http.MethodPost, "/v1/sync", nil), "b"); !ok {
			t.Fatalf("backfill page %d was limited", k)
		}
	}
}
