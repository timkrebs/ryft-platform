package scoring

import (
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Strength (Train, M3b; docs/SCORING.md → Strength, D59). Logged sets become a
// muscular load on the TRIMP scale, which joins the day's heart-rate TRIMP
// before the strain compression. Everything here is pure.
//
//	SetLoad     = reps × (weight / e1RM)² × muscleFactor     weight and bodyweight sets
//	            = duration_min × 0.5 × muscleFactor          time sets
//	            = distance_m / 100 × 0.4 × muscleFactor      distance sets
//	              × 2 for unilateral sets (reps are per side), × 0.25 for warm-ups
//	muscleFactor = (Σ primary muscle mass + ½ Σ secondary) × the exercise's load factor
//	SessionLoad = Σ SetLoad × strengthScale                   (TRIMP-equivalent)
//
// Calibration, pinned by the tests: a 5 × 5 of back squat, bench press and
// barbell row at 80 % of e1RM is strain 11.0 on its own; twelve singles at 90 %
// (six snatches, six clean & jerks) 8.0; a 20-minute bodyweight circuit 5.1.
const (
	strengthScale = 0.6
	warmupShare   = 0.25
	maxEpleyReps  = 12
	timePerMin    = 0.5
	distPer100m   = 0.4
	// DefaultBodyweightKG stands in when no session ever logged a body weight.
	DefaultBodyweightKG = 75.0
	// overlapShare: a HealthKit workout carries a session's load when it covers
	// at least this share of the session.
	overlapShare = 0.5
)

// muscleMass weights each muscle by its size, relative to the quads.
var muscleMass = map[string]float64{
	"quads": 1.0, "glutes": 1.0, "hamstrings": 0.8, "lower_back": 0.6, "adductors": 0.4, "calves": 0.3,
	"chest": 0.7, "lats": 0.7, "upper_back": 0.6, "traps": 0.4,
	"front_delts": 0.35, "side_delts": 0.3, "rear_delts": 0.25,
	"triceps": 0.35, "biceps": 0.3, "forearms": 0.2, "abs": 0.3, "obliques": 0.25,
}

// DefaultLoadFactor is an exercise kind's load factor; built-ins can set their
// own (the Olympic lifts 1.15, weighted pull-ups and dips 0.6).
func DefaultLoadFactor(kind string) float64 {
	switch kind {
	case model.KindBarbell:
		return 1.0
	case model.KindDumbbell, model.KindKettlebell:
		return 0.9
	case model.KindMachine:
		return 0.8
	case model.KindCable:
		return 0.7
	case model.KindBodyweight:
		return 0.32
	default: // cardio
		return 0.3
	}
}

// E1RM is Epley's estimated one-rep max, with reps capped at 12 (beyond that
// the estimate says nothing about strength).
func E1RM(weight float64, reps int) float64 {
	return weight * (1 + float64(min(reps, maxEpleyReps))/30)
}

// MuscleFactor is how much muscle an exercise loads.
func MuscleFactor(ex model.Exercise) float64 {
	var m float64
	for _, p := range ex.PrimaryMuscles {
		m += muscleMass[p]
	}
	for _, s := range ex.SecondaryMuscles {
		m += 0.5 * muscleMass[s]
	}
	lf := ex.LoadFactor
	if lf <= 0 {
		lf = DefaultLoadFactor(ex.Kind)
	}
	return m * lf
}

// EffectiveWeight is the weight a rep moves: the bar for weight sets, body
// weight × factor plus any added load for bodyweight sets. false for time and
// distance exercises.
func EffectiveWeight(ex model.Exercise, st model.Set, bodyweight float64) (float64, bool) {
	added := 0.0
	if st.WeightKG != nil {
		added = *st.WeightKG
	}
	switch ex.LoadType {
	case model.LoadWeight:
		return added, true
	case model.LoadBodyweight:
		return bodyweight*ex.BodyweightFactor + added, true
	}
	return 0, false
}

func reps(st model.Set) int {
	if st.Reps == nil {
		return 0
	}
	return *st.Reps
}

// SetLoad is one completed set's muscular load before strengthScale; e1rm is
// the athlete's best for the exercise as of the session (including it).
func SetLoad(st model.Set, e1rm float64, ex model.Exercise, bodyweight float64) float64 {
	if st.CompletedAt == nil {
		return 0
	}
	var base float64
	switch ex.LoadType {
	case model.LoadWeight, model.LoadBodyweight:
		w, _ := EffectiveWeight(ex, st, bodyweight)
		if w <= 0 || e1rm <= 0 {
			return 0
		}
		i := min(w/e1rm, 1)
		base = float64(reps(st)) * i * i
	case model.LoadTime:
		if st.DurationSec != nil {
			base = float64(*st.DurationSec) / 60 * timePerMin
		}
	case model.LoadDistance:
		if st.DistanceM != nil {
			base = *st.DistanceM / 100 * distPer100m
		}
	}
	load := base * MuscleFactor(ex)
	if ex.IsUnilateral {
		load *= 2
	}
	if st.IsWarmup {
		load *= warmupShare
	}
	return load
}

// BestE1RMs is the best estimated 1RM per exercise over completed working
// sets; a set's own session body weight counts, else fallback.
func BestE1RMs(sets []store.LoggedSet, exercises map[uuid.UUID]model.Exercise, fallback float64) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	for _, l := range sets {
		ex, ok := exercises[l.ExerciseID]
		if !ok || l.IsWarmup || reps(l.Set) <= 0 {
			continue
		}
		bw := fallback
		if l.Bodyweight != nil {
			bw = *l.Bodyweight
		}
		w, ok := EffectiveWeight(ex, l.Set, bw)
		if !ok || w <= 0 {
			continue
		}
		if e := E1RM(w, reps(l.Set)); e > out[l.ExerciseID] {
			out[l.ExerciseID] = e
		}
	}
	return out
}

// SessionLoad is what a completed session adds to its day.
type SessionLoad struct {
	TRIMP    float64            // muscular load, TRIMP-equivalent, one decimal
	Strain   float64            // TRIMP alone on the strain scale
	VolumeKG float64            // Σ reps × effective weight of completed working sets (both sides when unilateral)
	ByMuscle map[string]float64 // that volume per muscle: primary in full, secondary half
}

// LoadOfSession scores a session. e1rms are per exercise as of the session's
// end (BestE1RMs over sets up to it, its own included).
func LoadOfSession(s model.Session, exercises map[uuid.UUID]model.Exercise, e1rms map[uuid.UUID]float64, bodyweight float64) SessionLoad {
	out := SessionLoad{ByMuscle: map[string]float64{}}
	var raw float64
	for _, se := range s.Exercises {
		ex, ok := exercises[se.ExerciseID]
		if !ok {
			continue
		}
		for _, st := range se.Sets {
			raw += SetLoad(st, e1rms[se.ExerciseID], ex, bodyweight)
			if st.CompletedAt == nil || st.IsWarmup {
				continue
			}
			w, ok := EffectiveWeight(ex, st, bodyweight)
			if !ok {
				continue
			}
			v := float64(reps(st)) * w
			if ex.IsUnilateral {
				v *= 2
			}
			out.VolumeKG += v
			for _, m := range ex.PrimaryMuscles {
				out.ByMuscle[m] += v
			}
			for _, m := range ex.SecondaryMuscles {
				out.ByMuscle[m] += v / 2
			}
		}
	}
	out.TRIMP = math.Round(raw*strengthScale*10) / 10
	out.Strain = StrainFromTRIMP(out.TRIMP)
	out.VolumeKG = math.Round(out.VolumeKG*10) / 10
	return out
}

// SessionEnd is when a completed session ended: ended_at, else its last
// completed set, else its start.
func SessionEnd(s model.Session) time.Time {
	if s.EndedAt != nil {
		return *s.EndedAt
	}
	end := s.StartedAt
	for _, e := range s.Exercises {
		for _, st := range e.Sets {
			if st.CompletedAt != nil && st.CompletedAt.After(end) {
				end = *st.CompletedAt
			}
		}
	}
	return end
}

// strengthActivities are the HealthKit workouts a session's load may join.
var strengthActivities = map[string]bool{"strength": true, "functional_strength": true, "crossfit": true}

// MatchWorkout picks the HealthKit workout that carries a session's load: a
// strength, functional strength or crossfit workout, not taken by another
// session, overlapping at least half of the session; the largest overlap wins
// (then the earlier start). nil: the platform creates one (D60).
func MatchWorkout(s model.Session, workouts []model.Workout, taken map[uuid.UUID]bool) *model.Workout {
	start, end := s.StartedAt, SessionEnd(s)
	dur := end.Sub(start)
	if dur <= 0 {
		return nil
	}
	var best *model.Workout
	var bestOverlap time.Duration
	for k := range workouts {
		w := &workouts[k]
		if w.Source == model.SessionSource || !strengthActivities[w.Activity] || taken[w.ID] {
			continue
		}
		ov := minTime(end, w.End).Sub(maxTime(start, w.Start))
		if float64(ov) < overlapShare*float64(dur) {
			continue
		}
		if best == nil || ov > bestOverlap || (ov == bestOverlap && w.Start.Before(best.Start)) {
			best, bestOverlap = w, ov
		}
	}
	return best
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// PersonalRecords computes an exercise's records from its completed working
// sets (oldest first): e1rm, max_weight, max_reps, max_volume for weight
// exercises; max_reps (and max_weight of added load) for bodyweight ones; none
// for time and distance. The first set to reach a value holds the record.
func PersonalRecords(sets []store.LoggedSet, ex model.Exercise) []model.PR {
	best := map[string]model.PR{}
	consider := func(kind string, v float64, l store.LoggedSet) {
		if v <= 0 {
			return
		}
		if cur, ok := best[kind]; ok && v <= cur.Value {
			return
		}
		best[kind] = model.PR{ExerciseID: ex.ID, Kind: kind, Value: v, SetID: l.ID, SessionID: l.SessionID, AchievedAt: *l.CompletedAt}
	}
	for _, l := range sets {
		if l.ExerciseID != ex.ID || l.IsWarmup || l.CompletedAt == nil || reps(l.Set) <= 0 {
			continue
		}
		r := reps(l.Set)
		w := 0.0
		if l.WeightKG != nil {
			w = *l.WeightKG
		}
		switch ex.LoadType {
		case model.LoadWeight:
			consider(model.PRE1RM, round1(E1RM(w, r)), l)
			consider(model.PRMaxWeight, round1(w), l)
			consider(model.PRMaxReps, float64(r), l)
			consider(model.PRMaxVolume, round1(float64(r)*w), l)
		case model.LoadBodyweight:
			consider(model.PRMaxReps, float64(r), l)
			consider(model.PRMaxWeight, round1(w), l)
		}
	}
	out := []model.PR{}
	for _, k := range []string{model.PRE1RM, model.PRMaxWeight, model.PRMaxReps, model.PRMaxVolume} {
		if p, ok := best[k]; ok {
			out = append(out, p)
		}
	}
	return out
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// round25 rounds to the nearest 2.5 kg (the smallest pair of plates).
func round25(x float64) float64 { return math.Round(x/2.5) * 2.5 }

// NextTargetKG is the weight a plan exercise asks for next (rounded to 2.5 kg):
//
//   - pct_1rm: target_value % of the current e1RM (nil without one);
//   - kg: target_value, moved by a linear progression over the attempts so far
//     (oldest first): +increment after an attempt with every prescribed set at
//     the top of the rep range and at least the target, the same after a
//     miss, 10 % less after deload_after misses in a row (default 3);
//   - rpe, bodyweight: nil.
func NextTargetKG(pe model.PlanExercise, prog *model.Progression, attempts []store.Attempt, e1rm float64) *float64 {
	switch pe.TargetKind {
	case model.TargetPct1RM:
		if e1rm <= 0 || pe.TargetValue == nil {
			return nil
		}
		v := round25(*pe.TargetValue / 100 * e1rm)
		return &v
	case model.TargetKG:
		if pe.TargetValue == nil {
			return nil
		}
		target := *pe.TargetValue
		if prog == nil || prog.Type != model.ProgressionLinear || prog.IncrementKG == nil {
			v := round25(target)
			return &v
		}
		deloadAfter := 3
		if prog.DeloadAfter != nil {
			deloadAfter = *prog.DeloadAfter
		}
		misses := 0
		for _, a := range attempts {
			if attemptMet(pe, a, target) {
				target += *prog.IncrementKG
				misses = 0
				continue
			}
			misses++
			if misses >= deloadAfter {
				target = round25(target * 0.9)
				misses = 0
			}
		}
		v := round25(target)
		return &v
	}
	return nil
}

// attemptMet: at least the prescribed number of completed working sets with
// reps ≥ reps_max at ≥ target kg.
func attemptMet(pe model.PlanExercise, a store.Attempt, target float64) bool {
	n := 0
	for _, st := range a.Sets {
		if st.CompletedAt == nil || st.IsWarmup || st.WeightKG == nil {
			continue
		}
		if reps(st) >= pe.RepsMax && *st.WeightKG >= target-0.01 {
			n++
		}
	}
	return n >= pe.Sets
}
