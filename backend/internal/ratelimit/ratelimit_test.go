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
	// A backfill: 60 pages back to back must all pass.
	for k := 0; k < 60; k++ {
		if _, ok, _ := u.Allow(httptest.NewRequest(http.MethodPost, "/v1/sync", nil), "b"); !ok {
			t.Fatalf("backfill page %d was limited", k)
		}
	}
}
