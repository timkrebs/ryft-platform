package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// What follows an event's days: a completed session's session.scored, a sync's and an
// import's refresh (D92, D125), never a session.scored for an import.
func TestAnnounce(t *testing.T) {
	user, session := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name    string
		ev      model.IngestedEvent
		session bool
		refresh string // the refresh's day, "" for none
	}{
		{"sync", model.IngestedEvent{Reason: model.ReasonSync, Days: []string{"2026-10-25", "2026-10-26", "2026-10-24"}}, false, "2026-10-26"},
		{"import", model.IngestedEvent{Reason: model.ReasonImport, Days: []string{"2026-06-08", "2026-06-10"}}, false, "2026-06-10"},
		{"import of one day", model.IngestedEvent{Reason: model.ReasonImport, Days: []string{"2026-06-08"}}, false, "2026-06-08"},
		{"import without days", model.IngestedEvent{Reason: model.ReasonImport}, false, ""},
		{"a session completed", model.IngestedEvent{Reason: events.ReasonSessionCompleted, Days: []string{"2026-10-26"}, SessionID: &session}, true, ""},
		{"a session edited", model.IngestedEvent{Reason: "session", Days: []string{"2026-10-26", "2026-10-25"}}, false, ""},
		{"recompute", model.IngestedEvent{Reason: "recompute", Days: []string{"2025-10-01", "2026-10-26"}}, false, ""},
		{"merge", model.IngestedEvent{Reason: "merge", Days: []string{"2025-10-01", "2026-10-26"}}, false, ""},
	} {
		tc.ev.UserID, tc.ev.RequestID = user, "rid"
		s, r := announce(tc.ev)
		if (s != nil) != tc.session || (s != nil && (s.SessionID != session || s.Day != "2026-10-26" || s.UserID != user)) {
			t.Errorf("%s: session.scored %+v", tc.name, s)
		}
		switch {
		case tc.refresh == "" && r != nil:
			t.Errorf("%s: no refresh, got %+v", tc.name, r)
		case tc.refresh != "" && (r == nil || r.Day != tc.refresh || r.UserID != user || r.RequestID != "rid"):
			t.Errorf("%s: refresh for %s, got %+v", tc.name, tc.refresh, r)
		}
	}
}

// Integration test, opt-in like internal/db's: RYFT_TEST_POSTGRES_URL.
func scoringStore(t *testing.T) (*store.Store, *scoring.Engine, uuid.UUID) {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server")
	}
	ctx := context.Background()
	root, err := db.Connect(ctx, admin, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ryft_scoringtest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool).WithImportRules(store.ImportRules{SameSession: train.SameSession, Conflict: train.ImportConflict,
		SamePlan: train.SamePlan})
	user := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	tz := "UTC"
	if _, err := st.UpdateUser(ctx, user, model.UserPatch{TZ: &tz}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "test", nil); err != nil {
		t.Fatal(err)
	}
	return st, scoring.NewEngine(st, quiet), user
}

// An import's event (reason import, two explicit days, no session id) is scored like a
// sync's: both days, not the range between them, the imported sessions get their load
// and workout, the queue rows are marked, and the refresh follows with the newest day,
// never a session.scored. A duplicate and a redelivery change nothing (D100).
func TestImportEventIsScoredLikeASync(t *testing.T) {
	st, engine, user := scoringStore(t)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	squat := train.BuiltinExerciseID("back-squat")
	window := store.DayWindow{From: today.AddDate(0, 0, -399).Format(time.DateOnly), To: today.Format(time.DateOnly)}
	var days []string
	var ids []uuid.UUID
	for _, back := range []int{30, 20} { // two days with nine days between them
		start := today.AddDate(0, 0, -back).Add(17 * time.Hour)
		end := start.Add(time.Hour)
		s := model.Session{ID: uuid.New(), Title: "Lower", StartedAt: start, EndedAt: &end, Status: model.SessionCompleted,
			Exercises: []model.SessionExercise{{ID: uuid.New(), ExerciseID: squat}}}
		for k := 0; k < 5; k++ {
			reps, kg, at := 5, 100.0, start.Add(time.Duration(10+3*k)*time.Minute)
			s.Exercises[0].Sets = append(s.Exercises[0].Sets, model.Set{ID: uuid.New(), SetIndex: k, Reps: &reps, WeightKG: &kg,
				CompletedAt: &at, UpdatedAt: at})
		}
		out, err := st.ImportSession(ctx, user, model.ImportSourceAlphaProgression, s, time.UTC, window)
		if err != nil || out.Status != store.ImportCreated || !out.Queued {
			t.Fatalf("import: %+v %v", out, err)
		}
		days, ids = append(days, out.Day), append(ids, s.ID)
	}
	ev := model.IngestedEvent{UserID: user, Days: days, Reason: model.ReasonImport, RequestID: "rid"}

	if err := handleEvent(ctx, engine, ev); err != nil {
		t.Fatal(err)
	}
	if pending, err := st.PendingDays(ctx, user); err != nil || len(pending) != 0 {
		t.Fatalf("the queued days are scored: %v %v", pending, err)
	}
	scored := func() []model.DailyScore {
		rows, err := st.DailyScores(ctx, user, days[0], days[1])
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := scored()
	var got []string
	for _, d := range rows {
		got = append(got, d.Day)
		if d.Strain == nil || *d.Strain <= 0 || d.StrengthMin == nil || *d.StrengthMin != 60 {
			t.Errorf("%s: strain %v, strength_min %v from the imported session", d.Day, d.Strain, d.StrengthMin)
		}
	}
	if !slices.Equal(got, days) {
		t.Fatalf("the two days and not the range between them: %v, want %v", got, days)
	}
	for _, id := range ids {
		s, err := st.Session(ctx, user, id)
		if err != nil || s.MuscularTRIMP == nil || *s.MuscularTRIMP <= 0 || s.WorkoutID == nil || s.Origin != model.OriginImport {
			t.Fatalf("the imported session's load is scoring's: %+v %v", s, err)
		}
	}
	if session, refresh := announce(ev); session != nil || refresh == nil || refresh.Day != days[1] {
		t.Fatalf("refresh with the newest day, no session.scored: %+v %+v", session, refresh)
	}

	// The same event again, and as a redelivery: nothing changes.
	before := scored()
	if err := handleEvent(ctx, engine, ev); err != nil {
		t.Fatal(err)
	}
	if err := handleEvent(events.WithRedelivery(ctx), engine, ev); err != nil {
		t.Fatal(err)
	}
	after := scored()
	for k := range before {
		b, a := before[k], after[k]
		if *b.Strain != *a.Strain || *b.StrengthMin != *a.StrengthMin {
			t.Fatalf("%s changed on a duplicate: %v/%v → %v/%v", b.Day, *b.Strain, *b.StrengthMin, *a.Strain, *a.StrengthMin)
		}
	}
	if pending, _ := st.PendingDays(ctx, user); len(pending) != 0 {
		t.Fatalf("nothing queued again: %v", pending)
	}
}
