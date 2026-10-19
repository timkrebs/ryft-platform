// Package train holds the pure rules of the Train feature (exercises, plans,
// sessions) that are not scoring: fixed ids for built-in content, slugs, the
// exercise name for a language, request validation and the session merge and
// conflict rule (docs/DECISIONS.md D61). No I/O.
package train

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// namespace derives every fixed id: built-in exercises, plans, plan days and
// plan exercises (from their slugs and positions), and the workout scoring
// creates for a session. The ids are part of the contract: they never change.
var namespace = uuid.MustParse("1c7e3b9a-5d2f-4e8b-9a61-3f0d2c4b7e15")

func id(parts ...string) uuid.UUID { return uuid.NewSHA1(namespace, []byte(strings.Join(parts, ":"))) }

// BuiltinExerciseID is the id of the built-in exercise with this slug.
func BuiltinExerciseID(slug string) uuid.UUID { return id("exercise", slug) }

// BuiltinPlanID is the id of the built-in plan with this slug.
func BuiltinPlanID(slug string) uuid.UUID { return id("plan", slug) }

// BuiltinPlanDayID is a built-in plan day: week 0 stands for "every week".
func BuiltinPlanDayID(plan string, week, dayIndex int) uuid.UUID {
	return id("plan", plan, "w"+strconv.Itoa(week), "d"+strconv.Itoa(dayIndex))
}

// BuiltinPlanExerciseID is the plan exercise at position on a built-in plan day.
func BuiltinPlanExerciseID(plan string, week, dayIndex, position int) uuid.UUID {
	return id("plan", plan, "w"+strconv.Itoa(week), "d"+strconv.Itoa(dayIndex), "p"+strconv.Itoa(position))
}

// SessionWorkoutID is the id of the workout scoring creates for a session that
// has no matching HealthKit workout.
func SessionWorkoutID(userID, sessionID uuid.UUID) uuid.UUID {
	return id("session-workout", userID.String(), sessionID.String())
}

// Slug turns a name into a slug: lower case ASCII letters and digits joined by
// single hyphens, German umlauts spelled out ("Kniebeuge mit Pause" →
// "kniebeuge-mit-pause"). "exercise" when nothing is left.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch r {
		case 'ä':
			b.WriteString("ae")
			dash = false
			continue
		case 'ö':
			b.WriteString("oe")
			dash = false
			continue
		case 'ü':
			b.WriteString("ue")
			dash = false
			continue
		case 'ß':
			b.WriteString("ss")
			dash = false
			continue
		}
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if len(s) > 60 {
		s = strings.TrimSuffix(s[:60], "-")
	}
	if s == "" {
		return "exercise"
	}
	return s
}

// German reports whether an Accept-Language header prefers German: its first
// language is de or de-*.
func German(acceptLanguage string) bool {
	first, _, _ := strings.Cut(acceptLanguage, ",")
	first, _, _ = strings.Cut(strings.TrimSpace(first), ";")
	first = strings.ToLower(strings.TrimSpace(first))
	return first == "de" || strings.HasPrefix(first, "de-")
}

// Localize sets ex.Name for the request's language.
func Localize(ex *model.Exercise, german bool) {
	ex.Name = ex.NameEN
	if german && ex.NameDE != "" {
		ex.Name = ex.NameDE
	}
}

// Problem is a validation failure: the request is well-formed but invalid (422).
type Problem struct{ Msg string }

func (p Problem) Error() string { return p.Msg }

func problem(format string, args ...any) error { return Problem{fmt.Sprintf(format, args...)} }

// ---- exercises ----------------------------------------------------------------

// DefaultBodyweightFactor is the share of body weight a custom bodyweight
// exercise moves unless the app says otherwise.
const DefaultBodyweightFactor = 0.65

func validMuscles(field string, ms []string, needOne bool) error {
	if needOne && len(ms) == 0 {
		return problem("%s needs at least one muscle", field)
	}
	if len(ms) > len(model.Muscles) {
		return problem("%s: too many muscles", field)
	}
	for _, m := range ms {
		if !slices.Contains(model.Muscles, m) {
			return problem("%s: unknown muscle %q", field, m)
		}
	}
	return nil
}

func validName(field, s string) error {
	if n := len([]rune(strings.TrimSpace(s))); n < 1 || n > 80 {
		return problem("%s must be 1–80 characters", field)
	}
	return nil
}

func validEquipment(eq []string) error {
	if len(eq) > 10 {
		return problem("equipment: at most 10 entries")
	}
	for _, e := range eq {
		if n := len(e); n < 1 || n > 40 {
			return problem("equipment entries must be 1–40 characters")
		}
	}
	return nil
}

// NewExercise validates a POST /v1/exercises body and returns the exercise it
// describes, with defaults filled in. Slug and LoadFactor are the caller's.
func NewExercise(in model.ExerciseInput) (model.Exercise, error) {
	ex := model.Exercise{ID: in.ID, NameEN: strings.TrimSpace(in.Name), Kind: in.Kind, LoadType: in.LoadType,
		PrimaryMuscles: in.PrimaryMuscles, SecondaryMuscles: in.SecondaryMuscles, Equipment: in.Equipment, IsCustom: true}
	if in.ID == uuid.Nil {
		return ex, problem("id is required")
	}
	ex.NameDE = ex.NameEN
	if in.NameDE != nil {
		ex.NameDE = strings.TrimSpace(*in.NameDE)
	}
	if in.BodyweightFactor != nil {
		ex.BodyweightFactor = *in.BodyweightFactor
	} else if in.LoadType == model.LoadBodyweight {
		ex.BodyweightFactor = DefaultBodyweightFactor
	}
	if in.IsUnilateral != nil {
		ex.IsUnilateral = *in.IsUnilateral
	}
	return ex, CheckExercise(ex)
}

// ApplyPatch applies a PATCH /v1/exercises/{id} body to ex and validates the result.
func ApplyPatch(ex model.Exercise, p model.ExercisePatch) (model.Exercise, error) {
	if p.Name != nil {
		ex.NameEN = strings.TrimSpace(*p.Name)
	}
	if p.NameDE != nil {
		ex.NameDE = strings.TrimSpace(*p.NameDE)
	}
	if p.Kind != nil {
		ex.Kind = *p.Kind
	}
	if p.PrimaryMuscles != nil {
		ex.PrimaryMuscles = *p.PrimaryMuscles
	}
	if p.SecondaryMuscles != nil {
		ex.SecondaryMuscles = *p.SecondaryMuscles
	}
	if p.Equipment != nil {
		ex.Equipment = *p.Equipment
	}
	if p.LoadType != nil {
		ex.LoadType = *p.LoadType
	}
	if p.BodyweightFactor != nil {
		ex.BodyweightFactor = *p.BodyweightFactor
	}
	if p.IsUnilateral != nil {
		ex.IsUnilateral = *p.IsUnilateral
	}
	if p.Archived != nil {
		ex.Archived = *p.Archived
	}
	return ex, CheckExercise(ex)
}

// CheckExercise validates a custom exercise.
func CheckExercise(ex model.Exercise) error {
	if err := validName("name", ex.NameEN); err != nil {
		return err
	}
	if err := validName("name_de", ex.NameDE); err != nil {
		return err
	}
	if !model.KnownExerciseKinds[ex.Kind] {
		return problem("kind must be one of barbell, dumbbell, kettlebell, bodyweight, machine, cable, cardio")
	}
	if !model.KnownLoadTypes[ex.LoadType] {
		return problem("load_type must be weight, bodyweight, time or distance")
	}
	if err := validMuscles("primary_muscles", ex.PrimaryMuscles, true); err != nil {
		return err
	}
	if err := validMuscles("secondary_muscles", ex.SecondaryMuscles, false); err != nil {
		return err
	}
	if ex.BodyweightFactor < 0 || ex.BodyweightFactor > 1.5 {
		return problem("bodyweight_factor must be 0–1.5")
	}
	return validEquipment(ex.Equipment)
}

// ---- plans --------------------------------------------------------------------

// CheckPlan validates a PUT /v1/plans/{id} document. Whether its exercises
// exist is the caller's check.
func CheckPlan(p model.Plan) error {
	if err := validName("name", p.Name); err != nil {
		return err
	}
	if len(p.Description) > 2000 {
		return problem("description: at most 2000 characters")
	}
	if !model.KnownPlanGoals[p.Goal] {
		return problem("goal must be strength, hypertrophy, weightlifting or general")
	}
	if p.Weeks != nil && (*p.Weeks < 1 || *p.Weeks > 52) {
		return problem("weeks must be 1–52 or null")
	}
	if p.DaysPerWeek < 1 || p.DaysPerWeek > 7 {
		return problem("days_per_week must be 1–7")
	}
	if pr := p.Progression; pr != nil {
		if !model.KnownProgressions[pr.Type] {
			return problem("progression.type must be linear, percent or none")
		}
		if pr.Type == model.ProgressionLinear {
			if pr.IncrementKG == nil || *pr.IncrementKG <= 0 || *pr.IncrementKG > 20 {
				return problem("progression.increment_kg must be 0–20 kg for linear progression")
			}
			if pr.DeloadAfter != nil && (*pr.DeloadAfter < 1 || *pr.DeloadAfter > 10) {
				return problem("progression.deload_after must be 1–10")
			}
			if pr.OnFail != nil && *pr.OnFail != "repeat" {
				return problem(`progression.on_fail must be "repeat"`)
			}
		}
	}
	if len(p.Days) == 0 || len(p.Days) > 7*52 {
		return problem("a plan needs 1–364 days")
	}
	days, ex := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	slots := map[[2]int]bool{}
	for _, d := range p.Days {
		if d.ID == uuid.Nil || days[d.ID] {
			return problem("every plan day needs its own id")
		}
		days[d.ID] = true
		w := 0
		if d.Week != nil {
			if p.Weeks == nil {
				return problem("plan day %s: a plan without weeks repeats; its days have no week", d.ID)
			}
			if *d.Week < 1 || *d.Week > *p.Weeks {
				return problem("plan day %s: week must be 1–%d", d.ID, *p.Weeks)
			}
			w = *d.Week
		}
		if d.DayIndex < 0 || d.DayIndex >= p.DaysPerWeek {
			return problem("plan day %s: day_index must be 0–%d", d.ID, p.DaysPerWeek-1)
		}
		if slots[[2]int{w, d.DayIndex}] {
			return problem("two plan days share week %d, day_index %d", w, d.DayIndex)
		}
		slots[[2]int{w, d.DayIndex}] = true
		if len(d.Name) > 80 {
			return problem("plan day names: at most 80 characters")
		}
		if len(d.Exercises) > 30 {
			return problem("plan day %s: at most 30 exercises", d.ID)
		}
		pos := map[int]bool{}
		for _, e := range d.Exercises {
			if e.ID == uuid.Nil || ex[e.ID] {
				return problem("every plan exercise needs its own id")
			}
			ex[e.ID] = true
			if e.ExerciseID == uuid.Nil {
				return problem("plan exercise %s: exercise_id is required", e.ID)
			}
			if e.Position < 0 || pos[e.Position] {
				return problem("plan day %s: positions must be unique and ≥ 0", d.ID)
			}
			pos[e.Position] = true
			if e.Sets < 1 || e.Sets > 20 {
				return problem("plan exercise %s: sets must be 1–20", e.ID)
			}
			if e.RepsMin < 1 || e.RepsMin > e.RepsMax || e.RepsMax > 100 {
				return problem("plan exercise %s: 1 ≤ reps_min ≤ reps_max ≤ 100", e.ID)
			}
			if err := checkTarget(e); err != nil {
				return err
			}
			if e.RestSec < 0 || e.RestSec > 900 {
				return problem("plan exercise %s: rest_sec must be 0–900", e.ID)
			}
			if len(e.Notes) > 500 {
				return problem("plan exercise notes: at most 500 characters")
			}
		}
	}
	return nil
}

func checkTarget(e model.PlanExercise) error {
	v := e.TargetValue
	switch e.TargetKind {
	case model.TargetPct1RM:
		if v == nil || *v <= 0 || *v > 110 {
			return problem("plan exercise %s: pct_1rm needs target_value 1–110 (percent)", e.ID)
		}
	case model.TargetRPE:
		if v == nil || *v < 1 || *v > 10 {
			return problem("plan exercise %s: rpe needs target_value 1–10", e.ID)
		}
	case model.TargetKG:
		if v == nil || *v < 0 || *v > 500 {
			return problem("plan exercise %s: kg needs target_value 0–500", e.ID)
		}
	case model.TargetBodyweight:
	default:
		return problem("plan exercise %s: target_kind must be pct_1rm, rpe, kg or bodyweight", e.ID)
	}
	return nil
}

// ---- sessions -----------------------------------------------------------------

// Session limits (docs/API.md → PUT /v1/sessions/{id}).
const (
	MaxSessionExercises = 50
	MaxSetsPerExercise  = 50
	maxTitle            = 120
	maxNotes            = 2000
)

// CheckSession validates a PUT /v1/sessions/{id} body and clamps absurd
// values in place (reps ≤ 100, kg ≤ 500, RPE 1–10, …). now stands in for a
// set's missing updated_at. Whether the exercises exist is the caller's check.
func CheckSession(s *model.Session, now time.Time) error {
	if s.ID == uuid.Nil {
		return problem("id is required")
	}
	if s.StartedAt.IsZero() {
		return problem("started_at is required")
	}
	if s.EndedAt != nil && s.EndedAt.Before(s.StartedAt) {
		return problem("ended_at must not be before started_at")
	}
	if !model.KnownSessionStatuses[s.Status] {
		return problem("status must be in_progress, completed or discarded")
	}
	if len([]rune(s.Title)) > maxTitle {
		return problem("title: at most %d characters", maxTitle)
	}
	if len([]rune(s.Notes)) > maxNotes {
		return problem("notes: at most %d characters", maxNotes)
	}
	if len(s.Exercises) > MaxSessionExercises {
		return problem("at most %d exercises per session", MaxSessionExercises)
	}
	if s.BodyweightKG != nil {
		s.BodyweightKG = clampF(s.BodyweightKG, 30, 300)
	}
	exIDs, setIDs, pos := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[int]bool{}
	for i := range s.Exercises {
		e := &s.Exercises[i]
		if e.ID == uuid.Nil || exIDs[e.ID] {
			return problem("every session exercise needs its own id")
		}
		exIDs[e.ID] = true
		if e.ExerciseID == uuid.Nil {
			return problem("session exercise %s: exercise_id is required", e.ID)
		}
		if e.Position < 0 || pos[e.Position] {
			return problem("exercise positions must be unique and ≥ 0")
		}
		pos[e.Position] = true
		if len([]rune(e.Notes)) > maxNotes {
			return problem("notes: at most %d characters", maxNotes)
		}
		if len(e.Sets) > MaxSetsPerExercise {
			return problem("at most %d sets per exercise", MaxSetsPerExercise)
		}
		idx := map[int]bool{}
		for k := range e.Sets {
			st := &e.Sets[k]
			if st.ID == uuid.Nil || setIDs[st.ID] {
				return problem("every set needs its own id")
			}
			setIDs[st.ID] = true
			if st.SetIndex < 0 || idx[st.SetIndex] {
				return problem("session exercise %s: set_index must be unique and ≥ 0", e.ID)
			}
			idx[st.SetIndex] = true
			ClampSet(st)
			if st.UpdatedAt.IsZero() {
				st.UpdatedAt = now
			}
		}
	}
	return nil
}

// ClampSet bounds a set's numbers to what a person can log.
func ClampSet(st *model.Set) {
	st.Reps = clampI(st.Reps, 0, 100)
	st.WeightKG = clampF(st.WeightKG, 0, 500)
	st.RPE = clampF(st.RPE, 1, 10)
	st.DurationSec = clampI(st.DurationSec, 0, 21600)
	st.DistanceM = clampF(st.DistanceM, 0, 100000)
}

func clampI(v *int, lo, hi int) *int {
	if v == nil {
		return nil
	}
	x := min(max(*v, lo), hi)
	return &x
}

func clampF(v *float64, lo, hi float64) *float64 {
	if v == nil {
		return nil
	}
	x := min(max(*v, lo), hi)
	return &x
}

// Merge applies a PUT /v1/sessions/{id} body to the stored session (nil when
// it does not exist yet) under D61:
//
//   - The session's own fields come from the body.
//   - While the stored session is in progress (or discarded), exercises and sets
//     are merged by id: the body's are inserted or updated, ones only the server
//     has are kept, so an older queued save never deletes newer sets.
//   - A completed session is replaced whole: what the body leaves out goes
//     (except ended_at, which it keeps when the body has none).
//   - A set on both sides whose stored updated_at is later than the body's is a
//     conflict: Merge returns conflict = true and nothing may be written.
func Merge(stored *model.Session, body model.Session) (merged model.Session, conflict bool) {
	if stored == nil {
		return body, false
	}
	storedSets := map[uuid.UUID]model.Set{}
	for _, e := range stored.Exercises {
		for _, st := range e.Sets {
			storedSets[st.ID] = st
		}
	}
	for _, e := range body.Exercises {
		for _, st := range e.Sets {
			if old, ok := storedSets[st.ID]; ok && old.UpdatedAt.After(st.UpdatedAt) {
				return *stored, true
			}
		}
	}
	if stored.Status == model.SessionCompleted {
		if body.EndedAt == nil {
			body.EndedAt = stored.EndedAt // a completed session always has an end
		}
		return body, false
	}
	merged = body
	byID := map[uuid.UUID]int{}
	for i, e := range merged.Exercises {
		byID[e.ID] = i
	}
	for _, se := range stored.Exercises {
		i, ok := byID[se.ID]
		if !ok {
			merged.Exercises = append(merged.Exercises, se) // only on the server: kept
			continue
		}
		have := map[uuid.UUID]bool{}
		for _, st := range merged.Exercises[i].Sets {
			have[st.ID] = true
		}
		for _, st := range se.Sets {
			if !have[st.ID] {
				merged.Exercises[i].Sets = append(merged.Exercises[i].Sets, st)
			}
		}
		slices.SortStableFunc(merged.Exercises[i].Sets, func(a, b model.Set) int { return a.SetIndex - b.SetIndex })
	}
	slices.SortStableFunc(merged.Exercises, func(a, b model.SessionExercise) int { return a.Position - b.Position })
	return merged, false
}
