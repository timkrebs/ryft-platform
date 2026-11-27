package store

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
)

// MuscleSet is a working set with the muscles its exercise loads (D309).
type MuscleSet struct {
	CompletedAt        time.Time
	Kind               string // the exercise's kind: a cardio set weighs less (scoring.MuscleFreshnessAt)
	Primary, Secondary []string
}

// MuscleHistory is what DayDetail.muscles is computed from (D309).
type MuscleHistory struct {
	HasCompletedSession bool                 // any completed session, at any time
	Sets                []MuscleSet          // working sets completed in (since, until), oldest first (by completion time)
	Last                map[string]time.Time // per muscle, the latest working set before until, all-time
}

// muscleHistorySQL reads the account's working sets once (the CTE is referenced twice, so Postgres
// materializes it): a working set is completed, not a warm-up, in a completed session, on an exercise the
// user can see. The rows are the recent sets (kind 's'), the latest time per muscle (kind 'm', the muscle
// in primary_muscles[1]) and one row 'c' when the account has completed a session.
const muscleHistorySQL = `WITH work AS (
		SELECT st.id, st.completed_at, x.kind, x.primary_muscles, x.secondary_muscles
		FROM session_sets st
		JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		JOIN sessions s ON s.user_id = e.user_id AND s.id = e.session_id
		JOIN exercises x ON x.id = e.exercise_id AND (x.user_id IS NULL OR x.user_id = $1)
		WHERE st.user_id = $1 AND s.status = 'completed' AND NOT st.is_warmup
		  AND st.completed_at IS NOT NULL AND st.completed_at < $2)
	SELECT 's', w.completed_at, w.kind, w.primary_muscles, w.secondary_muscles
		FROM work w WHERE w.completed_at > $3
	UNION ALL
	SELECT 'm', max(w.completed_at), '', ARRAY[m.muscle], '{}'::text[]
		FROM work w CROSS JOIN LATERAL unnest(w.primary_muscles || w.secondary_muscles) AS m(muscle)
		GROUP BY m.muscle
	UNION ALL
	SELECT 'c', NULL, '', '{}'::text[], '{}'::text[]
		WHERE EXISTS (SELECT 1 FROM sessions WHERE user_id = $1 AND status = 'completed')`

// muscleHistoryUntil rounds an exclusive upper bound up to Postgres's microsecond precision, so a set stored
// at the microsecond before a request clock's fractional time still counts; the scoring filters exactly.
func muscleHistoryUntil(until time.Time) time.Time {
	return until.Add(time.Microsecond - time.Nanosecond).Truncate(time.Microsecond)
}

// MuscleHistory reads the working sets of (since, until), the latest working set per muscle before until and
// whether the account ever completed a session, in one round trip.
func (s *Store) MuscleHistory(ctx context.Context, userID uuid.UUID, since, until time.Time) (MuscleHistory, error) {
	out := MuscleHistory{Last: map[string]time.Time{}}
	rows, err := s.pool.Query(ctx, muscleHistorySQL, userID, muscleHistoryUntil(until), since.Truncate(time.Microsecond))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			row string
			at  *time.Time
			set MuscleSet
		)
		if err := rows.Scan(&row, &at, &set.Kind, &set.Primary, &set.Secondary); err != nil {
			return out, err
		}
		switch row {
		case "s":
			set.CompletedAt = *at
			out.Sets = append(out.Sets, set)
		case "m":
			if at != nil && len(set.Primary) == 1 {
				out.Last[set.Primary[0]] = *at
			}
		case "c":
			out.HasCompletedSession = true
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// UNION ALL keeps no order: oldest first here, so the scoring sums in a fixed order.
	slices.SortStableFunc(out.Sets, func(a, b MuscleSet) int { return a.CompletedAt.Compare(b.CompletedAt) })
	return out, nil
}
