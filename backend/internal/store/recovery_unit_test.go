package store

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// The tests of this file need no database.

func modelWorkout(start time.Time, minutes float64) model.Workout {
	return model.Workout{Start: start, End: start.Add(time.Duration(minutes * float64(time.Minute)))}
}

// moveFoldedRatingsSQL is recorded in the ApplySync statement trace: a change of the statement is a change of the
// golden file, and this is where a missing regeneration shows without a Postgres (RYFT_UPDATE_TRACE=1 go test
// ./internal/store -run TestApplySyncStatementTrace rewrites it, with a server).
func TestFoldRatingsStatementIsInTheTraceGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/applysync_trace.golden")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(strings.Fields(moveFoldedRatingsSQL), " ") + " [args=3]"
	// The three scenarios whose fold removes a copy: it folds, it folds with deletions, its delete fails.
	if n := strings.Count(string(raw), want); n != 3 {
		t.Fatalf("the rating move appears %d times in the trace golden, want 3 (before each fold delete):\n%s", n, want)
	}
	for _, scenario := range strings.Split(string(raw), "\n## ")[1:] {
		moves := strings.Contains(scenario, want)
		folds := strings.Contains(scenario, "DELETE FROM workouts WHERE user_id=$1 AND id = ANY($2) AND source <> $3 [args=3]")
		if moves != folds {
			t.Errorf("scenario %q: a fold delete without its rating move first (or the reverse)", strings.SplitN(scenario, "\n", 2)[0])
		}
	}
}

func TestMedianClockAcrossMidnight(t *testing.T) {
	hm := func(h, m int) int { return h*60 + m }
	for _, c := range []struct {
		name string
		in   []int
		want int
	}{
		{"evening and night, odd", []int{hm(22, 30), hm(23, 30), hm(0, 40)}, hm(23, 30)},
		{"all after midnight", []int{hm(0, 10), hm(0, 40), hm(1, 30)}, hm(0, 40)},
		{"even takes the middle", []int{hm(23, 0), hm(0, 0)}, hm(23, 30)},
		{"morning wake times", []int{hm(6, 30), hm(6, 45), hm(7, 15), hm(6, 0)}, hm(6, 38)},
		{"one", []int{hm(22, 10)}, hm(22, 10)},
	} {
		if got := medianClock(c.in); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, clockText(got), clockText(c.want))
		}
	}
}

func TestClockMinutesAndText(t *testing.T) {
	at := time.Date(2026, 11, 26, 5, 40, 0, 0, time.UTC)
	for tz, want := range map[int]string{0: "05:40", 3600: "06:40", -3600: "04:40", -6 * 3600: "23:40"} {
		if got := clockText(clockMinutes(at, tz)); got != want {
			t.Errorf("tz %d: %s, want %s", tz, got, want)
		}
	}
	if clockText(1500) != "01:00" {
		t.Errorf("clockText wraps the day: %s", clockText(1500))
	}
}

func TestAddDays(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"2026-11-26", 14, "2026-12-10"}, {"2026-11-26", -14, "2026-11-12"}, {"2026-03-01", -1, "2026-02-28"}, {"2026-12-31", 1, "2027-01-01"}} {
		if got := addDays(c.in, c.n); got != c.want {
			t.Errorf("addDays(%s, %d) = %s, want %s", c.in, c.n, got, c.want)
		}
	}
}

func TestWithRatingIsFostersLoad(t *testing.T) {
	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		minutes float64
		rpe     float64
		want    int
	}{{60, 8, 480}, {45.4, 7, 315}, {60, 3, 180}, {30, 7.5, 225}} {
		w := modelWorkout(start, c.minutes)
		rpe := c.rpe
		withRating(&w, &rpe)
		if w.SessionRPE == nil || *w.SessionRPE != c.rpe || w.SessionRPELoad == nil || *w.SessionRPELoad != c.want {
			t.Errorf("%v min at %v: %v %v, want load %d", c.minutes, c.rpe, w.SessionRPE, w.SessionRPELoad, c.want)
		}
	}
	w := modelWorkout(start, 60)
	withRating(&w, nil)
	if w.SessionRPE != nil || w.SessionRPELoad != nil {
		t.Errorf("an unrated workout carries no rating: %+v", w)
	}
}
