package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestAdminOnly(t *testing.T) {
	key := strings.Repeat("k", 40)
	ok := adminOnly(key, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for auth, want := range map[string]int{"Bearer " + key: 204, "Bearer " + key + "x": 401, "Bearer ": 401, "": 401,
		key: 401, "Basic " + key: 401} {
		r := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		ok.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Authorization %q: %d, want %d", auth, rec.Code, want)
		}
	}
}

// The seeded flags, an admin change and the evaluation for two users.
func TestRemoteConfig(t *testing.T) {
	st := store.New(testPool(t))
	ctx := context.Background()
	h := &handler{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, b := uuid.New(), uuid.New()
	ua, _ := st.EnsureUser(ctx, store.Identity{ID: a})
	ub, _ := st.EnsureUser(ctx, store.Identity{ID: b})
	get := func(u model.User) model.RemoteConfig {
		t.Helper()
		rec := httptest.NewRecorder()
		h.remoteConfig(rec, asUser(httptest.NewRequest(http.MethodGet, "/v1/config", nil), u, ""))
		var c model.RemoteConfig
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
			t.Fatalf("GET /v1/config: %d %s", rec.Code, rec.Body)
		}
		return c
	}
	c := get(ua)
	want := map[string]bool{"train.enabled": true, "sleep.planner": true, "today.redesign": false,
		"coach.enabled": false, "push.enabled": false, "physiology.card": false, "train.import": false}
	if c.TTLSec != 21600 || len(c.Flags) != len(want) {
		t.Fatalf("config %+v", c)
	}
	for k, v := range want {
		if c.Flags[k] != v {
			t.Errorf("seeded %s = %v, want %v", k, c.Flags[k], v)
		}
	}

	put := func(key, body string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/v1/admin/flags/"+key, strings.NewReader(body))
		r.SetPathValue("key", key)
		rec := httptest.NewRecorder()
		admin{h: h}.putFlag(rec, r)
		return rec.Code
	}
	if code := put("today.redesign", `{"enabled": true, "rollout_pct": 0, "allow_user_ids": ["`+b.String()+`"]}`); code != 200 {
		t.Fatalf("update: %d", code)
	}
	if get(ua).Flags["today.redesign"] || !get(ub).Flags["today.redesign"] {
		t.Fatal("the allow-list turns it on for b only")
	}
	if code := put("check.new_flag", `{"enabled": true, "rollout_pct": 100}`); code != 201 || !get(ua).Flags["check.new_flag"] {
		t.Fatalf("create: %d", code)
	}
	for key, body := range map[string]string{"Bad.Key": `{"enabled": true, "rollout_pct": 0}`,
		"a.b": `{"enabled": true}`, "a.c": `{"enabled": true, "rollout_pct": 101}`} {
		if code := put(key, body); code != 422 {
			t.Errorf("PUT %s %s: %d, want 422", key, body, code)
		}
	}
	if code := put("a.d", `{"enabled": true, "rollout_pct": 5, "extra": 1}`); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	r := httptest.NewRequest(http.MethodDelete, "/v1/admin/flags/check.new_flag", nil)
	r.SetPathValue("key", "check.new_flag")
	rec := httptest.NewRecorder()
	admin{h: h}.deleteFlag(rec, r)
	if _, still := get(ua).Flags["check.new_flag"]; rec.Code != 204 || still {
		t.Fatalf("delete: %d", rec.Code)
	}
}
