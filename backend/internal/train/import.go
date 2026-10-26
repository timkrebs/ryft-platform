package train

import (
	"bytes"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// The training import's pure rules (POST /v1/train/import, docs/API.md, D124-D130):
// what a request may hold, what an imported session must be, when a stored item is
// the same as the request's (T10, D126) or was changed in the app since (newer_on_server),
// and the window and estimate of its rescoring (D128). Messages name ids and fields,
// never a value from the file (D114).

// Import limits (docs/API.md → Conventions → Import limits).
const (
	MaxImportPlans    = 5
	MaxImportSessions = 50
	// MaxImportSessionSets bounds one session's sets (an item's invalid) and MaxImportSets a
	// request's (a whole-request 422): 50 exercises × 50 sets is the live limit, far more than
	// any logged session, and the request's total keeps one import's work bounded (N1).
	MaxImportSessionSets = 300
	MaxImportSets        = 2500
	// The import's time bounds (S1): a session lasts at most a day, a set is done within an
	// hour of it, nothing is stamped more than a day ahead.
	maxSessionLength = 24 * time.Hour
	setSlack         = time.Hour
	// ImportWindowDays: only days in the 400 local days ending today are queued for
	// scoring (D128), a sync's window. It is scoring.MaxRangeDays (pinned by a test in
	// cmd/api; this package cannot import scoring).
	ImportWindowDays = 400
	// The import's estimate: the sync's plus this per queued day, at most maxExpectedSec.
	expectedPerDaySec = 2
	maxExpectedSec    = 300
)

// The range of a session's started_at (and an enrolment's started_at): not before
// 2000-01-01, at most a day of clock skew ahead.
var earliestStart = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const futureSlack = 24 * time.Hour

func inStartRange(t, now time.Time) bool {
	return !t.Before(earliestStart) && !t.After(now.Add(futureSlack))
}

// CheckImportRequest refuses a request as a whole (422): an unknown source, more than
// 5 plans or 50 sessions, the same plan or session id twice.
func CheckImportRequest(req model.TrainImportRequest) error {
	if !model.KnownImportSources[req.Source] {
		return problem("source must be one of: %s", model.ImportSourceAlphaProgression)
	}
	if len(req.Plans) > MaxImportPlans {
		return problem("at most %d plans per request", MaxImportPlans)
	}
	if len(req.Sessions) > MaxImportSessions {
		return problem("at most %d sessions per request", MaxImportSessions)
	}
	plans := map[uuid.UUID]bool{}
	for _, p := range req.Plans {
		if p.ID != uuid.Nil && plans[p.ID] {
			return problem("plan %s is in the request twice", p.ID)
		}
		plans[p.ID] = true
	}
	sessions, sets := map[uuid.UUID]bool{}, 0
	for _, s := range req.Sessions {
		if s.ID != uuid.Nil && sessions[s.ID] {
			return problem("session %s is in the request twice", s.ID)
		}
		sessions[s.ID] = true
		for _, e := range s.Exercises {
			sets += len(e.Sets)
		}
	}
	if sets > MaxImportSets {
		return problem("at most %d sets per request", MaxImportSets)
	}
	return nil
}

// CheckImportSession validates one session of an import and normalises it in place:
// CheckSession's rules and clamping, plus status completed, ended_at set, started_at
// between 2000-01-01 and 24 h from now, ended_at at most 24 h after started_at and at most
// 24 h from now, at most MaxImportSessionSets sets, every set completed (T2) within an hour
// of the session ([started_at − 1 h, ended_at + 1 h]) and updated at most 24 h from now, and
// assist_kg only where CheckAssist allows it (for the exercises in exercises; an unknown
// one is the store's unknown_exercise). A set without updated_at takes its completed_at,
// not now, so a resend is identical (D126).
func CheckImportSession(s *model.Session, now time.Time, exercises map[uuid.UUID]model.Exercise) error {
	for i := range s.Exercises {
		for k := range s.Exercises[i].Sets {
			if st := &s.Exercises[i].Sets[k]; st.CompletedAt != nil && st.UpdatedAt.IsZero() {
				st.UpdatedAt = *st.CompletedAt
			}
		}
	}
	if err := CheckSession(s, now); err != nil {
		return err
	}
	if s.Status != model.SessionCompleted {
		return problem("status must be completed")
	}
	if s.EndedAt == nil {
		return problem("ended_at is required")
	}
	if !inStartRange(s.StartedAt, now) {
		return problem("started_at must be between 2000-01-01 and 24 h from now")
	}
	if s.EndedAt.Sub(s.StartedAt) > maxSessionLength {
		return problem("ended_at must be at most 24 h after started_at")
	}
	if s.EndedAt.After(now.Add(futureSlack)) {
		return problem("ended_at must be at most 24 h from now")
	}
	n := 0
	for _, e := range s.Exercises {
		n += len(e.Sets)
	}
	if n > MaxImportSessionSets {
		return problem("at most %d sets per imported session", MaxImportSessionSets)
	}
	from, to := s.StartedAt.Add(-setSlack), s.EndedAt.Add(setSlack)
	for _, e := range s.Exercises {
		ex, known := exercises[e.ExerciseID]
		for _, st := range e.Sets {
			if st.CompletedAt == nil {
				return problem("set %s: completed_at is required (an import sends only sets that were done)", st.ID)
			}
			if st.CompletedAt.Before(from) || st.CompletedAt.After(to) {
				return problem("set %s: completed_at must be between an hour before started_at and an hour after ended_at", st.ID)
			}
			if st.UpdatedAt.After(now.Add(futureSlack)) {
				return problem("set %s: updated_at must be at most 24 h from now", st.ID)
			}
			if known {
				if err := CheckAssist(ex, st); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// CheckAssist: assist_kg (the support of an assisted bodyweight set, T4) belongs only to
// a set of a bodyweight exercise, and not together with an added weight_kg above 0. Run
// it after ClampSet.
func CheckAssist(ex model.Exercise, st model.Set) error {
	if st.AssistKG == nil {
		return nil
	}
	if ex.LoadType != model.LoadBodyweight {
		return problem("set %s: assist_kg is only for bodyweight exercises", st.ID)
	}
	if *st.AssistKG > 0 && st.WeightKG != nil && *st.WeightKG > 0 {
		return problem("set %s: assist_kg and weight_kg cannot both be above 0", st.ID)
	}
	return nil
}

// ---- identical? (T10, D126) -------------------------------------------------------------

// The normalised content of a session, a plan: what the request determines and nothing the
// server computes. Times are Unix microseconds (what Postgres keeps), arrays maps by id.
type (
	setKey struct {
		SetIndex           int
		Reps, DurationSec  *int
		WeightKG, AssistKG float64 // null = 0 (T3)
		RPE, DistanceM     *float64
		IsWarmup           bool
		CompletedAt        *int64
		UpdatedAt          int64
	}
	exerciseKey struct {
		ExerciseID     uuid.UUID
		PlanExerciseID *uuid.UUID
		Position       int
		Notes          string
		Sets           map[uuid.UUID]setKey
	}
	sessionKey struct {
		Title, Notes        string
		StartedAt           int64
		EndedAt             *int64
		BodyweightKG        *float64
		DeviceID, PlanDayID *uuid.UUID
		ImportSource        *string
		Exercises           map[uuid.UUID]exerciseKey
	}
)

func micros(t time.Time) int64 { return t.UnixMicro() }

func microsOf(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	m := micros(*t)
	return &m
}

func zeroIfNil(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func keyOfSession(s model.Session) sessionKey {
	k := sessionKey{Title: s.Title, Notes: s.Notes, StartedAt: micros(s.StartedAt), EndedAt: microsOf(s.EndedAt),
		DeviceID: s.DeviceID, PlanDayID: s.PlanDayID, ImportSource: s.ImportSource,
		Exercises: make(map[uuid.UUID]exerciseKey, len(s.Exercises))}
	if s.BodyweightKG != nil {
		k.BodyweightKG = clampF(s.BodyweightKG, 30, 300)
	}
	for _, e := range s.Exercises {
		ek := exerciseKey{ExerciseID: e.ExerciseID, PlanExerciseID: e.PlanExerciseID, Position: e.Position, Notes: e.Notes,
			Sets: make(map[uuid.UUID]setKey, len(e.Sets))}
		for _, st := range e.Sets {
			ClampSet(&st) // a copy: the caller's set is not touched
			ek.Sets[st.ID] = setKey{SetIndex: st.SetIndex, Reps: st.Reps, DurationSec: st.DurationSec,
				WeightKG: zeroIfNil(st.WeightKG), AssistKG: zeroIfNil(st.AssistKG), RPE: st.RPE, DistanceM: st.DistanceM,
				IsWarmup: st.IsWarmup, CompletedAt: microsOf(st.CompletedAt), UpdatedAt: micros(st.UpdatedAt)}
		}
		k.Exercises[e.ID] = ek
	}
	return k
}

// SameSession says whether the stored session holds what the request's does (T10): title,
// times, notes, body weight, device, plan day and import_source; its exercises by id
// (exercise, plan exercise, position, notes); their sets by id (every field the app sets,
// completed_at and updated_at included). Both sides are clamped alike, weight_kg and
// assist_kg null equal 0, times compare as instants to the microsecond, array order does
// not matter. Not compared: the server's fields (muscular_trimp, strain, volume_kg,
// workout_id, prs, the session's updated_at, origin) and status (ImportConflict's).
func SameSession(stored, body model.Session) bool {
	return reflect.DeepEqual(keyOfSession(stored), keyOfSession(body))
}

// ImportConflict says whether the app changed the stored imported session after the
// import: the session was saved or completed in the app after the import last wrote it
// (updated_at later than imported_at, or no imported_at), a set on both sides has a stored
// updated_at later than the request's (train.Merge's rule), or the stored status is not
// completed (the user discarded it). The import then leaves it alone: newer_on_server
// (D126). Any edit counts, a title or a note as much as a set.
func ImportConflict(stored, body model.Session) bool {
	if stored.Status != model.SessionCompleted || edited(stored.UpdatedAt, stored.ImportedAt) {
		return true
	}
	had := map[uuid.UUID]int64{}
	for _, e := range stored.Exercises {
		for _, st := range e.Sets {
			had[st.ID] = micros(st.UpdatedAt)
		}
	}
	for _, e := range body.Exercises {
		for _, st := range e.Sets {
			if at, ok := had[st.ID]; ok && at > micros(st.UpdatedAt) {
				return true
			}
		}
	}
	return false
}

// PlanConflict says whether the app changed the stored plan after the import last wrote it
// (PUT /v1/plans/{id}: edited or archived; updated_at later than imported_at), or made it
// (no imported_at). The import then leaves it alone: newer_on_server (D126).
func PlanConflict(stored model.Plan) bool { return edited(stored.UpdatedAt, stored.ImportedAt) }

// edited: changed after the import wrote it, or never written by it. Both times are the
// database's, from the same statement for the import's write, so equal means untouched.
func edited(updated time.Time, imported *time.Time) bool {
	return imported == nil || micros(updated) > micros(*imported)
}

type (
	planExerciseKey struct {
		ExerciseID                       uuid.UUID
		Position, Sets, RepsMin, RepsMax int
		TargetKind                       string
		TargetValue                      *float64
		RestSec                          int
		SupersetGroup                    *int
		Notes                            string
	}
	planDayKey struct {
		Week      *int
		DayIndex  int
		Name      string
		Exercises map[uuid.UUID]planExerciseKey
	}
	planKey struct {
		Name, Goal, Description string
		Weeks                   *int
		DaysPerWeek             int
		Progression             model.Progression
		Archived                bool
		Days                    map[uuid.UUID]planDayKey
	}
)

func keyOfPlan(p model.Plan) planKey {
	k := planKey{Name: p.Name, Goal: p.Goal, Description: p.Description, Weeks: p.Weeks, DaysPerWeek: p.DaysPerWeek,
		Progression: model.Progression{Type: model.ProgressionNone}, Archived: p.Archived,
		Days: make(map[uuid.UUID]planDayKey, len(p.Days))}
	if p.Progression != nil {
		k.Progression = *p.Progression
	}
	for _, d := range p.Days {
		dk := planDayKey{Week: d.Week, DayIndex: d.DayIndex, Name: d.Name, Exercises: make(map[uuid.UUID]planExerciseKey, len(d.Exercises))}
		for _, e := range d.Exercises {
			dk.Exercises[e.ID] = planExerciseKey{ExerciseID: e.ExerciseID, Position: e.Position, Sets: e.Sets, RepsMin: e.RepsMin,
				RepsMax: e.RepsMax, TargetKind: e.TargetKind, TargetValue: e.TargetValue, RestSec: e.RestSec,
				SupersetGroup: e.SupersetGroup, Notes: e.Notes}
		}
		k.Days[d.ID] = dk
	}
	return k
}

// SamePlan says whether the stored plan holds what the request's does: every field of the
// Plan document except slug, is_builtin and the resolved PlanExercise.exercise; days and
// exercises by id; progression null equals {"type": "none"} (D126).
func SamePlan(stored, body model.Plan) bool {
	return reflect.DeepEqual(keyOfPlan(stored), keyOfPlan(body))
}

// ---- order, window, estimate -------------------------------------------------------------

// ImportOrder is the order the sessions are stored in: oldest started_at first, ties by
// id, whatever their order in the request (T5), so a record or an e1RM is measured
// against the history before it. It returns indices into sessions.
func ImportOrder(sessions []model.Session) []int {
	idx := make([]int, len(sessions))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		if c := sessions[a].StartedAt.Compare(sessions[b].StartedAt); c != 0 {
			return c
		}
		return bytes.Compare(sessions[a].ID[:], sessions[b].ID[:])
	})
	return idx
}

// ImportWindow is the local days an import may queue for scoring: the ImportWindowDays
// ending today in loc, as "2006-01-02" (from, to).
func ImportWindow(now time.Time, loc *time.Location) (from, to string) {
	t := now.In(loc)
	first := time.Date(t.Year(), t.Month(), t.Day()-(ImportWindowDays-1), 12, 0, 0, 0, loc)
	return first.Format(time.DateOnly), t.Format(time.DateOnly)
}

// ExpectedSeconds is an import's expected_within_sec: the sync estimate (base) plus 2 s per
// queued day, at most 300 s, and never below base.
func ExpectedSeconds(base, days int) int {
	return max(base, min(maxExpectedSec, base+expectedPerDaySec*days))
}

// ---- enrolment (T8) -----------------------------------------------------------------------

// CheckEnroll validates the fields of a POST /v1/plans/{id}/enroll body against the plan:
// started_at between 2000-01-01 and 24 h from now, current_week 1..weeks (1..1000 for a
// plan without weeks), current_day_index 0..days_per_week-1. Whether the plan has a day at
// the resulting position is the store's check: it knows the stored position.
func CheckEnroll(p model.Plan, in model.EnrollPost, now time.Time) error {
	if in.StartedAt != nil && !inStartRange(*in.StartedAt, now) {
		return problem("started_at must be between 2000-01-01 and 24 h from now")
	}
	if w := in.CurrentWeek; w != nil {
		last := 1000
		if p.Weeks != nil {
			last = *p.Weeks
		}
		if *w < 1 || *w > last {
			return problem("current_week must be 1–%d", last)
		}
	}
	if d := in.CurrentDayIndex; d != nil && (*d < 0 || *d >= p.DaysPerWeek) {
		return problem("current_day_index must be 0–%d", p.DaysPerWeek-1)
	}
	return nil
}
