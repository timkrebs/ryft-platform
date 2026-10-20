package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

type fakeBus struct {
	mu     sync.Mutex
	events []model.IngestedEvent
}

func (f *fakeBus) PublishIngested(_ context.Context, ev model.IngestedEvent, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeBus) PublishSessionCompleted(context.Context, model.SessionCompletedEvent, string) error {
	return nil
}

// D90 through the admin chain: the plan first, the wrong way round refused, the
// merge with its rescore and the deletion of from, then 404; audited.
func TestAdminMerge(t *testing.T) {
	h, to, st := journalHandler(t)
	ctx := context.Background()
	bus := &fakeBus{}
	h.bus, h.accounts = bus, &accounts{st: st, log: h.log}
	from := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: from, Provider: "apple"}); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().AddDate(0, 0, -3)
	if _, err := st.UpsertSamples(ctx, from, []model.Sample{{Type: model.SampleSteps, Start: day, End: day, Value: 5, Source: "iPhone"}}); err != nil {
		t.Fatal(err)
	}
	key := "test-admin-key-0123456789abcdef0123456789"
	srv := httpx.Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), adminRoutes(h, key, 30*time.Second))
	post := func(path string) (int, MergeAnswer, string) {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		var a MergeAnswer
		_ = json.Unmarshal(rec.Body.Bytes(), &a)
		return rec.Code, a, rec.Body.String()
	}
	base := "/v1/admin/users/" + from.String()[:8] + "/merge-into/" + to.ID.String()[:8]
	code, plan, body := post(base + "?dry_run=1")
	if code != 200 || !plan.DryRun || plan.Deleted != nil || plan.Rescore == nil || len(plan.Tables) == 0 {
		t.Fatalf("dry run: %d %s", code, body)
	}
	for _, tb := range plan.Tables { // from has one sample and nothing else
		if want := map[string]int64{"samples": 1}[tb.Table]; tb.Moved != want || tb.Dropped != 0 {
			t.Errorf("plan %s: moved %d dropped %d, want %d and 0", tb.Table, tb.Moved, tb.Dropped, want)
		}
	}
	if n, _ := st.ScoredDays(ctx, to.ID); n != 0 || len(bus.events) != 0 {
		t.Fatal("a dry run queues nothing")
	}
	if _, _, err := st.GetUser(ctx, from); err != nil {
		t.Fatalf("a dry run keeps from: %v", err)
	}
	// from with more scored days than to: refused unless forced.
	if _, err := st.UpsertDailyScore(ctx, from, store.ScoreRow{DailyScore: model.DailyScore{Day: day.Format(dayLayout)}}); err != nil {
		t.Fatal(err)
	}
	if code, _, body := post(base); code != 409 || !strings.Contains(body, "force=1") {
		t.Fatalf("the wrong way round: %d %s", code, body)
	}
	code, done, body := post(base + "?force=1")
	if code != 200 || done.DryRun || done.Deleted == nil || done.Deleted.Status != "deleted" || done.Rescore == nil ||
		done.Rescore.From != day.In(time.UTC).Format(dayLayout) {
		t.Fatalf("merge: %d %s", code, body)
	}
	if len(bus.events) != 1 || bus.events[0].UserID != to.ID || bus.events[0].Reason != "merge" || len(bus.events[0].Days) != 2 {
		t.Fatalf("the rescore of to: %+v", bus.events)
	}
	moved, err := st.SamplesBetween(ctx, to.ID, model.SampleSteps, day.Add(-time.Hour), day.Add(time.Hour))
	if err != nil || len(moved) != 1 {
		t.Fatalf("the sample moved: %d %v", len(moved), err)
	}
	if _, _, err := st.GetUser(ctx, from); err == nil {
		t.Fatal("from is deleted")
	}
	if code, _, body := post(base + "?force=1"); code != 404 || !strings.Contains(body, "not found") {
		t.Fatalf("a second merge: %d %s", code, body)
	}
	audit, _ := st.AuditSince(ctx, time.Now().Add(-time.Minute), 20)
	var merged bool
	for _, e := range audit {
		var det map[string]any
		_ = json.Unmarshal(e.Details, &det)
		if e.Action == "POST /v1/admin/users/{id}/merge-into/{to}" && e.Status == 200 && det["dry_run"] == false &&
			det["into"] == to.ID.String() && e.TargetUser != nil && *e.TargetUser == from {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("the merge is audited: %+v", audit)
	}
}
