package scoring

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestEngineScoresActiveMinutesWithVisibleWorkouts(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	const day = "2026-11-27"
	seed := stepSeed{t: t, st: st, user: user, loc: time.UTC}
	seed.samples(model.SampleHeartRate, "count/min", 160, day+" 08:00", day+" 08:03")
	seed.workoutAvg("running", 150, day+" 10:00", day+" 10:30")
	assertScore := func() {
		t.Helper()
		if _, err := engine.computeDay(ctx, user, day); err != nil {
			t.Fatal(err)
		}
		rows, err := st.DailyScores(ctx, user, day, day)
		if err != nil || len(rows) != 1 {
			t.Fatalf("read daily score: %d rows, %v", len(rows), err)
		}
		checkFitnessTile(t, "active_min", rows[0].ActiveMin, ptrTo(33))
	}
	assertScore()

	// The generic recording extends the interval on both ends, so taking its
	// union with the visible run would add four extra minutes.
	hidden := model.Workout{ID: uuid.New(), Activity: "other", Source: "phone",
		Start: seed.at(day + " 09:58"), End: seed.at(day + " 10:32")}
	if _, err := st.UpsertWorkouts(ctx, user, []model.Workout{hidden}); err != nil {
		t.Fatal(err)
	}
	assertScore()
	visible, err := st.VisibleWorkoutsBetween(ctx, user, seed.at(day+" 00:00"), seed.at("2026-11-28 00:00"))
	if err != nil || len(visible) != 1 || visible[0].ID != seed.workout {
		t.Fatalf("want only the specific workout visible, got %v, %v", visible, err)
	}
}
