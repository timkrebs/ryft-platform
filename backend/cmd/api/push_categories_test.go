package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// PATCH /v1/me/notifications knows ACTIVITY_SCORED (a category is a closed set, model.NotifyCategories): the
// 422 for an unknown one names all five, from that list. Nothing here needs a database: the unknown
// category is refused before the store is asked, and a known one reaches it (a closed pool: 500, never 422).
func TestPatchNotificationsKnowsActivityScored(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	srv, _ := nulServer(t, store.New(pool), model.User{ID: uuid.New(), TZ: "UTC"})

	rec := callNUL(srv, "PATCH", "/v1/me/notifications", `{"categories": {"ACTIVITY_SCORE": false}}`)
	want := "unknown category ACTIVITY_SCORE (MORNING_SCORE, SESSION_SCORED, PR, SYNC_STALE, ACTIVITY_SCORED)"
	if rec.Code != http.StatusUnprocessableEntity || errorBody(t, rec) != want {
		t.Fatalf("an unknown category: %d %s, want 422 %q", rec.Code, rec.Body, want)
	}
	for _, c := range model.NotifyCategories {
		if !strings.Contains(want, c) {
			t.Errorf("the 422 text omits %s", c)
		}
		rec := callNUL(srv, "PATCH", "/v1/me/notifications", `{"categories": {"`+c+`": false}}`)
		if rec.Code == http.StatusUnprocessableEntity || rec.Code == http.StatusBadRequest {
			t.Errorf("%s is refused: %d %s", c, rec.Code, rec.Body)
		}
	}
}

func TestNotifyCategoriesIncludeActivityScored(t *testing.T) {
	last := model.NotifyCategories[len(model.NotifyCategories)-1]
	if last != model.NotifyActivityScored || model.NotifyActivityScored != "ACTIVITY_SCORED" {
		t.Fatalf("the display order ends with %q, the API's table order", last)
	}
	// The handler itself, without the server: an unknown category before any store call.
	rec := httptest.NewRecorder()
	(&handler{}).patchNotifications(rec, httptest.NewRequest(http.MethodPatch, "/v1/me/notifications", strings.NewReader(`{"categories": {"X": true}}`)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
