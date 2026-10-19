package scoring

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// strengthDay is what a day's completed strength sessions add to it.
type strengthDay struct {
	TRIMP    float64 // Σ muscular load
	Minutes  int
	VolumeKG float64
	Sessions int
	Week     map[string]any // details.strength_week: the 7 days ending on this day
}

// strength scores the strength sessions started on [dayStart, dayEnd), stores
// each session's load, attaches it to a workout (a matching HealthKit workout,
// or one scoring creates, D60) and releases attachments that no longer hold.
// It runs before the per-workout heart-rate pass, so a created workout is
// scored like any other.
func (e *Engine) strength(ctx context.Context, userID uuid.UUID, dayStart, dayEnd time.Time) (strengthDay, error) {
	var out strengthDay
	sessions, err := e.st.CompletedSessionsStartedBetween(ctx, userID, dayStart, dayEnd)
	if err != nil {
		return out, err
	}
	exercises, err := e.exercisesOf(ctx, userID, sessions)
	if err != nil {
		return out, err
	}
	// HealthKit workouts that could overlap a session of this day.
	candidates, err := e.st.WorkoutsBetween(ctx, userID, dayStart.Add(-12*time.Hour), dayEnd.Add(12*time.Hour))
	if err != nil {
		return out, err
	}
	taken := map[uuid.UUID]bool{}
	chosen := map[uuid.UUID]uuid.UUID{}
	ids := make([]uuid.UUID, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
		load, err := e.loadOf(ctx, userID, s, exercises)
		if err != nil {
			return out, err
		}
		end := SessionEnd(s)
		workoutID := train.SessionWorkoutID(userID, s.ID)
		if w := MatchWorkout(s, candidates, taken); w != nil {
			workoutID = w.ID
			taken[w.ID] = true
			if err := e.st.AttachSession(ctx, userID, w.ID, s.ID, load.TRIMP); err != nil {
				return out, err
			}
		} else if err := e.st.EnsureSessionWorkout(ctx, userID, workoutID, s.ID, s.StartedAt, end, load.TRIMP); err != nil {
			return out, err
		}
		chosen[s.ID] = workoutID
		if err := e.st.SetSessionLoad(ctx, userID, s.ID, load.TRIMP, load.Strain, load.VolumeKG, workoutID); err != nil {
			return out, err
		}
		out.TRIMP += load.TRIMP
		out.VolumeKG += load.VolumeKG
		out.Minutes += int(math.Round(end.Sub(s.StartedAt).Minutes()))
		out.Sessions++
	}
	if err := e.st.ReleaseSessionWorkouts(ctx, userID, ids, chosen); err != nil {
		return out, err
	}
	out.Week, err = e.strengthWeek(ctx, userID, dayStart.AddDate(0, 0, -6), dayEnd)
	return out, err
}

// loadOf computes one session's load: e1RMs as of its end (its own sets
// included), its body weight or the last one logged (else 75 kg).
func (e *Engine) loadOf(ctx context.Context, userID uuid.UUID, s model.Session, exercises map[uuid.UUID]model.Exercise) (SessionLoad, error) {
	bw, err := e.bodyweight(ctx, userID, s)
	if err != nil {
		return SessionLoad{}, err
	}
	var exIDs []uuid.UUID
	for _, se := range s.Exercises {
		exIDs = append(exIDs, se.ExerciseID)
	}
	sets, err := e.st.LoggedSets(ctx, userID, exIDs, SessionEnd(s), s.ID)
	if err != nil {
		return SessionLoad{}, err
	}
	return LoadOfSession(s, exercises, BestE1RMs(sets, exercises, bw), bw), nil
}

// SessionLoadNow is LoadOfSession with the store lookups the api needs to
// answer POST /v1/sessions/{id}/complete the same way scoring will score it.
func (e *Engine) SessionLoadNow(ctx context.Context, userID uuid.UUID, s model.Session) (SessionLoad, error) {
	exercises, err := e.exercisesOf(ctx, userID, []model.Session{s})
	if err != nil {
		return SessionLoad{}, err
	}
	return e.loadOf(ctx, userID, s, exercises)
}

func (e *Engine) bodyweight(ctx context.Context, userID uuid.UUID, s model.Session) (float64, error) {
	if s.BodyweightKG != nil {
		return *s.BodyweightKG, nil
	}
	last, err := e.st.LastBodyweight(ctx, userID, s.StartedAt)
	if err != nil || last == nil {
		return DefaultBodyweightKG, err
	}
	return *last, nil
}

func (e *Engine) exercisesOf(ctx context.Context, userID uuid.UUID, sessions []model.Session) (map[uuid.UUID]model.Exercise, error) {
	var ids []uuid.UUID
	for _, s := range sessions {
		for _, se := range s.Exercises {
			ids = append(ids, se.ExerciseID)
		}
	}
	return e.st.VisibleExercises(ctx, userID, ids)
}

// strengthWeek is details.strength_week: strength minutes and volume per
// muscle (primary in full, secondary half) of the sessions started in [from, to).
func (e *Engine) strengthWeek(ctx context.Context, userID uuid.UUID, from, to time.Time) (map[string]any, error) {
	sessions, err := e.st.CompletedSessionsStartedBetween(ctx, userID, from, to)
	if err != nil {
		return nil, err
	}
	exercises, err := e.exercisesOf(ctx, userID, sessions)
	if err != nil {
		return nil, err
	}
	minutes := 0
	byMuscle := map[string]float64{}
	for _, s := range sessions {
		minutes += int(math.Round(SessionEnd(s).Sub(s.StartedAt).Minutes()))
		bw, err := e.bodyweight(ctx, userID, s)
		if err != nil {
			return nil, err
		}
		// Volume needs no e1RM.
		for m, v := range LoadOfSession(s, exercises, nil, bw).ByMuscle {
			byMuscle[m] += v
		}
	}
	for m, v := range byMuscle {
		byMuscle[m] = math.Round(v)
	}
	return map[string]any{"minutes": minutes, "sessions": len(sessions), "volume_kg": byMuscle}, nil
}

// DayStrainSessions attaches the day's completed sessions to its DayStrain.
func DayStrainSessions(ds *model.DayStrain, sessions []model.SessionSummary) {
	if ds == nil {
		return
	}
	ds.Sessions = sessions
	if ds.Sessions == nil {
		ds.Sessions = []model.SessionSummary{}
	}
}

// strainOf is a workout's strain with an attached session's muscular load.
func strainOf(hr StrainResult, w model.Workout) float64 {
	if w.MuscularLoad == nil || *w.MuscularLoad <= 0 {
		return hr.Score
	}
	return StrainFromTRIMP(hr.TRIMP + *w.MuscularLoad)
}
