package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestScoreQueueAndSyncState(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	if err := st.QueueDays(ctx, id, []string{"2026-10-20", "2026-10-21"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.PendingDays(ctx, id); len(p) != 2 || p[0] != "2026-10-20" {
		t.Fatalf("pending: %v", p)
	}
	// A computation that started before the queueing does not finish it.
	if _, ok, err := st.MarkScored(ctx, id, "2026-10-20", before); err != nil || ok {
		t.Fatalf("started before: %v %v", ok, err)
	}
	queued, ok, err := st.MarkScored(ctx, id, "2026-10-20", time.Now())
	if err != nil || !ok || queued.Before(before) {
		t.Fatalf("mark: %v %v %v", queued, ok, err)
	}
	if _, ok, _ := st.MarkScored(ctx, id, "2026-10-20", time.Now()); ok {
		t.Fatal("marked once")
	}
	if p, _ := st.PendingDays(ctx, id); len(p) != 1 || p[0] != "2026-10-21" {
		t.Fatalf("pending after: %v", p)
	}
	if err := st.QueueDays(ctx, id, []string{"2026-10-20"}); err != nil { // a new sync touches it again
		t.Fatal(err)
	}
	if p, _ := st.PendingDays(ctx, id); len(p) != 2 {
		t.Fatalf("pending again: %v", p)
	}
	if sec, err := st.ExpectedScoreSec(ctx); err != nil || sec != DefaultExpectedSec {
		t.Fatalf("too few to know: %d %v", sec, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at)
		SELECT $1, d::date, now() - interval '30 seconds', now() - interval '30 seconds' + (n || ' seconds')::interval
		FROM generate_series(1, 10) n, LATERAL (SELECT '2026-09-' || lpad(n::text, 2, '0') AS d) x`, id); err != nil {
		t.Fatal(err)
	}
	if sec, err := st.ExpectedScoreSec(ctx); err != nil || sec != 10 {
		t.Fatalf("p95 of 1..10 s: %d %v", sec, err)
	}

	// The sync state: last device, newest records, pending, the resync request.
	dev := uuid.New()
	if err := st.TouchDevice(ctx, id, dev, "iPhone", nil, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.UpsertSources(ctx, id, []SourceSeen{{Name: "Watch", Kind: model.KindHR, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestResync(ctx, id); err != nil {
		t.Fatal(err)
	}
	s, err := st.SyncStateOf(ctx, id, "2026-10-21")
	if err != nil || s.LastSyncAt == nil || s.LastSyncDevice == nil || *s.LastSyncDevice != "iPhone" ||
		!s.Latest[model.KindHR].Equal(now) || len(s.Pending) != 2 || s.ResyncAt == nil || s.TodayScoredAt != nil {
		t.Fatalf("sync state: %+v %v", s, err)
	}
	if has, _ := st.HasData(ctx, id); !has {
		t.Fatal("has data")
	}

	// Another account on the same device: the hint's source.
	other := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: other, Provider: "apple"}); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.HasData(ctx, other); has {
		t.Fatal("a new account has no data")
	}
	if p, last, found, err := st.OtherAccountOnDevice(ctx, other, dev); err != nil || !found || p != nil || last.IsZero() {
		t.Fatalf("the other account on the device: %v %v %v %v", p, last, found, err)
	}
	if _, _, found, _ := st.OtherAccountOnDevice(ctx, other, uuid.New()); found {
		t.Fatal("an unknown device")
	}
	if err := st.PruneScoreQueue(ctx); err != nil {
		t.Fatal(err)
	}
}

// Identical re-sends are neither rewritten nor counted (ignored.duplicate).
func TestUpsertCountsOnlyChanges(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	s := model.Sample{Type: model.SampleSteps, Start: now, End: now, Value: 10, Source: "iPhone"}
	for k, c := range []struct {
		sample model.Sample
		want   int
	}{{s, 1}, {s, 0}, {model.Sample{Type: s.Type, Start: now, End: now, Value: 11, Source: "iPhone"}, 1}} {
		if n, err := st.UpsertSamples(ctx, id, []model.Sample{c.sample}); err != nil || n != c.want {
			t.Errorf("sample %d: %d %v, want %d", k, n, err, c.want)
		}
	}
	w := model.Workout{ID: uuid.New(), Activity: "running", Start: now.Add(-time.Hour), End: now, Source: "Watch"}
	for k, want := range []int{1, 0} {
		if n, err := st.UpsertWorkouts(ctx, id, []model.Workout{w}); err != nil || n != want {
			t.Errorf("workout %d: %d %v, want %d", k, n, err, want)
		}
	}
	kcal := 300.0
	w.EnergyKcal = &kcal
	if n, _ := st.UpsertWorkouts(ctx, id, []model.Workout{w}); n != 1 {
		t.Errorf("a changed workout counts: %d", n)
	}
}
