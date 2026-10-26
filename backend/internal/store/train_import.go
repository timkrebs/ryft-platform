package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// The training import (POST /v1/train/import, D124-D133). The handler decides what is
// valid (train.CheckPlan, train.CheckImportSession); this file stores one item at a time
// and answers what happened to it.

// Import item statuses and rejection codes (docs/API.md → ImportResult).
const (
	ImportCreated   = "created"
	ImportUnchanged = "unchanged"
	ImportUpdated   = "updated"
	ImportRejected  = "rejected"

	ImportInvalid              = "invalid"
	ImportUnknownExercise      = "unknown_exercise"
	ImportUnknownPlanReference = "unknown_plan_reference"
	ImportIDTaken              = "id_taken"
	ImportNewerOnServer        = "newer_on_server"
)

// importRetryBackoff is the pause before the one retry of a deadlocked item.
const importRetryBackoff = 25 * time.Millisecond

// ErrImportRules: the store was built without the comparison rules of internal/train.
var ErrImportRules = errors.New("store: import rules not set (WithImportRules)")

// ImportRules are the pure comparisons of the import (docs/work/2026-10-26-train-import/design.md
// §3: train.SameSession, train.SamePlan, train.ImportConflict). The store takes them as
// values so it does not decide what "identical" means (D126); main wires the train functions in.
type ImportRules struct {
	// SameSession: the stored session equals the request's after the same clamping and defaults.
	SameSession func(stored, body model.Session) bool
	// Conflict: the app changed the stored session after the import (edited since the import
	// wrote it, a set with a later updated_at than the body's, or the stored status is not
	// completed): leave it alone.
	Conflict func(stored, body model.Session) bool
	// SamePlan: the stored plan equals the request's.
	SamePlan func(stored, body model.Plan) bool
	// PlanConflict: the app changed the stored plan after the import (or made it): leave it alone.
	PlanConflict func(stored model.Plan) bool
}

// WithImportRules sets the comparisons ImportSession and ImportPlan use and returns s.
func (s *Store) WithImportRules(r ImportRules) *Store {
	s.rules = r
	return s
}

func (s *Store) importRules() (ImportRules, error) {
	r := s.rules
	if r.SameSession == nil || r.Conflict == nil || r.SamePlan == nil || r.PlanConflict == nil {
		return r, ErrImportRules
	}
	return r, nil
}

// DayWindow is the local days a request may queue for scoring: From..To inclusive,
// "2006-01-02" strings (the 400 days ending today, as a sync's window, D128).
type DayWindow struct{ From, To string }

// Contains says whether the local day is inside the window.
func (w DayWindow) Contains(day string) bool { return w.To != "" && day >= w.From && day <= w.To }

// start is the first instant of From in loc.
func (w DayWindow) start(loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", w.From, loc)
}

// ImportOutcome is what happened to one item.
type ImportOutcome struct {
	Status  string // created | unchanged | updated | rejected
	Code    string // rejected only: invalid | unknown_exercise | unknown_plan_reference | id_taken | newer_on_server
	Message string // rejected only: ids and field names, never values (D114)
	// Day is the local day of the session's started_at ("" for a plan), whether or not
	// it is inside the window. PreviousDay is the local day the replaced version was on,
	// when an update moved the session to another day: that day lost a session too, so the
	// caller adds it to the days it rescores. "" otherwise.
	Day, PreviousDay string
	// Exercises are the exercise ids of the stored version and, for an update, of the
	// replaced one (deduplicated): the records to recompute.
	Exercises []uuid.UUID
	// Replaced is the stored version an update replaced (nil otherwise): its exercises,
	// start and body weight mattered to later sessions too (scoring.ScopeOf, D135).
	Replaced *model.Session
}

// msgPlanIDTaken is id_taken's message for a plan, whoever holds the id.
const msgPlanIDTaken = "the plan id, or the id of one of its days or exercises, is already in use"

// importReject aborts an item's transaction with a rejection: nothing of the item stays.
type importReject struct{ code, msg string }

func (e importReject) Error() string { return e.code + ": " + e.msg }

// retryable: a serialization failure or deadlock, which a fresh transaction survives.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// rejectable: a value the database refuses (class 22 data exception, not-null and check
// violations). It will fail the same way next time, so it is the item's fault, not a 503.
// Foreign-key violations are not: they mean a row went away between the check and the write,
// and the retry finds the precise answer.
func rejectable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code[:2] == "22" || pgErr.Code == "23502" || pgErr.Code == "23514"
}

// unit runs fn as one item's own transaction (D131): committed or rolled back as a whole,
// independent of every other item. A deadlock or serialization failure is retried once.
func (s *Store) unit(ctx context.Context, fn func(tx pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, s.pool, fn)
	if retryable(err) {
		select {
		case <-time.After(importRetryBackoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		err = pgx.BeginFunc(ctx, s.pool, fn)
	}
	return err
}

// rejected turns an item error into an outcome; ok false when it is a real failure
// (connection, timeout, deadlock after the retry) that must stop the request.
func rejected(err error) (ImportOutcome, bool) {
	var rj importReject
	switch {
	case errors.As(err, &rj):
		return ImportOutcome{Status: ImportRejected, Code: rj.code, Message: rj.msg}, true
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrTaken):
		// The same words whoever holds the id (a built-in, another of the user's plans, another
		// account): nothing about another account (plan ids are global; the app derives them
		// with the account, API.md).
		return ImportOutcome{Status: ImportRejected, Code: ImportIDTaken, Message: msgPlanIDTaken}, true
	case rejectable(err):
		return ImportOutcome{Status: ImportRejected, Code: ImportInvalid, Message: "a value is outside what can be stored"}, true
	}
	return ImportOutcome{}, false
}

// ---- plans --------------------------------------------------------------------------------

// ImportPlan stores one plan of an import: created, unchanged (SamePlan: nothing written),
// updated (replaced whole, as PUT /v1/plans/{id}), or rejected (id_taken for a built-in's,
// another account's, or another plan's id or day or exercise id; unknown_exercise;
// newer_on_server for a plan the app changed since the import wrote it, or made itself).
// The caller has run train.CheckPlan. A returned error is a database failure.
func (s *Store) ImportPlan(ctx context.Context, userID uuid.UUID, p model.Plan) (ImportOutcome, error) {
	rules, err := s.importRules()
	if err != nil {
		return ImportOutcome{}, err
	}
	var out ImportOutcome
	err = s.unit(ctx, func(tx pgx.Tx) error {
		out = ImportOutcome{}
		own, exists, err := owner(ctx, tx, "plans", p.ID) // FOR UPDATE: a concurrent PUT of the plan waits
		switch {
		case err != nil:
			return err
		case exists && own == nil:
			return ErrForbidden
		case exists && *own != userID:
			return ErrTaken
		}
		if exists {
			stored, err := loadPlan(ctx, tx, userID, p.ID)
			if err != nil {
				return err
			}
			if rules.PlanConflict(stored) {
				return importReject{ImportNewerOnServer, "the plan was changed in the app after the import; it is left as it is"}
			}
			if rules.SamePlan(stored, p) {
				out.Status = ImportUnchanged
				return nil
			}
		}
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
				return importReject{ImportUnknownExercise, fmt.Sprintf("exercise %s is neither built-in nor yours", id)}
			}
		}
		if err := writePlan(ctx, tx, userID, p, true); err != nil {
			return err
		}
		out.Status = ImportUpdated
		if !exists {
			out.Status = ImportCreated
		}
		return nil
	})
	if err != nil {
		if o, ok := rejected(err); ok {
			return o, nil
		}
		return ImportOutcome{}, err
	}
	return out, nil
}

// ---- sessions -----------------------------------------------------------------------------

// ImportSession stores one completed session of an import in its own transaction, under the
// advisory lock a live PUT of the same id takes:
//
//   - not stored: created, origin import and import_source source;
//   - stored with another origin: rejected id_taken (also an exercise or set id that belongs
//     to another of the user's sessions);
//   - a reference (exercise, plan day, plan exercise) the user cannot see: rejected
//     unknown_exercise / unknown_plan_reference (plans of the same request are committed
//     first, so they are visible);
//   - stored and Conflict: rejected newer_on_server; stored and SameSession: unchanged, nothing
//     written, nothing queued;
//   - otherwise replaced whole: updated.
//
// A created or updated session whose day (and, when an update moved it, the previous day) is
// inside window is put into score_queue in the same transaction, so an interrupted request
// leaves its days pending and the retry finds them (D128). The load columns of the session
// (muscular_trimp, strain, volume_kg, workout_id) are scoring's and stay as they are.
// The caller has run train.CheckImportSession (status completed, ended_at set, every set
// completed). A returned error is a database failure that stops the request (503).
func (s *Store) ImportSession(ctx context.Context, userID uuid.UUID, source string, in model.Session, loc *time.Location, window DayWindow) (ImportOutcome, error) {
	rules, err := s.importRules()
	if err != nil {
		return ImportOutcome{}, err
	}
	in.Status, in.Origin, in.ImportSource = model.SessionCompleted, model.OriginImport, &source // what the rules compare (D126)
	day := localDay(in.StartedAt, loc)
	var out ImportOutcome
	err = s.unit(ctx, func(tx pgx.Tx) error {
		out = ImportOutcome{Day: day}
		if _, err := tx.Exec(ctx, lockSession, userID, in.ID); err != nil {
			return err
		}
		stored, err := loadSession(ctx, tx, userID, in.ID, true)
		if err != nil {
			return err
		}
		if stored != nil && stored.Origin != model.OriginImport {
			return importReject{ImportIDTaken, "the session id belongs to a session logged in the app"}
		}
		if err := importReferences(ctx, tx, userID, in); err != nil {
			return err
		}
		out.Exercises = exerciseIDs(in, stored)
		if stored != nil {
			if rules.Conflict(*stored, in) {
				return importReject{ImportNewerOnServer, "the session was changed after the import; it is left as it is"}
			}
			if rules.SameSession(*stored, in) {
				out.Status = ImportUnchanged
				return nil
			}
		}
		if err := writeSessionAs(ctx, tx, userID, in, stored != nil, source); err != nil {
			var p train.Problem
			if errors.As(err, &p) { // writeSession's only Problem: an id owned by another session
				return importReject{ImportIDTaken, "an exercise or set id belongs to another of your sessions"}
			}
			return err
		}
		out.Status = ImportCreated
		var days []string
		if stored != nil {
			out.Status, out.Replaced = ImportUpdated, stored
			if prev := localDay(stored.StartedAt, loc); prev != day {
				out.PreviousDay = prev
				days = append(days, prev)
			}
		}
		days = append(days, day)
		days = slices.DeleteFunc(days, func(d string) bool { return !window.Contains(d) })
		if len(days) > 0 {
			if err := queueDays(ctx, tx, userID, days); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if o, ok := rejected(err); ok {
			o.Day = day
			return o, nil
		}
		return ImportOutcome{}, err
	}
	return out, nil
}

func localDay(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

// exerciseIDs are the exercise ids of the request's session and of the stored one, once each.
func exerciseIDs(in model.Session, stored *model.Session) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, x := range []*model.Session{&in, stored} {
		if x == nil {
			continue
		}
		for _, e := range x.Exercises {
			if !seen[e.ExerciseID] {
				seen[e.ExerciseID] = true
				ids = append(ids, e.ExerciseID)
			}
		}
	}
	return ids
}

// importReferences is checkReferences with the import's rejection codes.
func importReferences(ctx context.Context, tx pgx.Tx, userID uuid.UUID, body model.Session) error {
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
			return importReject{ImportUnknownExercise, fmt.Sprintf("exercise %s is neither built-in nor yours", id)}
		}
	}
	if body.PlanDayID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plan_days WHERE id=$2 AND (user_id IS NULL OR user_id=$1))`,
			userID, *body.PlanDayID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return importReject{ImportUnknownPlanReference, "plan_day_id is neither a built-in plan's nor one of yours"}
		}
	}
	if len(peIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT id) FROM plan_exercises WHERE id = ANY($2) AND (user_id IS NULL OR user_id=$1)`,
			userID, peIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(uniq(peIDs)) {
			return importReject{ImportUnknownPlanReference, "a plan_exercise_id is neither a built-in plan's nor one of yours"}
		}
	}
	return nil
}

// ---- the lease ----------------------------------------------------------------------------

// ImportLeaseTTL is how long a lease lasts without being released: above the request's 50 s
// budget (the finish included) plus the release's own 5 s, so a working import never loses it.
// A pod that dies mid-import (a rollout) blocks that user's next import this long, at most.
const ImportLeaseTTL = 70 * time.Second

// AcquireImportLease takes the user's import lease for ttl (D132): at most one import per user
// at a time across every api pod. ok false when another import holds a lease that has not
// expired; ErrNotFound when the account does not exist. release gives the lease back; it is
// safe to call after the lease expired and was taken by another import (it then does nothing),
// and it works on a context the caller has cancelled.
//
// The lease is one conditional UPDATE of users.import_lease_until on the database's clock: one
// statement, no session state, so it holds through Supabase's transaction pooler (which a
// session advisory lock would not), and pods need no clock agreement.
func (s *Store) AcquireImportLease(ctx context.Context, userID uuid.UUID, ttl time.Duration) (release func(context.Context), ok bool, err error) {
	var until time.Time
	err = s.pool.QueryRow(ctx, `UPDATE users SET import_lease_until = now() + make_interval(secs => $2)
		WHERE id = $1 AND (import_lease_until IS NULL OR import_lease_until < now())
		RETURNING import_lease_until`, userID, ttl.Seconds()).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, ErrNotFound
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	release = func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Only our own lease: the value we set is the token. A failure is not worth more than
		// the lease's expiry, which the next import waits out.
		_, _ = s.pool.Exec(ctx, `UPDATE users SET import_lease_until = NULL WHERE id = $1 AND import_lease_until = $2`, userID, until)
	}
	return release, true, nil
}

// ---- the days -----------------------------------------------------------------------------

// PendingAmong returns those of days (local "2006-01-02" days) that are queued for scoring and not
// yet scored, sorted: the days an earlier, interrupted attempt of the same import left behind.
func (s *Store) PendingAmong(ctx context.Context, userID uuid.UUID, days []string) ([]string, error) {
	out := []string{}
	if len(days) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD') FROM score_queue
		WHERE user_id = $1 AND scored_at IS NULL AND day = ANY(SELECT d::date FROM unnest($2::text[]) d) ORDER BY day`, userID, days)
	if err != nil {
		return nil, err
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return append(out, got...), err
}

// DependentScope is scoring.DependentScope's fields (D135; the store cannot import scoring):
// which other sessions' stored load the changed sessions can move.
type DependentScope struct {
	Since           time.Time   // the oldest changed session's start: nothing before it depends on them
	Exercises       []uuid.UUID // the changed sessions' exercises
	Through         time.Time   // the newest changed session's history end + the e1RM window
	BodyweightSince *time.Time  // the oldest changed session with a body weight; nil: none has one
}

// DependentDays returns the local days, sorted and inside w, of the user's completed sessions that
// started after q.Since and either share one of q.Exercises and started before q.Through (their
// e1RM window holds a changed set), or have no body weight of their own, hold a bodyweight
// exercise and started after q.BodyweightSince (their body weight is the last one logged before
// them, which a changed session may now be): the rule of scoring.DependentScope.Covers (D128, D135).
// The changed sessions' own days may be among them; the caller has those anyway.
func (s *Store) DependentDays(ctx context.Context, userID uuid.UUID, q DependentScope, w DayWindow, loc *time.Location) ([]string, error) {
	out := []string{}
	if (len(q.Exercises) == 0 && q.BodyweightSince == nil) || w.To == "" {
		return out, nil
	}
	from, err := w.start(loc)
	if err != nil {
		return nil, fmt.Errorf("day window: %w", err)
	}
	if q.Since.After(from) {
		from = q.Since
	}
	// The range on started_at is the sessions_user_started index; the exercise and the
	// bodyweight exercise are EXISTS on session_exercises_exercise / the exercise's key.
	// Strictly after Since: the oldest changed session itself is queued by its own item.
	rows, err := s.pool.Query(ctx, `SELECT s.started_at FROM sessions s
		WHERE s.user_id = $1 AND s.status = 'completed' AND s.started_at > $2
		  AND ((s.started_at < $4 AND EXISTS (SELECT 1 FROM session_exercises e
		              WHERE e.user_id = s.user_id AND e.session_id = s.id AND e.exercise_id = ANY($3)))
		    OR ($5::timestamptz IS NOT NULL AND s.bodyweight_kg IS NULL AND s.started_at > $5
		        AND EXISTS (SELECT 1 FROM session_exercises e JOIN exercises x ON x.id = e.exercise_id
		                    WHERE e.user_id = s.user_id AND e.session_id = s.id AND x.load_type = 'bodyweight')))`,
		userID, from, q.Exercises, q.Through, q.BodyweightSince)
	if err != nil {
		return nil, err
	}
	starts, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range starts {
		if d := localDay(t, loc); w.Contains(d) && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out, nil
}

// FinishImport queues days for scoring and writes the request's one event (ev: events.Ingested
// with reason "import", D125) into the outbox in the same transaction (D101): the days are
// queued and announced together or not at all. It does nothing for an empty days list (no event
// either). The caller kicks the outbox relay after it returns.
func (s *Store) FinishImport(ctx context.Context, userID uuid.UUID, days []string, ev events.Message) error {
	return s.QueueDaysWith(ctx, userID, days, func(tx pgx.Tx) error {
		return outbox.Insert(ctx, tx, "api", ev)
	})
}
