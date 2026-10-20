package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestUserSources(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	watch, ring := "com.apple.health.81E2", "com.ouraring.oura"
	sync := func(samples []model.Sample, sleep []model.SleepSegment) {
		t.Helper()
		if err := st.UpsertSources(ctx, id, SourcesOf(samples, sleep, nil)); err != nil {
			t.Fatal(err)
		}
	}
	sync([]model.Sample{
		{Type: model.SampleHeartRate, Start: now.Add(-2 * time.Hour), End: now.Add(-2 * time.Hour), Source: "Watch", SourceBundleID: &watch},
		{Type: model.SampleSteps, Start: now.Add(-3 * time.Hour), End: now.Add(-3 * time.Hour), Source: "Watch"},
		{Type: model.SampleVO2Max, Start: now, End: now, Source: "Watch"},
		{Type: model.SampleSteps, Start: now.Add(-40 * 24 * time.Hour), End: now.Add(-40 * 24 * time.Hour), Source: "Old phone"},
		{Type: model.SampleSteps, Start: now, End: now, Source: ""},
	}, []model.SleepSegment{{Start: now.Add(-9 * time.Hour), End: now.Add(-time.Hour), Stage: "deep", Source: "Oura", SourceBundleID: &ring}})
	// An older page later: last_seen stays, the bundle id stays.
	sync([]model.Sample{{Type: model.SampleHeartRate, Start: now.Add(-5 * time.Hour), End: now.Add(-5 * time.Hour), Source: "Watch"}}, nil)

	got, err := st.UserSources(ctx, id, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Watch" || got[1].Name != "Oura" {
		t.Fatalf("newest first, the old phone and the nameless source left out: %+v", got)
	}
	w, o := got[0], got[1]
	if !slices.Equal(w.Kinds, []string{model.KindHR, model.KindSteps}) || w.BundleID == nil || *w.BundleID != watch ||
		!w.LastSeen.Equal(now) {
		t.Fatalf("watch: %+v (vo2max counts as hr, the latest record is now)", w)
	}
	if !slices.Equal(o.Kinds, []string{model.KindSleep}) || o.BundleID == nil || *o.BundleID != ring || !o.LastSeen.Equal(now.Add(-time.Hour)) {
		t.Fatalf("ring: %+v", o)
	}
	if week, _ := st.UserSources(ctx, id, now.Add(-30*time.Minute)); len(week) != 1 || week[0].Name != "Watch" {
		t.Fatalf("since a time: %+v", week)
	}
	if empty, err := st.UserSources(ctx, uuid.New(), now.Add(-time.Hour)); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no sources is an empty list, not null: %v %v", empty, err)
	}

	// The device row keeps the app and OS version; nil keeps the stored one.
	dev := uuid.New()
	app, osv := "0.9.0 (41)", "iOS 26.1"
	if err := st.TouchDevice(ctx, id, dev, "phone", &app, &osv); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchDevice(ctx, id, dev, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	var gotApp, gotOS string
	if err := pool.QueryRow(ctx, `SELECT app_version, os_version FROM devices WHERE user_id=$1 AND id=$2`, id, dev).
		Scan(&gotApp, &gotOS); err != nil || gotApp != app || gotOS != osv {
		t.Fatalf("device versions: %q %q %v", gotApp, gotOS, err)
	}
}
