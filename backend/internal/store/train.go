package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// Train (migration 0011): exercises, plans, enrolments, sessions, records.
// Every method is scoped to one user; built-ins (user_id NULL) are visible to
// all and changed only by the seeds.

var (
	// ErrForbidden: the row is built-in and cannot be changed (403).
	ErrForbidden = errors.New("built-in")
	// ErrTaken: the id belongs to a built-in or another user's row (409).
	ErrTaken = errors.New("id taken")
)

// querier is what both the pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---- exercises -------------------------------------------------------------------

const exerciseColumns = `id, user_id IS NOT NULL, slug, name_en, name_de, kind, primary_muscles, secondary_muscles,
	equipment, load_type, bodyweight_factor::float8, load_factor::float8, is_unilateral, archived`

func scanExercise(r pgx.Row) (model.Exercise, error) {
	var e model.Exercise
	err := r.Scan(&e.ID, &e.IsCustom, &e.Slug, &e.NameEN, &e.NameDE, &e.Kind, &e.PrimaryMuscles, &e.SecondaryMuscles,
		&e.Equipment, &e.LoadType, &e.BodyweightFactor, &e.LoadFactor, &e.IsUnilateral, &e.Archived)
	e.Name = e.NameEN
	return e, err
}

// ExerciseFilter narrows GET /v1/exercises.
type ExerciseFilter struct {
	Q, Muscle, Kind string
	IncludeArchived bool
}

// Exercises lists the built-ins and the user's own exercises matching f.
func (s *Store) Exercises(ctx context.Context, userID uuid.UUID, f ExerciseFilter) ([]model.Exercise, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+exerciseColumns+` FROM exercises
		WHERE (user_id IS NULL OR user_id = $1)
		  AND ($2 = '' OR strpos(lower(name_en), lower($2)) > 0 OR strpos(lower(name_de), lower($2)) > 0 OR strpos(slug, lower($2)) > 0)
		  AND ($3 = '' OR $3 = ANY(primary_muscles) OR $3 = ANY(secondary_muscles))
		  AND ($4 = '' OR kind = $4)
		  AND ($5 OR NOT archived)
		ORDER BY name_en`, userID, f.Q, f.Muscle, f.Kind, f.IncludeArchived)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Exercise, error) { return scanExercise(r) })
}

// Exercise is one exercise the user can see (built-in or own); ErrNotFound otherwise.
func (s *Store) Exercise(ctx context.Context, userID, id uuid.UUID) (model.Exercise, error) {
	e, err := scanExercise(s.pool.QueryRow(ctx, `SELECT `+exerciseColumns+` FROM exercises
		WHERE id = $2 AND (user_id IS NULL OR user_id = $1)`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// VisibleExercises returns those of ids the user can see, by id.
func (s *Store) VisibleExercises(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]model.Exercise, error) {
	return visibleExercises(ctx, s.pool, userID, ids)
}

func visibleExercises(ctx context.Context, q querier, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]model.Exercise, error) {
	out := map[uuid.UUID]model.Exercise{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+exerciseColumns+` FROM exercises
		WHERE id = ANY($2) AND (user_id IS NULL OR user_id = $1)`, userID, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Exercise, error) { return scanExercise(r) })
	for _, e := range list {
		out[e.ID] = e
	}
	return out, err
}

// owner reads who owns an exercise or plan row: (nil, true) for a built-in,
// (&id, true) for a user's, (nil, false) when there is none.
func owner(ctx context.Context, tx pgx.Tx, table string, id uuid.UUID) (*uuid.UUID, bool, error) {
	var u *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT user_id FROM `+table+` WHERE id = $1 FOR UPDATE`, id).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return u, err == nil, err
}

// SaveCustomExercise creates the user's exercise, or updates it when the same
// id is sent again (created = false). ErrTaken when the id is a built-in or
// another user's.
func (s *Store) SaveCustomExercise(ctx context.Context, userID uuid.UUID, e model.Exercise) (model.Exercise, bool, error) {
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		own, exists, err := owner(ctx, tx, "exercises", e.ID)
		if err != nil {
			return err
		}
		if exists && (own == nil || *own != userID) {
			return ErrTaken
		}
		if exists {
			return writeExercise(ctx, tx, userID, e)
		}
		slug, err := freeSlug(ctx, tx, userID, train.Slug(e.NameEN))
		if err != nil {
			return err
		}
		created = true
		_, err = tx.Exec(ctx, `INSERT INTO exercises (id, user_id, slug, name_en, name_de, kind, primary_muscles,
			secondary_muscles, equipment, load_type, bodyweight_factor, load_factor, is_unilateral)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			e.ID, userID, slug, e.NameEN, e.NameDE, e.Kind, e.PrimaryMuscles, nonNil(e.SecondaryMuscles),
			nonNil(e.Equipment), e.LoadType, e.BodyweightFactor, e.LoadFactor, e.IsUnilateral)
		return err
	})
	if err != nil {
		return model.Exercise{}, false, err
	}
	out, err := s.Exercise(ctx, userID, e.ID)
	return out, created, err
}

func writeExercise(ctx context.Context, tx pgx.Tx, userID uuid.UUID, e model.Exercise) error {
	_, err := tx.Exec(ctx, `UPDATE exercises SET name_en=$3, name_de=$4, kind=$5, primary_muscles=$6, secondary_muscles=$7,
		equipment=$8, load_type=$9, bodyweight_factor=$10, load_factor=$11, is_unilateral=$12, archived=$13, updated_at=now()
		WHERE id=$1 AND user_id=$2`, e.ID, userID, e.NameEN, e.NameDE, e.Kind, e.PrimaryMuscles, nonNil(e.SecondaryMuscles),
		nonNil(e.Equipment), e.LoadType, e.BodyweightFactor, e.LoadFactor, e.IsUnilateral, e.Archived)
	return err
}

// freeSlug is base, or base-2, base-3, … if the user already has it.
func freeSlug(ctx context.Context, tx pgx.Tx, userID uuid.UUID, base string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT slug FROM exercises WHERE user_id=$1 AND (slug = $2 OR slug LIKE $2 || '-%')`, userID, base)
	if err != nil {
		return "", err
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	slug := base
	for n := 2; used[slug]; n++ {
		slug = base + "-" + strconv.Itoa(n)
	}
	return slug, nil
}

// UpdateCustomExercise applies fn to the user's own exercise. ErrForbidden for
// a built-in, ErrNotFound when it is not the user's.
func (s *Store) UpdateCustomExercise(ctx context.Context, userID, id uuid.UUID, fn func(model.Exercise) (model.Exercise, error)) (model.Exercise, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		own, exists, err := owner(ctx, tx, "exercises", id)
		switch {
		case err != nil:
			return err
		case exists && own == nil:
			return ErrForbidden
		case !exists || *own != userID:
			return ErrNotFound
		}
		cur, err := scanExercise(tx.QueryRow(ctx, `SELECT `+exerciseColumns+` FROM exercises WHERE id=$1`, id))
		if err != nil {
			return err
		}
		next, err := fn(cur)
		if err != nil {
			return err
		}
		return writeExercise(ctx, tx, userID, next)
	})
	if err != nil {
		return model.Exercise{}, err
	}
	return s.Exercise(ctx, userID, id)
}

// ---- personal records -----------------------------------------------------------

const prColumns = `exercise_id, kind, value::float8, set_id, session_id, achieved_at`

func scanPR(r pgx.CollectableRow) (model.PR, error) {
	var p model.PR
	err := r.Scan(&p.ExerciseID, &p.Kind, &p.Value, &p.SetID, &p.SessionID, &p.AchievedAt)
	return p, err
}

// ExercisePRs are the user's records for one exercise.
func (s *Store) ExercisePRs(ctx context.Context, userID, exerciseID uuid.UUID) ([]model.PR, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+prColumns+` FROM personal_records WHERE user_id=$1 AND exercise_id=$2 ORDER BY kind`, userID, exerciseID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPR)
}

// SessionPRs are the records a session holds.
func (s *Store) SessionPRs(ctx context.Context, userID, sessionID uuid.UUID) ([]model.PR, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+prColumns+` FROM personal_records WHERE user_id=$1 AND session_id=$2 ORDER BY exercise_id, kind`, userID, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPR)
}

// PRNote is a record with its exercise's name, for the insight cards.
type PRNote struct {
	model.PR
	Exercise string // English name
}

// PRsSince lists records achieved at or after since, newest first.
func (s *Store) PRsSince(ctx context.Context, userID uuid.UUID, since time.Time) ([]PRNote, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.exercise_id, p.kind, p.value::float8, p.set_id, p.session_id, p.achieved_at, e.name_en
		FROM personal_records p JOIN exercises e ON e.id = p.exercise_id
		WHERE p.user_id=$1 AND p.achieved_at >= $2 ORDER BY p.achieved_at DESC`, userID, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PRNote, error) {
		var n PRNote
		err := r.Scan(&n.ExerciseID, &n.Kind, &n.Value, &n.SetID, &n.SessionID, &n.AchievedAt, &n.Exercise)
		return n, err
	})
}

// ReplacePRs makes prs the user's records for one exercise: kinds left out are deleted.
func (s *Store) ReplacePRs(ctx context.Context, userID, exerciseID uuid.UUID, prs []model.PR) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		kinds := make([]string, 0, len(prs))
		for _, p := range prs {
			kinds = append(kinds, p.Kind)
			if _, err := tx.Exec(ctx, `INSERT INTO personal_records (user_id, exercise_id, kind, value, set_id, session_id, achieved_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (user_id, exercise_id, kind) DO UPDATE SET value=EXCLUDED.value, set_id=EXCLUDED.set_id,
				  session_id=EXCLUDED.session_id, achieved_at=EXCLUDED.achieved_at, updated_at=now()
				WHERE (personal_records.value, personal_records.set_id) IS DISTINCT FROM (EXCLUDED.value, EXCLUDED.set_id)`,
				userID, exerciseID, p.Kind, p.Value, p.SetID, p.SessionID, p.AchievedAt); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM personal_records WHERE user_id=$1 AND exercise_id=$2 AND NOT (kind = ANY($3))`,
			userID, exerciseID, kinds)
		return err
	})
}

// LoggedSet is one completed set with what scoring needs around it.
type LoggedSet struct {
	SessionID    uuid.UUID
	ExerciseID   uuid.UUID
	SessionStart time.Time
	Bodyweight   *float64 // the session's
	model.Set
}

// LoggedSets returns the completed sets (completed_at set) of the given
// exercises in completed sessions, plus those of session also (whatever its
// status), that were completed no later than until; oldest first.
func (s *Store) LoggedSets(ctx context.Context, userID uuid.UUID, exerciseIDs []uuid.UUID, until time.Time, also uuid.UUID) ([]LoggedSet, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id, e.exercise_id, s.started_at, s.bodyweight_kg::float8, `+setColumns+`
		FROM session_sets st
		JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		JOIN sessions s ON s.user_id = e.user_id AND s.id = e.session_id
		WHERE st.user_id = $1 AND e.exercise_id = ANY($2) AND st.completed_at IS NOT NULL AND st.completed_at <= $3
		  AND (s.status = 'completed' OR s.id = $4)
		ORDER BY st.completed_at, st.id`, userID, exerciseIDs, until, also)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LoggedSet, error) {
		var l LoggedSet
		err := r.Scan(append([]any{&l.SessionID, &l.ExerciseID, &l.SessionStart, &l.Bodyweight}, setFields(&l.Set)...)...)
		return l, err
	})
}

// LastBodyweight is the latest body weight logged on a session started before t.
func (s *Store) LastBodyweight(ctx context.Context, userID uuid.UUID, before time.Time) (*float64, error) {
	var bw *float64
	err := s.pool.QueryRow(ctx, `SELECT bodyweight_kg::float8 FROM sessions WHERE user_id=$1 AND bodyweight_kg IS NOT NULL
		AND started_at < $2 AND status <> 'discarded' ORDER BY started_at DESC LIMIT 1`, userID, before).Scan(&bw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return bw, err
}

// ---- sessions ----------------------------------------------------------------------

const sessionColumns = `id, title, started_at, ended_at, status, plan_day_id, workout_id, notes, bodyweight_kg::float8,
	muscular_trimp::float8, strain::float8, volume_kg::float8, device_id, updated_at`

func scanSession(r pgx.Row) (model.Session, error) {
	var x model.Session
	err := r.Scan(&x.ID, &x.Title, &x.StartedAt, &x.EndedAt, &x.Status, &x.PlanDayID, &x.WorkoutID, &x.Notes,
		&x.BodyweightKG, &x.MuscularTRIMP, &x.Strain, &x.VolumeKG, &x.DeviceID, &x.UpdatedAt)
	x.Exercises, x.PRs = []model.SessionExercise{}, []model.PR{}
	return x, err
}

const setColumns = `st.id, st.set_index, st.reps, st.weight_kg::float8, st.rpe::float8, st.duration_sec, st.distance_m::float8,
	st.is_warmup, st.completed_at, st.updated_at`

func setFields(x *model.Set) []any {
	return []any{&x.ID, &x.SetIndex, &x.Reps, &x.WeightKG, &x.RPE, &x.DurationSec, &x.DistanceM, &x.IsWarmup, &x.CompletedAt, &x.UpdatedAt}
}

// fillSessions loads the exercises and sets of the given sessions.
func fillSessions(ctx context.Context, q querier, userID uuid.UUID, list []model.Session) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	at := map[uuid.UUID]int{}
	for k, x := range list {
		ids[k], at[x.ID] = x.ID, k
	}
	rows, err := q.Query(ctx, `SELECT session_id, id, exercise_id, plan_exercise_id, position, notes FROM session_exercises
		WHERE user_id=$1 AND session_id = ANY($2) ORDER BY session_id, position, id`, userID, ids)
	if err != nil {
		return err
	}
	type exRow struct {
		session uuid.UUID
		model.SessionExercise
	}
	exs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (exRow, error) {
		var e exRow
		err := r.Scan(&e.session, &e.ID, &e.ExerciseID, &e.PlanExerciseID, &e.Position, &e.Notes)
		e.Sets = []model.Set{}
		return e, err
	})
	if err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT st.session_exercise_id, `+setColumns+` FROM session_sets st
		JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		WHERE st.user_id=$1 AND e.session_id = ANY($2) ORDER BY st.session_exercise_id, st.set_index`, userID, ids)
	if err != nil {
		return err
	}
	type setRow struct {
		exercise uuid.UUID
		model.Set
	}
	sets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (setRow, error) {
		var x setRow
		err := r.Scan(append([]any{&x.exercise}, setFields(&x.Set)...)...)
		return x, err
	})
	if err != nil {
		return err
	}
	byEx := map[uuid.UUID][]model.Set{}
	for _, x := range sets {
		byEx[x.exercise] = append(byEx[x.exercise], x.Set)
	}
	for _, e := range exs {
		if s := byEx[e.ID]; s != nil {
			e.Sets = s
		}
		k := at[e.session]
		list[k].Exercises = append(list[k].Exercises, e.SessionExercise)
	}
	return nil
}

func loadSession(ctx context.Context, q querier, userID, id uuid.UUID, lock bool) (*model.Session, error) {
	sql := `SELECT ` + sessionColumns + ` FROM sessions WHERE user_id=$1 AND id=$2`
	if lock {
		sql += ` FOR UPDATE`
	}
	x, err := scanSession(q.QueryRow(ctx, sql, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []model.Session{x}
	if err := fillSessions(ctx, q, userID, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// Session is one session with its exercises, sets and records; ErrNotFound.
func (s *Store) Session(ctx context.Context, userID, id uuid.UUID) (model.Session, error) {
	x, err := loadSession(ctx, s.pool, userID, id, false)
	if err != nil {
		return model.Session{}, err
	}
	if x == nil {
		return model.Session{}, ErrNotFound
	}
	prs, err := s.SessionPRs(ctx, userID, id)
	if err != nil {
		return model.Session{}, err
	}
	x.PRs = prs
	return *x, nil
}

// CompletedSessionsStartedBetween returns the completed sessions started in
// [from, to), oldest first, with exercises and sets.
func (s *Store) CompletedSessionsStartedBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]model.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sessions
		WHERE user_id=$1 AND status='completed' AND started_at >= $2 AND started_at < $3 ORDER BY started_at, id`, userID, from, to)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Session, error) { return scanSession(r) })
	if err != nil {
		return nil, err
	}
	return list, fillSessions(ctx, s.pool, userID, list)
}

// SaveResult is what SaveSession did.
type SaveResult struct {
	Session  model.Session  // stored copy after the write; the server copy on a conflict
	Created  bool           // the session did not exist
	Conflict bool           // the server had a newer set: nothing was written (409)
	Before   *model.Session // the stored session before the write, nil if new
	NewSets  int            // completed sets that were not stored before
}

// lockSession serialises saves of one session, also before its row exists.
const lockSession = `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`

// SaveSession applies a PUT /v1/sessions/{id} body (validated with
// train.CheckSession) under train.Merge, in one transaction.
func (s *Store) SaveSession(ctx context.Context, userID uuid.UUID, body model.Session) (SaveResult, error) {
	var res SaveResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockSession, userID, body.ID); err != nil {
			return err
		}
		stored, err := loadSession(ctx, tx, userID, body.ID, true)
		if err != nil {
			return err
		}
		res.Before, res.Created = stored, stored == nil
		switch {
		case body.Status == model.SessionCompleted && (stored == nil || stored.Status != model.SessionCompleted):
			return train.Problem{Msg: "use POST /v1/sessions/{id}/complete to finish a session"}
		case stored != nil && stored.Status == model.SessionCompleted && body.Status == model.SessionInProgress:
			return train.Problem{Msg: "a completed session cannot be reopened; save it with status completed or discarded"}
		}
		if err := checkReferences(ctx, tx, userID, body); err != nil {
			return err
		}
		merged, conflict := train.Merge(stored, body)
		if conflict {
			res.Conflict, res.Session = true, *stored
			return nil
		}
		res.NewSets = newCompletedSets(stored, merged)
		return writeSession(ctx, tx, userID, merged, stored != nil && stored.Status == model.SessionCompleted)
	})
	if err != nil || res.Conflict {
		if res.Conflict {
			prs, perr := s.SessionPRs(ctx, userID, body.ID)
			res.Session.PRs = prs
			err = perr
		}
		return res, err
	}
	res.Session, err = s.Session(ctx, userID, body.ID)
	return res, err
}

func newCompletedSets(stored *model.Session, merged model.Session) int {
	had := map[uuid.UUID]bool{}
	if stored != nil {
		for _, e := range stored.Exercises {
			for _, st := range e.Sets {
				had[st.ID] = st.CompletedAt != nil
			}
		}
	}
	n := 0
	for _, e := range merged.Exercises {
		for _, st := range e.Sets {
			if st.CompletedAt != nil && !had[st.ID] {
				n++
			}
		}
	}
	return n
}

// checkReferences: every exercise, the plan day and the plan exercises must
// be built-in or the user's (a train.Problem otherwise).
func checkReferences(ctx context.Context, tx pgx.Tx, userID uuid.UUID, body model.Session) error {
	var exIDs, peIDs []uuid.UUID
	for _, e := range body.Exercises {
		exIDs = append(exIDs, e.ExerciseID)
		if e.PlanExerciseID != nil {
			peIDs = append(peIDs, *e.PlanExerciseID)
		}
	}
	vis, err := visibleExercises(ctx, tx, userID, exIDs)
	if err != nil {
		return err
	}
	for _, id := range exIDs {
		if _, ok := vis[id]; !ok {
			return train.Problem{Msg: fmt.Sprintf("exercise %s is neither built-in nor yours", id)}
		}
	}
	if body.PlanDayID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plan_days WHERE id=$2 AND (user_id IS NULL OR user_id=$1))`,
			userID, *body.PlanDayID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return train.Problem{Msg: "plan_day_id is neither a built-in plan's nor one of yours"}
		}
	}
	if len(peIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT id) FROM plan_exercises WHERE id = ANY($2) AND (user_id IS NULL OR user_id=$1)`,
			userID, peIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(uniq(peIDs)) {
			return train.Problem{Msg: "a plan_exercise_id is neither a built-in plan's nor one of yours"}
		}
	}
	return nil
}

func uniq(ids []uuid.UUID) map[uuid.UUID]bool {
	m := map[uuid.UUID]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// writeSession upserts the merged document. replace deletes exercises and sets
// the document no longer has (a completed session is replaced whole).
func writeSession(ctx context.Context, tx pgx.Tx, userID uuid.UUID, x model.Session, replace bool) error {
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO sessions (user_id, id, title, started_at, ended_at, status, plan_day_id, notes, bodyweight_kg, device_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (user_id, id) DO UPDATE SET title=EXCLUDED.title, started_at=EXCLUDED.started_at, ended_at=EXCLUDED.ended_at,
		  status=EXCLUDED.status, plan_day_id=EXCLUDED.plan_day_id, notes=EXCLUDED.notes, bodyweight_kg=EXCLUDED.bodyweight_kg,
		  device_id=EXCLUDED.device_id, updated_at=now()`,
		userID, x.ID, x.Title, x.StartedAt, x.EndedAt, x.Status, x.PlanDayID, x.Notes, x.BodyweightKG, x.DeviceID)
	var exIDs, setIDs []uuid.UUID
	for _, e := range x.Exercises {
		exIDs = append(exIDs, e.ID)
		// A session exercise id is unique per user; one that belongs to another
		// of the user's sessions is not taken over (the WHERE), which the row
		// count below turns into an error.
		b.Queue(`INSERT INTO session_exercises (user_id, id, session_id, exercise_id, plan_exercise_id, position, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_id, id) DO UPDATE SET exercise_id=EXCLUDED.exercise_id, plan_exercise_id=EXCLUDED.plan_exercise_id,
			  position=EXCLUDED.position, notes=EXCLUDED.notes, updated_at=now()
			WHERE session_exercises.session_id = EXCLUDED.session_id`,
			userID, e.ID, x.ID, e.ExerciseID, e.PlanExerciseID, e.Position, e.Notes)
		for _, st := range e.Sets {
			setIDs = append(setIDs, st.ID)
			b.Queue(`INSERT INTO session_sets (user_id, id, session_exercise_id, set_index, reps, weight_kg, rpe, duration_sec,
				distance_m, is_warmup, completed_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
				ON CONFLICT (user_id, id) DO UPDATE SET session_exercise_id=EXCLUDED.session_exercise_id, set_index=EXCLUDED.set_index,
				  reps=EXCLUDED.reps, weight_kg=EXCLUDED.weight_kg, rpe=EXCLUDED.rpe, duration_sec=EXCLUDED.duration_sec,
				  distance_m=EXCLUDED.distance_m, is_warmup=EXCLUDED.is_warmup, completed_at=EXCLUDED.completed_at,
				  updated_at=EXCLUDED.updated_at
				WHERE session_sets.session_exercise_id IN (SELECT id FROM session_exercises WHERE user_id=$1 AND session_id=$13)`,
				userID, st.ID, e.ID, st.SetIndex, st.Reps, st.WeightKG, st.RPE, st.DurationSec, st.DistanceM, st.IsWarmup,
				st.CompletedAt, st.UpdatedAt, x.ID)
		}
	}
	if replace {
		b.Queue(`DELETE FROM session_sets st USING session_exercises e
			WHERE st.user_id=$1 AND e.user_id=$1 AND e.id=st.session_exercise_id AND e.session_id=$2 AND NOT (st.id = ANY($3))`,
			userID, x.ID, setIDs)
		b.Queue(`DELETE FROM session_exercises WHERE user_id=$1 AND session_id=$2 AND NOT (id = ANY($3))`, userID, x.ID, exIDs)
	}
	res := tx.SendBatch(ctx, b)
	defer res.Close()
	for k := 0; k < b.Len(); k++ {
		tag, err := res.Exec()
		if err != nil {
			return err
		}
		if k > 0 && tag.RowsAffected() == 0 && (!replace || k < b.Len()-2) {
			return train.Problem{Msg: "an exercise or set id belongs to another of your sessions"}
		}
	}
	return res.Close()
}

// Completion is what finishing a session stores (computed by the caller).
type Completion struct {
	EndedAt       time.Time
	BodyweightKG  *float64 // nil keeps the stored value
	MuscularTRIMP float64
	Strain        float64
	VolumeKG      float64
	WorkoutID     uuid.UUID
}

// CompleteSession marks a session completed with its load. ErrNotFound when
// there is no such session, ErrTaken when it is discarded.
func (s *Store) CompleteSession(ctx context.Context, userID, id uuid.UUID, c Completion) error {
	return s.CompleteSessionWith(ctx, userID, id, c, nil)
}

// CompleteSessionWith is CompleteSession with also run in the same transaction: the
// session.completed event written into the outbox (D101).
func (s *Store) CompleteSessionWith(ctx context.Context, userID, id uuid.UUID, c Completion, also func(pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `UPDATE sessions SET status='completed', ended_at=$3, bodyweight_kg=COALESCE($4, bodyweight_kg),
			muscular_trimp=$5, strain=$6, volume_kg=$7, workout_id=$8, updated_at=now()
			WHERE user_id=$1 AND id=$2 AND status <> 'discarded' RETURNING status`,
			userID, id, c.EndedAt, c.BodyweightKG, c.MuscularTRIMP, c.Strain, c.VolumeKG, c.WorkoutID).Scan(&status); err != nil {
			return err
		}
		if also != nil {
			return also(tx)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE user_id=$1 AND id=$2)`, userID, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrTaken
		}
		return ErrNotFound
	}
	return err
}

// SetSessionLoad is scoring's write: the load it computed and the workout it
// attached the session to.
func (s *Store) SetSessionLoad(ctx context.Context, userID, id uuid.UUID, trimp, strain, volume float64, workoutID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET muscular_trimp=$3, strain=$4, volume_kg=$5, workout_id=$6
		WHERE user_id=$1 AND id=$2 AND (muscular_trimp, strain, volume_kg, workout_id) IS DISTINCT FROM ($3::float8, $4::float8, $5::float8, $6::uuid)`,
		userID, id, trimp, strain, volume, workoutID)
	return err
}

// DeleteSession removes a session with its sets and returns it as it was;
// ErrNotFound when there is none.
func (s *Store) DeleteSession(ctx context.Context, userID, id uuid.UUID) (model.Session, error) {
	var out model.Session
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockSession, userID, id); err != nil {
			return err
		}
		x, err := loadSession(ctx, tx, userID, id, true)
		if err != nil {
			return err
		}
		if x == nil {
			return ErrNotFound
		}
		out = *x
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND id=$2`, userID, id)
		return err
	})
	return out, err
}

// SessionSummaries lists sessions started in [from, to), newest first (or
// oldest first with ascending), optionally of one status ("" = all).
func (s *Store) SessionSummaries(ctx context.Context, userID uuid.UUID, from, to time.Time, status string, ascending bool) ([]model.SessionSummary, error) {
	order := "DESC"
	if ascending {
		order = "ASC"
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.title, s.started_at, s.ended_at, s.status, s.plan_day_id, s.workout_id,
		  CASE WHEN s.ended_at IS NULL OR s.status = 'in_progress' THEN NULL
		       ELSE round(extract(epoch FROM s.ended_at - s.started_at) / 60)::int END,
		  (SELECT count(*) FROM session_exercises e WHERE e.user_id = s.user_id AND e.session_id = s.id),
		  (SELECT count(*) FROM session_sets st JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		     WHERE e.user_id = s.user_id AND e.session_id = s.id AND st.completed_at IS NOT NULL AND NOT st.is_warmup),
		  s.volume_kg::float8, s.muscular_trimp::float8, s.strain::float8,
		  (SELECT count(*) FROM personal_records p WHERE p.user_id = s.user_id AND p.session_id = s.id)
		FROM sessions s WHERE s.user_id=$1 AND s.started_at >= $2 AND s.started_at < $3 AND ($4 = '' OR s.status = $4)
		ORDER BY s.started_at `+order+`, s.id`, userID, from, to, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.SessionSummary, error) {
		var x model.SessionSummary
		err := r.Scan(&x.ID, &x.Title, &x.StartedAt, &x.EndedAt, &x.Status, &x.PlanDayID, &x.WorkoutID, &x.DurationMin,
			&x.ExerciseCount, &x.SetCount, &x.VolumeKG, &x.MuscularTRIMP, &x.Strain, &x.PRCount)
		return x, err
	})
}

// HistorySession is one completed session's sets of a single exercise.
type HistorySession struct {
	SessionID  uuid.UUID
	StartedAt  time.Time
	Bodyweight *float64
	Sets       []model.Set
}

// ExerciseSessions returns the latest limit completed sessions with the
// exercise, newest first, with that exercise's sets.
func (s *Store) ExerciseSessions(ctx context.Context, userID, exerciseID uuid.UUID, limit int) ([]HistorySession, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.started_at, s.bodyweight_kg::float8 FROM sessions s
		WHERE s.user_id=$1 AND s.status='completed' AND EXISTS (SELECT 1 FROM session_exercises e
		  WHERE e.user_id = s.user_id AND e.session_id = s.id AND e.exercise_id = $2)
		ORDER BY s.started_at DESC LIMIT $3`, userID, exerciseID, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (HistorySession, error) {
		var h HistorySession
		err := r.Scan(&h.SessionID, &h.StartedAt, &h.Bodyweight)
		h.Sets = []model.Set{}
		return h, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]uuid.UUID, len(list))
	at := map[uuid.UUID]int{}
	for k, h := range list {
		ids[k], at[h.SessionID] = h.SessionID, k
	}
	rows, err = s.pool.Query(ctx, `SELECT e.session_id, `+setColumns+` FROM session_sets st
		JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		WHERE st.user_id=$1 AND e.session_id = ANY($2) AND e.exercise_id = $3
		ORDER BY e.session_id, e.position, st.set_index`, userID, ids, exerciseID)
	if err != nil {
		return nil, err
	}
	type row struct {
		session uuid.UUID
		model.Set
	}
	sets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(append([]any{&x.session}, setFields(&x.Set)...)...)
		return x, err
	})
	for _, x := range sets {
		k := at[x.session]
		list[k].Sets = append(list[k].Sets, x.Set)
	}
	return list, err
}

// Attempt is one completed session's sets for a plan exercise.
type Attempt struct {
	StartedAt time.Time
	Sets      []model.Set
}

// PlanExerciseAttempts returns the completed sessions that worked on a plan
// exercise, oldest first: session exercises that name it, or, for sessions
// that name none, the same exercise on a session started from its plan day.
func (s *Store) PlanExerciseAttempts(ctx context.Context, userID uuid.UUID, pe model.PlanExercise, planDayID uuid.UUID) ([]Attempt, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.started_at, e.id, `+setColumns+`
		FROM sessions s
		JOIN session_exercises e ON e.user_id = s.user_id AND e.session_id = s.id
		JOIN session_sets st ON st.user_id = e.user_id AND st.session_exercise_id = e.id
		WHERE s.user_id=$1 AND s.status='completed'
		  AND (e.plan_exercise_id = $2 OR (e.plan_exercise_id IS NULL AND s.plan_day_id = $3 AND e.exercise_id = $4))
		ORDER BY s.started_at, e.id, st.set_index`, userID, pe.ID, planDayID, pe.ExerciseID)
	if err != nil {
		return nil, err
	}
	var out []Attempt
	var last uuid.UUID
	_, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (struct{}, error) {
		var started time.Time
		var ex uuid.UUID
		var st model.Set
		if err := r.Scan(append([]any{&started, &ex}, setFields(&st)...)...); err != nil {
			return struct{}{}, err
		}
		if len(out) == 0 || ex != last {
			out = append(out, Attempt{StartedAt: started})
			last = ex
		}
		out[len(out)-1].Sets = append(out[len(out)-1].Sets, st)
		return struct{}{}, nil
	})
	return out, err
}

// LastWorkingSets are the completed working sets of the exercise in the most
// recent completed session that has any; [] when none.
func (s *Store) LastWorkingSets(ctx context.Context, userID, exerciseID uuid.UUID) ([]model.Set, error) {
	rows, err := s.pool.Query(ctx, `WITH last AS (
		  SELECT s.id FROM sessions s JOIN session_exercises e ON e.user_id = s.user_id AND e.session_id = s.id
		  WHERE s.user_id=$1 AND s.status='completed' AND e.exercise_id=$2 ORDER BY s.started_at DESC LIMIT 1)
		SELECT `+setColumns+` FROM session_sets st
		JOIN session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
		WHERE st.user_id=$1 AND e.session_id = (SELECT id FROM last) AND e.exercise_id=$2
		  AND st.completed_at IS NOT NULL AND NOT st.is_warmup
		ORDER BY e.position, st.set_index`, userID, exerciseID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Set, error) {
		var x model.Set
		err := r.Scan(setFields(&x)...)
		return x, err
	})
	if out == nil {
		out = []model.Set{}
	}
	return out, err
}

// ---- workouts carrying a session's load (scoring) ------------------------------------

// AttachSession puts a session's load on one of the phone's workouts.
func (s *Store) AttachSession(ctx context.Context, userID, workoutID, sessionID uuid.UUID, load float64) error {
	_, err := s.pool.Exec(ctx, `UPDATE workouts SET muscular_load=$4, session_id=$3 WHERE user_id=$1 AND id=$2
		AND (muscular_load, session_id) IS DISTINCT FROM ($4::float8, $3::uuid)`, userID, workoutID, sessionID, load)
	return err
}

// EnsureSessionWorkout makes sure the workout scoring owns for a session (no
// matching HealthKit workout) exists with the session's times and load.
func (s *Store) EnsureSessionWorkout(ctx context.Context, userID, workoutID, sessionID uuid.UUID, start, end time.Time, load float64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var a, b time.Time
		err := tx.QueryRow(ctx, `SELECT start_at, end_at FROM workouts WHERE user_id=$1 AND id=$2 AND source=$3`,
			userID, workoutID, model.SessionSource).Scan(&a, &b)
		switch {
		case err == nil && a.Equal(start) && b.Equal(end):
			_, err = tx.Exec(ctx, `UPDATE workouts SET muscular_load=$3, session_id=$4 WHERE user_id=$1 AND id=$2
				AND (muscular_load, session_id) IS DISTINCT FROM ($3::float8, $4::uuid)`, userID, workoutID, load, sessionID)
			return err
		case err == nil:
			// Times changed: scoring may not move a workout, only replace its own.
			if _, err := tx.Exec(ctx, `DELETE FROM workouts WHERE user_id=$1 AND id=$2 AND source=$3`, userID, workoutID, model.SessionSource); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO workouts (user_id, id, activity, start_at, end_at, source, session_id, muscular_load)
			VALUES ($1,$2,'strength',$3,$4,$5,$6,$7)`, userID, workoutID, start, end, model.SessionSource, sessionID, load)
		return err
	})
}

// ReleaseSessionWorkouts undoes stale attachments after a day's pass: a
// workout keeps a session's load only if the session is completed and, when it
// is one of daySessions, the workout is the one chosen for it (chosen maps
// session → workout). Workouts scoring created for such sessions go.
func (s *Store) ReleaseSessionWorkouts(ctx context.Context, userID uuid.UUID, daySessions []uuid.UUID, chosen map[uuid.UUID]uuid.UUID) error {
	keep := make([]uuid.UUID, 0, len(chosen))
	for _, w := range chosen {
		keep = append(keep, w)
	}
	stale := `session_id IS NOT NULL AND (
		NOT EXISTS (SELECT 1 FROM sessions s WHERE s.user_id = workouts.user_id AND s.id = workouts.session_id AND s.status = 'completed')
		OR (session_id = ANY($2) AND NOT (id = ANY($3))))`
	if _, err := s.pool.Exec(ctx, `UPDATE workouts SET muscular_load=NULL, session_id=NULL
		WHERE user_id=$1 AND source <> '`+model.SessionSource+`' AND `+stale, userID, daySessions, keep); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM workouts WHERE user_id=$1 AND source = '`+model.SessionSource+`'
		AND (session_id IS NULL OR `+stale+`)`, userID, daySessions, keep)
	return err
}

// ---- plans ----------------------------------------------------------------------------

const planColumns = `id, slug, name, goal, weeks, days_per_week, description, progression, user_id IS NULL, archived`

func scanPlanHead(r pgx.Row, p *model.Plan) error {
	var prog []byte
	if err := r.Scan(&p.ID, &p.Slug, &p.Name, &p.Goal, &p.Weeks, &p.DaysPerWeek, &p.Description, &prog, &p.IsBuiltin, &p.Archived); err != nil {
		return err
	}
	if len(prog) > 0 && string(prog) != "null" {
		p.Progression = &model.Progression{}
		if err := json.Unmarshal(prog, p.Progression); err != nil {
			return fmt.Errorf("plan %s progression: %w", p.ID, err)
		}
	}
	return nil
}

// PlanSummaries lists the built-in plans and the user's own (not archived).
func (s *Store) PlanSummaries(ctx context.Context, userID uuid.UUID) (builtin, mine []model.PlanSummary, err error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.slug, p.name, p.goal, p.weeks, p.days_per_week, p.description,
		  p.user_id IS NULL, p.archived,
		  (SELECT count(*) FROM plan_days d WHERE d.plan_id = p.id),
		  (SELECT count(*) FROM plan_exercises e JOIN plan_days d ON d.id = e.plan_day_id WHERE d.plan_id = p.id)
		FROM plans p WHERE (p.user_id IS NULL AND NOT p.archived) OR (p.user_id = $1 AND NOT p.archived)
		ORDER BY p.user_id IS NOT NULL, p.name`, userID)
	if err != nil {
		return nil, nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.PlanSummary, error) {
		var x model.PlanSummary
		err := r.Scan(&x.ID, &x.Slug, &x.Name, &x.Goal, &x.Weeks, &x.DaysPerWeek, &x.Description, &x.IsBuiltin,
			&x.Archived, &x.DayCount, &x.ExerciseCount)
		return x, err
	})
	builtin, mine = []model.PlanSummary{}, []model.PlanSummary{}
	for _, x := range all {
		if x.IsBuiltin {
			builtin = append(builtin, x)
		} else {
			mine = append(mine, x)
		}
	}
	return builtin, mine, err
}

// Plan is a whole plan the user can see, its exercises resolved; ErrNotFound.
func (s *Store) Plan(ctx context.Context, userID, id uuid.UUID) (model.Plan, error) {
	var p model.Plan
	err := scanPlanHead(s.pool.QueryRow(ctx, `SELECT `+planColumns+` FROM plans WHERE id=$2 AND (user_id IS NULL OR user_id=$1)`, userID, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, week, day_index, name FROM plan_days WHERE plan_id=$1
		ORDER BY week NULLS FIRST, day_index`, id)
	if err != nil {
		return p, err
	}
	p.Days, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.PlanDay, error) {
		var d model.PlanDay
		err := r.Scan(&d.ID, &d.Week, &d.DayIndex, &d.Name)
		d.Exercises = []model.PlanExercise{}
		return d, err
	})
	if err != nil {
		return p, err
	}
	rows, err = s.pool.Query(ctx, `SELECT e.plan_day_id, e.id, e.exercise_id, e.position, e.sets, e.reps_min, e.reps_max,
		  e.target_kind, e.target_value::float8, e.rest_sec, e.superset_group, e.notes
		FROM plan_exercises e JOIN plan_days d ON d.id = e.plan_day_id WHERE d.plan_id=$1 ORDER BY e.plan_day_id, e.position`, id)
	if err != nil {
		return p, err
	}
	type row struct {
		day uuid.UUID
		model.PlanExercise
	}
	exs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.day, &x.ID, &x.ExerciseID, &x.Position, &x.Sets, &x.RepsMin, &x.RepsMax, &x.TargetKind,
			&x.TargetValue, &x.RestSec, &x.SupersetGroup, &x.Notes)
		return x, err
	})
	if err != nil {
		return p, err
	}
	var ids []uuid.UUID
	for _, x := range exs {
		ids = append(ids, x.ExerciseID)
	}
	resolved, err := s.VisibleExercises(ctx, userID, ids)
	if err != nil {
		return p, err
	}
	at := map[uuid.UUID]int{}
	for k, d := range p.Days {
		at[d.ID] = k
	}
	for _, x := range exs {
		if e, ok := resolved[x.ExerciseID]; ok {
			x.Exercise = &e
		}
		k := at[x.day]
		p.Days[k].Exercises = append(p.Days[k].Exercises, x.PlanExercise)
	}
	return p, nil
}

// SavePlan creates or replaces the user's plan (validated with
// train.CheckPlan). ErrForbidden for a built-in id, ErrTaken for another
// user's; a train.Problem when an exercise is not visible to the user.
func (s *Store) SavePlan(ctx context.Context, userID uuid.UUID, p model.Plan) (bool, error) {
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		own, exists, err := owner(ctx, tx, "plans", p.ID)
		switch {
		case err != nil:
			return err
		case exists && own == nil:
			return ErrForbidden
		case exists && *own != userID:
			return ErrTaken
		}
		created = !exists
		var exIDs []uuid.UUID
		for _, d := range p.Days {
			for _, e := range d.Exercises {
				exIDs = append(exIDs, e.ExerciseID)
			}
		}
		vis, err := visibleExercises(ctx, tx, userID, exIDs)
		if err != nil {
			return err
		}
		for _, id := range exIDs {
			if _, ok := vis[id]; !ok {
				return train.Problem{Msg: fmt.Sprintf("exercise %s is neither built-in nor yours", id)}
			}
		}
		prog, err := json.Marshal(p.Progression)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		b.Queue(`INSERT INTO plans (id, user_id, name, goal, weeks, days_per_week, description, progression, archived)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, goal=EXCLUDED.goal, weeks=EXCLUDED.weeks,
			  days_per_week=EXCLUDED.days_per_week, description=EXCLUDED.description, progression=EXCLUDED.progression,
			  archived=EXCLUDED.archived, updated_at=now() WHERE plans.user_id = EXCLUDED.user_id`,
			p.ID, userID, p.Name, p.Goal, p.Weeks, p.DaysPerWeek, p.Description, prog, p.Archived)
		var dayIDs, peIDs []uuid.UUID
		for _, d := range p.Days {
			dayIDs = append(dayIDs, d.ID)
			b.Queue(`INSERT INTO plan_days (id, plan_id, user_id, week, day_index, name) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (id) DO UPDATE SET week=EXCLUDED.week, day_index=EXCLUDED.day_index, name=EXCLUDED.name, updated_at=now()
				WHERE plan_days.plan_id = EXCLUDED.plan_id`, d.ID, p.ID, userID, d.Week, d.DayIndex, d.Name)
			for _, e := range d.Exercises {
				peIDs = append(peIDs, e.ID)
				b.Queue(`INSERT INTO plan_exercises (id, plan_day_id, user_id, exercise_id, position, sets, reps_min, reps_max,
					target_kind, target_value, rest_sec, superset_group, notes) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
					ON CONFLICT (id) DO UPDATE SET plan_day_id=EXCLUDED.plan_day_id, exercise_id=EXCLUDED.exercise_id,
					  position=EXCLUDED.position, sets=EXCLUDED.sets, reps_min=EXCLUDED.reps_min, reps_max=EXCLUDED.reps_max,
					  target_kind=EXCLUDED.target_kind, target_value=EXCLUDED.target_value, rest_sec=EXCLUDED.rest_sec,
					  superset_group=EXCLUDED.superset_group, notes=EXCLUDED.notes, updated_at=now()
					WHERE plan_exercises.user_id = EXCLUDED.user_id
					  AND plan_exercises.plan_day_id IN (SELECT id FROM plan_days WHERE plan_id=$14)`,
					e.ID, d.ID, userID, e.ExerciseID, e.Position, e.Sets, e.RepsMin, e.RepsMax, e.TargetKind, e.TargetValue,
					e.RestSec, e.SupersetGroup, e.Notes, p.ID)
			}
		}
		n := b.Len()
		b.Queue(`DELETE FROM plan_exercises WHERE plan_day_id IN (SELECT id FROM plan_days WHERE plan_id=$1) AND NOT (id = ANY($2))`, p.ID, peIDs)
		b.Queue(`DELETE FROM plan_days WHERE plan_id=$1 AND NOT (id = ANY($2))`, p.ID, dayIDs)
		res := tx.SendBatch(ctx, b)
		defer res.Close()
		for k := 0; k < b.Len(); k++ {
			tag, err := res.Exec()
			if err != nil {
				return err
			}
			if k < n && tag.RowsAffected() == 0 {
				return ErrTaken // a day or exercise id that belongs to another plan
			}
		}
		return res.Close()
	})
	return created, err
}

// DeletePlan removes the user's plan with its days, exercises and enrolments.
func (s *Store) DeletePlan(ctx context.Context, userID, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		own, exists, err := owner(ctx, tx, "plans", id)
		switch {
		case err != nil:
			return err
		case exists && own == nil:
			return ErrForbidden
		case !exists || *own != userID:
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM plans WHERE id=$1 AND user_id=$2`, id, userID)
		return err
	})
}

// ---- enrolments -------------------------------------------------------------------------

const enrollmentColumns = `id, plan_id, started_at, current_week, current_day_index, status`

func scanEnrollment(r pgx.Row) (model.Enrollment, error) {
	var e model.Enrollment
	err := r.Scan(&e.ID, &e.PlanID, &e.StartedAt, &e.CurrentWeek, &e.CurrentDayIndex, &e.Status)
	return e, err
}

// ActiveEnrollment is the user's active enrolment, or nil.
func (s *Store) ActiveEnrollment(ctx context.Context, userID uuid.UUID) (*model.Enrollment, error) {
	e, err := scanEnrollment(s.pool.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM plan_enrollments WHERE user_id=$1 AND status='active'`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &e, err
}

// lockEnrollments serialises a user's enrolment changes.
const lockEnrollments = `SELECT pg_advisory_xact_lock(7419004, hashtext($1::text))`

// Enroll makes the plan the user's active one: an active enrolment in it is
// returned as is, a paused one resumes, otherwise a new one starts at week 1,
// day 0 (created). Another active enrolment is paused. ErrNotFound when the
// plan is not visible.
func (s *Store) Enroll(ctx context.Context, userID, planID uuid.UUID) (model.Enrollment, bool, error) {
	var out model.Enrollment
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockEnrollments, userID); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans WHERE id=$2 AND (user_id IS NULL OR user_id=$1))`, userID, planID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		cur, err := scanEnrollment(tx.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM plan_enrollments WHERE user_id=$1 AND status='active'`, userID))
		switch {
		case err == nil && cur.PlanID == planID:
			out = cur
			return nil
		case err == nil:
			if _, err := tx.Exec(ctx, `UPDATE plan_enrollments SET status='paused', updated_at=now() WHERE id=$1`, cur.ID); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		out, err = scanEnrollment(tx.QueryRow(ctx, `UPDATE plan_enrollments SET status='active', updated_at=now()
			WHERE id = (SELECT id FROM plan_enrollments WHERE user_id=$1 AND plan_id=$2 AND status='paused' ORDER BY updated_at DESC LIMIT 1)
			RETURNING `+enrollmentColumns, userID, planID))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		created = true
		out, err = scanEnrollment(tx.QueryRow(ctx, `INSERT INTO plan_enrollments (id, user_id, plan_id, status)
			VALUES ($1,$2,$3,'active') RETURNING `+enrollmentColumns, uuid.New(), userID, planID))
		return err
	})
	return out, created, err
}

// PauseEnrollment pauses the active enrolment, if any.
func (s *Store) PauseEnrollment(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE plan_enrollments SET status='paused', updated_at=now() WHERE user_id=$1 AND status='active'`, userID)
	return err
}

// CurrentPlanDay resolves the enrolment's current day: the plan day of its
// week, or the every-week day with its index; ErrNotFound when there is none.
func CurrentPlanDay(p model.Plan, e model.Enrollment) (model.PlanDay, error) {
	var every *model.PlanDay
	for k, d := range p.Days {
		if d.DayIndex != e.CurrentDayIndex {
			continue
		}
		if d.Week != nil && *d.Week == e.CurrentWeek {
			return d, nil
		}
		if d.Week == nil {
			every = &p.Days[k]
		}
	}
	if every != nil {
		return *every, nil
	}
	return model.PlanDay{}, ErrNotFound
}

// AdvanceEnrollment moves the active enrolment past planDayID when that is its
// current day: to the next day, the next week, or completed after the last day
// of a plan with weeks.
func (s *Store) AdvanceEnrollment(ctx context.Context, userID, planDayID uuid.UUID) error {
	e, err := s.ActiveEnrollment(ctx, userID)
	if err != nil || e == nil {
		return err
	}
	p, err := s.Plan(ctx, userID, e.PlanID)
	if err != nil {
		return err
	}
	if d, err := CurrentPlanDay(p, *e); err != nil || d.ID != planDayID {
		return nil //nolint:nilerr // not the current day: nothing to advance
	}
	week, idx, status := e.CurrentWeek, e.CurrentDayIndex+1, model.EnrollmentActive
	if idx >= p.DaysPerWeek {
		week, idx = week+1, 0
	}
	if p.Weeks != nil && week > *p.Weeks {
		week, idx, status = *p.Weeks, p.DaysPerWeek-1, model.EnrollmentCompleted
	}
	_, err = s.pool.Exec(ctx, `UPDATE plan_enrollments SET current_week=$3, current_day_index=$4, status=$5, updated_at=now()
		WHERE id=$1 AND user_id=$2 AND current_week=$6 AND current_day_index=$7`,
		e.ID, userID, week, idx, status, e.CurrentWeek, e.CurrentDayIndex)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
