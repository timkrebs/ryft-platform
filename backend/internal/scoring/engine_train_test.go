package scoring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// Integration test, opt-in like internal/db's: RYFT_TEST_POSTGRES_URL.
func engineStore(t *testing.T) (*store.Store, *Engine, uuid.UUID) {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run engine tests")
	}
	ctx := context.Background()
	root, err := db.Connect(ctx, admin, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	name := fmt.Sprintf("ryft_enginetest_%d", rand.Uint32())
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
	st := store.New(pool)
	user := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, user, model.UserPatch{TZ: ptrTo("UTC")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "test", nil); err != nil {
		t.Fatal(err)
	}
	return st, NewEngine(st, quiet), user
}

func ptrTo[T any](v T) *T { return &v }

// A completed session on a day without heart rate: the day gets a strain from
// the muscular load, a workout is created for the session, and when a
// matching HealthKit strength workout syncs later the load moves to it.
func TestEngineScoresStrengthSessions(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	lib := library(t)
	sq, bp, row := lib["back-squat"], lib["bench-press"], lib["barbell-row"]
	day := "2026-10-18"
	b := newBuilder().add(sq, 5, 5, kg(112)).add(bp, 5, 5, kg(80)).add(row, 5, 5, kg(72))
	s := b.s
	s.Status = model.SessionInProgress
	end := start.Add(60 * time.Minute)
	s.EndedAt = &end
	if _, err := st.SaveSession(ctx, user, s); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteSession(ctx, user, s.ID, store.Completion{EndedAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	scores, err := st.DailyScores(ctx, user, day, day)
	if err != nil || len(scores) != 1 {
		t.Fatalf("scores: %v %v", scores, err)
	}
	d := scores[0]
	if d.Strain == nil || *d.Strain < 7 || d.StrengthMin == nil || *d.StrengthMin != 60 || d.VolumeKG == nil || *d.VolumeKG != 6600 {
		t.Fatalf("day without heart rate scored from the session: strain %v min %v volume %v", d.Strain, d.StrengthMin, d.VolumeKG)
	}
	load, _ := st.DayLoadOf(ctx, user, day)
	if ds := DayStrainFrom(load); ds == nil || ds.MuscularTRIMP == 0 || ds.HRMin != 0 {
		t.Fatalf("DayStrain on a session-only day: %+v", ds)
	}
	wid := train.SessionWorkoutID(user, s.ID)
	w, err := st.Workout(ctx, user, wid)
	if err != nil || w.Source != model.SessionSource || w.Strain == nil || *w.Strain != *d.Strain || w.SessionID == nil ||
		w.ModelVersion == nil || *w.ModelVersion != ModelVersion {
		t.Fatalf("created workout: %+v %v", w, err)
	}
	got, _ := st.Session(ctx, user, s.ID)
	if got.WorkoutID == nil || *got.WorkoutID != wid || got.MuscularTRIMP == nil || *got.Strain != *d.Strain {
		t.Fatalf("session load and workout: %+v", got)
	}

	// The watch's strength workout arrives and overlaps the session.
	hk := model.Workout{ID: uuid.New(), Activity: "strength", Start: start.Add(-5 * time.Minute), End: end.Add(2 * time.Minute), Source: "Apple Watch"}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{hk}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Workout(ctx, user, wid); err == nil {
		t.Fatal("the created workout goes once a HealthKit workout carries the load")
	}
	w, err = st.Workout(ctx, user, hk.ID)
	if err != nil || w.SessionID == nil || *w.SessionID != s.ID || w.MuscularLoad == nil {
		t.Fatalf("the HealthKit workout carries the session: %+v %v", w, err)
	}
	got, _ = st.Session(ctx, user, s.ID)
	if *got.WorkoutID != hk.ID {
		t.Fatal("the session points at the HealthKit workout")
	}

	// Deleting the session takes its load off the workout and the day.
	if _, err := st.DeleteSession(ctx, user, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, day); err != nil {
		t.Fatal(err)
	}
	w, _ = st.Workout(ctx, user, hk.ID)
	if w.SessionID != nil || w.MuscularLoad != nil {
		t.Fatalf("detached after deletion: %+v", w)
	}
	scores, _ = st.DailyScores(ctx, user, day, day)
	if scores[0].Strain != nil || scores[0].StrengthMin != nil {
		t.Fatalf("no strain left without heart rate or sessions: %+v", scores[0])
	}
}

// D78: after a withdrawal nothing is scored, neither one day nor a range.
func TestEngineSkipsWithoutConsent(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	if err := engine.ComputeDay(ctx, user, "2026-10-18"); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeDay(ctx, user, "2026-10-19"); !errors.Is(err, ErrNoConsent) {
		t.Fatalf("ComputeDay without consent: %v", err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-10-17", "2026-10-19"); !errors.Is(err, ErrNoConsent) {
		t.Fatalf("ComputeRange without consent: %v", err)
	}
	days, err := st.DailyScores(ctx, user, "2026-10-01", "2026-10-31")
	if err != nil || len(days) != 1 || days[0].Day != "2026-10-18" {
		t.Fatalf("only the day before the withdrawal is scored: %+v %v", days, err)
	}
}
