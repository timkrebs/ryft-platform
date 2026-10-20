package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestFeedback(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Second)
	app := "0.9.0"
	first, err := st.AddFeedback(ctx, id, model.FeedbackPost{Message: "Recovery 41?", AppVersion: &app,
		RequestIDs: []string{"r1", "r2"}, Diagnostics: json.RawMessage(`{"healthkit_types": 9}`)}, model.FeedbackData)
	if err != nil {
		t.Fatal(err)
	}
	for range FeedbackPerDay - 1 {
		if _, err := st.AddFeedback(ctx, id, model.FeedbackPost{Message: "again"}, model.FeedbackOther); err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.AddFeedback(ctx, id, model.FeedbackPost{Message: "one too many"}, model.FeedbackOther)
	if e, ok := IsTooMuchFeedback(err); !ok || e.RetryAfter < 23*time.Hour || e.RetryAfter > 24*time.Hour {
		t.Fatalf("the 11th report of the day: %v", err)
	}
	other := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(ctx, other, model.FeedbackPost{Message: "mine"}, model.FeedbackIdea); err != nil {
		t.Fatalf("the limit is per user: %v", err)
	}

	list, err := st.FeedbackSince(ctx, start, 100)
	if err != nil || len(list) < 11 {
		t.Fatalf("since: %d %v", len(list), err)
	}
	k := slices.IndexFunc(list, func(f model.Feedback) bool { return f.ID == first })
	f := list[k]
	if f.UserID != id || f.Category != model.FeedbackData || f.Message != "Recovery 41?" || *f.AppVersion != app ||
		!slices.Equal(f.RequestIDs, []string{"r1", "r2"}) || string(f.Diagnostics) != `{"healthkit_types": 9}` || f.ResolvedAt != nil {
		t.Fatalf("stored report: %+v %s", f, f.Diagnostics)
	}
	if list[0].Message != "mine" || list[1].Diagnostics != nil || list[1].RequestIDs == nil {
		t.Fatalf("newest first, no diagnostics is null, no ids is []: %+v", list[:2])
	}
	if later, _ := st.FeedbackSince(ctx, time.Now().Add(time.Minute), 100); len(later) != 0 {
		t.Fatalf("since the future: %d", len(later))
	}

	if err := st.ResolveFeedback(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveFeedback(ctx, first); err != nil {
		t.Fatalf("resolving again is a no-op: %v", err)
	}
	if err := st.ResolveFeedback(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown report: %v", err)
	}

	// The admin view: open reports, the 14 days, no email unless asked.
	if _, err := st.pool.Exec(ctx, `UPDATE users SET email = 'a@example.com' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day, hrv_ms, deep_min, rem_min, calibrating) VALUES
		($1, current_date, 48, 60, 90, false), ($1, current_date - 1, NULL, 0, 0, true), ($1, current_date - 20, 50, 60, 60, true)`, id); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSources(ctx, id, []SourceSeen{{Name: "Watch", Kind: model.KindHR, LastSeen: time.Now()},
		{Name: "Watch", Kind: model.KindSleep, LastSeen: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	for _, emails := range []bool{false, true} {
		users, err := st.AdminUsers(ctx, emails)
		if err != nil {
			t.Fatal(err)
		}
		k := slices.IndexFunc(users, func(u model.AdminUser) bool { return u.ID == id })
		u := users[k]
		if u.ShortID != id.String()[:8] || u.DaysScored14 != 2 || u.NightsStaged14 != 1 || u.DaysHRV14 != 1 ||
			u.Calibrating == nil || *u.Calibrating || u.OpenFeedback != FeedbackPerDay-1 || u.MaxHRSource != model.HRSourceDefault ||
			len(u.Sources) != 1 || !slices.Equal(u.Sources[0].Kinds, []string{model.KindHR, model.KindSleep}) {
			t.Fatalf("admin row: %+v", u)
		}
		if (u.Email != nil) != emails {
			t.Fatalf("emails=%v: email %v", emails, u.Email)
		}
	}
}
