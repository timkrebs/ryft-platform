package scoring

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring/workoutdup"
)

const (
	// dupMargin widens the day for the cross-source decision: a recording that
	// starts at 23:58 and its copy from another source at 00:01 are one
	// workout, and both days' passes must see both to agree. The same margin
	// as the session candidates (strength), so they read the same
	// neighbourhood.
	dupMargin = 12 * time.Hour
	// dupLongest: the store reads workouts by start, so a recording that
	// started before the widened window and runs into it (a 24-h race, a
	// multi-day hike logged as one) is read from this much earlier and kept
	// when it ends inside the window. Workouts up to dupMargin + dupLongest =
	// 48 h long are seen whole; a longer one starting earlier is not part of
	// the decision (it stays shown, as before the rule).
	dupLongest = 36 * time.Hour
	// maxNearWorkouts caps the recordings one day's decision considers. A
	// real day ±12 h has a handful; hundreds mean a broken source or a bulk
	// import, and the pairing is quadratic. Above the cap the day's
	// duplicate_of is cleared (everything shown, as before the rule) and only
	// the count is logged.
	maxNearWorkouts = 200
)

// workoutDuplicates decides the day's cross-source duplicates
// (workoutdup.CrossSourceDuplicates, SCORING.md → workouts) and stores them:
// duplicate_of is set for the day's hidden recordings and cleared for every
// other workout of the day, so a deletion or late heart rate heals on the next
// pass. The decision reads every workout (hidden ones included) that overlaps
// the day ±dupMargin; only the day's own rows (by start) are written, so each
// row has one writer, its day's pass. Heart rate is read in one query over the
// recordings that have a match and counted per recording in memory. It
// returns the day's duplicate → primary.
func (e *Engine) workoutDuplicates(ctx context.Context, userID uuid.UUID, day string, dayStart, dayEnd time.Time) (map[uuid.UUID]uuid.UUID, error) {
	from, to := dayStart.Add(-dupMargin), dayEnd.Add(dupMargin)
	read, err := e.st.WorkoutsBetween(ctx, userID, from.Add(-dupLongest), to)
	if err != nil {
		return nil, err
	}
	near := slices.DeleteFunc(read, func(w model.Workout) bool { return w.End.Before(from) })
	dups := map[uuid.UUID]uuid.UUID{}
	if len(near) > maxNearWorkouts {
		e.log.WarnContext(ctx, "cross-source workout decision skipped: too many workouts near the day",
			"day", day, "workouts", len(near), "cap", maxNearWorkouts)
		return dups, e.st.SetWorkoutDuplicates(ctx, userID, day, dups)
	}

	var matched []model.Workout
	for _, w := range near {
		if workoutdup.HasCrossSourceMatch(w, near) {
			matched = append(matched, w)
		}
	}
	ownHR := map[uuid.UUID]int{}
	if len(matched) > 0 {
		lo, hi := matched[0].Start, WorkoutWindowEnd(matched[0])
		for _, w := range matched[1:] {
			lo, hi = minTime(lo, w.Start), maxTime(hi, WorkoutWindowEnd(w))
		}
		hr, err := e.st.SamplesBetween(ctx, userID, model.SampleHeartRate, lo, hi) // ordered by start
		if err != nil {
			return nil, err
		}
		for _, w := range matched {
			i, _ := slices.BinarySearchFunc(hr, w.Start, func(s model.Sample, t time.Time) int { return s.Start.Compare(t) })
			j, _ := slices.BinarySearchFunc(hr, WorkoutWindowEnd(w), func(s model.Sample, t time.Time) int { return s.Start.Compare(t) })
			ownHR[w.ID] = workoutdup.OwnSourceHR(w, hr[i:j])
		}
	}

	for d, p := range workoutdup.CrossSourceDuplicates(near, ownHR) {
		if s := startOf(near, d); !s.Before(dayStart) && s.Before(dayEnd) {
			dups[d] = p
		}
	}
	if err := e.st.SetWorkoutDuplicates(ctx, userID, day, dups); err != nil {
		return nil, err
	}
	return dups, nil
}

func startOf(ws []model.Workout, id uuid.UUID) time.Time {
	for _, w := range ws {
		if w.ID == id {
			return w.Start
		}
	}
	return time.Time{}
}

// visibleCount is details.workouts: the day's workouts less its hidden
// duplicates (API.md → Duplicate workouts).
func visibleCount(workouts []model.Workout, dups map[uuid.UUID]uuid.UUID) int {
	n := 0
	for _, w := range workouts {
		if _, hidden := dups[w.ID]; !hidden {
			n++
		}
	}
	return n
}
