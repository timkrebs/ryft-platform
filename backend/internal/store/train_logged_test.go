package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// LoggedSetsSince is LoggedSets without the sets completed at or before since, except the
// session also's (an e1RM reference's window, D135); LoggedSets keeps all of the history.
func TestLoggedSetsSince(t *testing.T) {
	st, pool, a, b := trainStore(t)
	ctx := context.Background()
	completed := func(user uuid.UUID, start time.Time, kg float64) model.Session {
		s := newSession(squat, lift(0, 5, kg, start.Add(10*time.Minute)), lift(1, 5, kg, start.Add(15*time.Minute)))
		s.StartedAt = start
		if _, err := st.SaveSession(ctx, user, s); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE sessions SET status='completed', ended_at=started_at + interval '1 hour' WHERE user_id=$1 AND id=$2`, user, s.ID); err != nil {
			t.Fatal(err)
		}
		return s
	}
	until := t0.AddDate(0, 0, 100)
	old, inWindow, edge := completed(a, t0, 140), completed(a, t0.AddDate(0, 0, 60), 100), completed(a, t0.AddDate(0, 0, 10), 120)
	completed(b, t0.AddDate(0, 0, 60), 200)             // another user's
	since := t0.AddDate(0, 0, 10).Add(10 * time.Minute) // exactly edge's first set: at or before since is out

	sessionsOf := func(sets []LoggedSet) []uuid.UUID {
		var ids []uuid.UUID
		for _, l := range sets {
			if !slices.Contains(ids, l.SessionID) {
				ids = append(ids, l.SessionID)
			}
		}
		return ids
	}
	all, err := st.LoggedSets(ctx, a, []uuid.UUID{squat}, until, uuid.Nil)
	if err != nil || len(all) != 6 || !slices.Equal(sessionsOf(all), []uuid.UUID{old.ID, edge.ID, inWindow.ID}) {
		t.Fatalf("all of the history, oldest first: %d sets of %v, %v", len(all), sessionsOf(all), err)
	}
	got, err := st.LoggedSetsSince(ctx, a, []uuid.UUID{squat}, since, until, uuid.Nil)
	if err != nil || len(got) != 3 || !slices.Equal(sessionsOf(got), []uuid.UUID{edge.ID, inWindow.ID}) ||
		!got[0].CompletedAt.After(since) {
		t.Fatalf("after since only (edge's second set, the window's two): %d sets of %v, %v", len(got), sessionsOf(got), err)
	}
	// The session also keeps every set, however old.
	got, err = st.LoggedSetsSince(ctx, a, []uuid.UUID{squat}, since, until, old.ID)
	if err != nil || len(got) != 5 || got[0].SessionID != old.ID || got[1].SessionID != old.ID {
		t.Fatalf("the session's own sets are kept: %d sets of %v, %v", len(got), sessionsOf(got), err)
	}
	// until still cuts: nothing after it.
	if got, err := st.LoggedSetsSince(ctx, a, []uuid.UUID{squat}, t0.AddDate(0, 0, -1), t0.AddDate(0, 0, 30), uuid.Nil); err != nil ||
		!slices.Equal(sessionsOf(got), []uuid.UUID{old.ID, edge.ID}) {
		t.Fatalf("until: %v %v", sessionsOf(got), err)
	}
}
